package main

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/pgrundev/pgbot/internal/store"
	"github.com/pgrundev/pgbot/internal/why"
	"github.com/spf13/cobra"
)

type whyFlags struct {
	window      time.Duration
	fingerprint string
	storePath   string
	noColor     bool
	json        bool
}

// newWhyCmd answers "why did this get slow?" from the local baseline store —
// fully offline, like diff: every `pgbot inspect` already stored a snapshot,
// and the why-engine connects the onsets across them into causal chains. No
// model generates anything; same contract as the findings engine.
func newWhyCmd() *cobra.Command {
	var f whyFlags
	cmd := &cobra.Command{
		Use:   "why",
		Short: "Explain a regression from baseline history: symptom ← mechanism ← antecedent (offline)",
		Long: "Builds per-object time series from the stored snapshots, finds sustained\n" +
			"shifts, and connects them into causal chains — \"this query slowed 3.2×\n" +
			"because seq scans surged on orders after the table grew 18%\" — with the\n" +
			"numbers and onset times for every hop. Deterministic: the chains are computed\n" +
			"from Postgres's own counters across your history, never guessed. Runs offline\n" +
			"from the local store; each `pgbot inspect` adds one snapshot of history.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runWhy(cmd.OutOrStdout(), f)
		},
	}
	fl := cmd.Flags()
	fl.DurationVar(&f.window, "window", 7*24*time.Hour, "how far back to analyze")
	fl.StringVar(&f.fingerprint, "fingerprint", "", "which database (fingerprint or a unique prefix); required if the store holds more than one")
	fl.StringVar(&f.storePath, "store", "", "baseline DB path (default: XDG state dir)")
	fl.BoolVar(&f.noColor, "no-color", false, "disable ANSI color")
	fl.BoolVar(&f.json, "json", false, "emit the report as JSON (why_schema_version 1.0.0)")
	return cmd
}

// computeWhy opens the store, resolves the database, and runs the analysis —
// shared by the command and the MCP tool.
func computeWhy(storePath, fpSpec string, window time.Duration) (why.Report, error) {
	st, err := store.Open(storePath)
	if err != nil {
		return why.Report{}, fmt.Errorf("open baseline store: %w", err)
	}
	defer st.Close()

	items, err := st.List()
	if err != nil {
		return why.Report{}, err
	}
	if len(items) == 0 {
		return why.Report{}, fmt.Errorf("no baselines yet — run `pgbot inspect` a few times first; each run stores one snapshot of history")
	}
	item, err := resolveFingerprint(items, fpSpec)
	if err != nil {
		return why.Report{}, err
	}

	snaps, err := st.LoadRange(item.Fingerprint, time.Now().UTC().Add(-window))
	if err != nil {
		return why.Report{}, err
	}
	samples := make([]why.Sample, len(snaps))
	for i, s := range snaps {
		samples[i] = why.Sample{At: s.CollectedAt, C: s.Context}
	}
	events, err := st.RecentEvents(item.Fingerprint, 200)
	if err != nil { // events enrich antecedents; their absence must not block the analysis
		events = nil
	}
	return why.Analyze(samples, events, why.Options{}), nil
}

func runWhy(w io.Writer, f whyFlags) error {
	report, err := computeWhy(f.storePath, f.fingerprint, f.window)
	if err != nil {
		return err
	}

	if f.json {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	}
	printWhy(w, report)
	return nil
}

// printWhy renders the narrative: one block per chain — the symptom line,
// then each hop indented, then the hedged confidence.
func printWhy(w io.Writer, r why.Report) {
	fmt.Fprintf(w, "why · %s · %d snapshots · %s → %s\n\n",
		r.Database, r.Snapshots, r.WindowStart.Format("Jan 2 15:04"), r.WindowEnd.Format("Jan 2 15:04"))
	for _, note := range r.Notes {
		fmt.Fprintf(w, "%s\n", note)
	}
	if len(r.Chains) == 0 && len(r.Notes) == 0 {
		fmt.Fprintln(w, "✓ no sustained regressions found in the window — nothing to explain")
		return
	}
	for _, ch := range r.Chains {
		fmt.Fprintf(w, "● %s\n", ch.Symptom.Text)
		for _, h := range ch.Hops {
			fmt.Fprintf(w, "    ↳ %s\n", h.Text)
		}
		if ch.Confidence < 0.5 {
			fmt.Fprintf(w, "    possibly — confidence %.0f%%: no mechanism found in the stored history; the cause may be outside what pgbot collects\n", ch.Confidence*100)
		} else {
			fmt.Fprintf(w, "    confidence %.0f%% — computed from onset alignment across %d snapshots\n", ch.Confidence*100, r.Snapshots)
		}
		fmt.Fprintln(w)
	}
}
