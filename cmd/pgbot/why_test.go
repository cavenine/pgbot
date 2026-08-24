package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pgrundev/pgbot/internal/model"
	"github.com/pgrundev/pgbot/internal/store"
)

// whyFixtureStore writes the flagship regression history into a temp store:
// query 42 on orders slows 8ms → 26ms per call at snapshot 3, seq scans surge
// with it, the table grows ~18% across the window.
func whyFixtureStore(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "baselines.db")
	writeWhyHistory(t, path)
	return path
}

// seedWhyFixture writes the same history into the DEFAULT store location —
// for tools that resolve the store themselves (set XDG_STATE_HOME first).
func seedWhyFixture(t *testing.T) {
	t.Helper()
	writeWhyHistory(t, "")
}

func writeWhyHistory(t *testing.T, path string) {
	t.Helper()
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	t0 := time.Now().UTC().Add(-6 * time.Hour)
	for i := 0; i < 6; i++ {
		calls := int64(1000 * (i + 1))
		var totalMS float64
		if i < 3 {
			totalMS = 8 * float64(calls)
		} else {
			totalMS = 8*3000 + 26*float64(calls-3000)
		}
		seq := int64(360 * i)
		if i >= 3 {
			seq = 360*3 + 180000*int64(i-2)
		}
		c := &model.Context{
			Fingerprint:   "whyfixture",
			CollectedAt:   t0.Add(time.Duration(i) * time.Hour),
			SchemaVersion: model.SchemaVersion,
			Server:        model.ServerInfo{Database: "app"},
			Queries: &model.Queries{Enabled: true, TotalExecMS: totalMS, Top: []model.QueryStat{{
				QueryID: 42, Query: "SELECT * FROM orders WHERE customer_id = $1",
				Calls: calls, TotalMS: totalMS,
			}}},
			Tables: &model.Tables{Top: []model.TableStat{{
				Schema: "public", Name: "orders",
				SeqScans: seq, TotalBytes: int64(1000e6 + float64(i)*60e6), LiveTuples: 1e6,
			}}},
		}
		if _, err := st.Save(c); err != nil {
			t.Fatal(err)
		}
	}
}

func runWhyCmd(t *testing.T, args ...string) string {
	t.Helper()
	cmd := newWhyCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("why failed: %v\n%s", err, buf.String())
	}
	return buf.String()
}

// The flagship narrative, end to end from a stored history: symptom with the
// factor, indented because/after chain, and hedged confidence wording.
func TestWhy_narrative(t *testing.T) {
	out := runWhyCmd(t, "--store", whyFixtureStore(t), "--no-color")
	for _, want := range []string{"slowed 3.2×", "because seq scans", "public.orders", "grew"} {
		if !strings.Contains(out, want) {
			t.Errorf("narrative missing %q:\n%s", want, out)
		}
	}
}

// --json emits the versioned Report shape.
func TestWhy_json(t *testing.T) {
	out := runWhyCmd(t, "--store", whyFixtureStore(t), "--json")
	var r struct {
		SchemaVersion string `json:"why_schema_version"`
		Database      string `json:"database"`
		Chains        []struct {
			Symptom    struct{ Text string }
			Confidence float64
		} `json:"chains"`
	}
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, out)
	}
	if r.SchemaVersion != "1.0.0" || r.Database != "app" || len(r.Chains) == 0 {
		t.Errorf("unexpected report: %+v", r)
	}
}

// An empty store gets the setup message, not an error.
func TestWhy_emptyStore(t *testing.T) {
	cmd := newWhyCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"--store", filepath.Join(t.TempDir(), "empty.db")})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "pgbot inspect") {
		t.Errorf("empty store must say to run pgbot inspect first, got err=%v out=%s", err, buf.String())
	}
}
