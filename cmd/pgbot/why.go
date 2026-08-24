package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/pgrundev/pgbot/internal/store"
	"github.com/pgrundev/pgbot/internal/why"
	"github.com/spf13/cobra"
)

type whyFlags struct {
	window      time.Duration
	fingerprint string
	storePath   string
	maxChains   int
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
		Use:   "why [count]",
		Short: "Explain a regression from baseline history: symptom ← mechanism ← antecedent (offline)",
		Long: "Builds per-object time series from the stored snapshots, finds sustained\n" +
			"shifts, and connects them into causal chains — \"this query slowed 3.2×\n" +
			"because seq scans surged on orders after the table grew 18%\" — with the\n" +
			"numbers and onset times for every hop. Deterministic: the chains are computed\n" +
			"from Postgres's own counters across your history, never guessed. Runs offline\n" +
			"from the local store; each `pgbot inspect` adds one snapshot of history.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// `pgbot why 10` is the ergonomic form of --max-chains=10.
			if len(args) == 1 {
				n, err := strconv.Atoi(args[0])
				if err != nil || n < 1 {
					return fmt.Errorf("the argument is how many chains to show, e.g. `pgbot why 10` — got %q", args[0])
				}
				f.maxChains = n
			}
			return runWhy(cmd.OutOrStdout(), f)
		},
	}
	fl := cmd.Flags()
	fl.DurationVar(&f.window, "window", 7*24*time.Hour, "how far back to analyze")
	fl.StringVar(&f.fingerprint, "fingerprint", "", "which database (fingerprint or a unique prefix); required if the store holds more than one")
	fl.StringVar(&f.storePath, "store", "", "baseline DB path (default: XDG state dir)")
	fl.IntVar(&f.maxChains, "max-chains", 5, "how many chains to report, worst first (or pass it as the argument: `pgbot why 10`)")
	fl.BoolVar(&f.noColor, "no-color", false, "disable ANSI color")
	fl.BoolVar(&f.json, "json", false, "emit the report as JSON (why_schema_version 1.0.0)")
	return cmd
}

// computeWhy opens the store, resolves the database, and runs the analysis —
// shared by the command and the MCP tool.
func computeWhy(storePath, fpSpec string, window time.Duration, maxChains int) (why.Report, error) {
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
	report := why.Analyze(samples, events, why.Options{MaxChains: maxChains})
	// "Only 2 snapshots" while the listing said 26 reads as a bug: when the
	// store holds history the window cut off, say so and name the fix.
	if older := item.Count - len(samples); older > 0 && len(samples) < 3 {
		report.Notes = append(report.Notes, fmt.Sprintf(
			"the store holds %d more snapshot(s) for this database older than the %s window — widen it to reach them, e.g. --window %dh",
			older, window, int(window.Hours())*4))
	}
	return report, nil
}

func runWhy(w io.Writer, f whyFlags) error {
	report, err := computeWhy(f.storePath, f.fingerprint, f.window, f.maxChains)
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

// printWhy renders the narrative and the context a first-time reader needs:
// what was analyzed, how many regressions were found vs shown, one block per
// chain (symptom, then hops), the hedged confidence, and a how-to-read legend.
func printWhy(w io.Writer, r why.Report) {
	fmt.Fprintf(w, "why · %s · %d snapshots · %s → %s\n",
		r.Database, r.Snapshots, r.WindowStart.Format("Jan 2 15:04"), r.WindowEnd.Format("Jan 2 15:04"))
	for _, note := range r.Notes {
		fmt.Fprintf(w, "%s\n", note)
	}
	if r.Snapshots < 3 {
		return // the note above already says what to do
	}
	fmt.Fprintf(w, "analyzed %d quer%s and %d table%s from your stored history — found %d sustained regression%s",
		r.AnalyzedQueries, plural(r.AnalyzedQueries, "y", "ies"),
		r.AnalyzedTables, plural(r.AnalyzedTables, "", "s"),
		r.RegressionsFound, plural(r.RegressionsFound, "", "s"))
	if r.RegressionsFound > len(r.Chains) {
		fmt.Fprintf(w, " · showing the %d worst of %d (--max-chains for more)", len(r.Chains), r.RegressionsFound)
	}
	fmt.Fprint(w, "\n\n")
	if len(r.Chains) == 0 {
		fmt.Fprintln(w, "✓ nothing got measurably worse in the window — nothing to explain")
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
	fmt.Fprintln(w, "how to read this: each block is one chain — what regressed ← the mechanism ← what set it off,")
	fmt.Fprintln(w, "with the numbers and the time each shift started, computed from your snapshot history.")
	fmt.Fprintln(w, "More history sharpens it: every `pgbot inspect` adds one snapshot.")
}

// plural picks the singular or plural suffix.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
