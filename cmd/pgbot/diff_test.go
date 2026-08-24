package main

import (
	"strings"
	"testing"
	"time"

	"github.com/pgrundev/pgbot/internal/model"
	"github.com/pgrundev/pgbot/internal/render"
	"github.com/pgrundev/pgbot/internal/store"
)

func TestResolveFingerprint(t *testing.T) {
	items := []store.ListItem{
		{Fingerprint: "abc123def456", Database: "prod"},
		{Fingerprint: "xyz789ghi012", Database: "staging"},
	}
	// A unique prefix resolves.
	if it, err := resolveFingerprint(items, "abc"); err != nil || it.Database != "prod" {
		t.Errorf("prefix should resolve to prod, got %+v %v", it, err)
	}
	// Database name resolves.
	if it, err := resolveFingerprint(items, "staging"); err != nil || it.Fingerprint != "xyz789ghi012" {
		t.Errorf("name should resolve, got %+v %v", it, err)
	}
	// Ambiguous / empty with multiple → error naming the choices.
	if _, err := resolveFingerprint(items, ""); err == nil || !strings.Contains(err.Error(), "pass --fingerprint") {
		t.Errorf("empty spec with 2 dbs must error, got %v", err)
	}
	// Unknown → error.
	if _, err := resolveFingerprint(items, "nope"); err == nil {
		t.Error("unknown spec must error")
	}
	// A single db resolves without a spec.
	if it, err := resolveFingerprint(items[:1], ""); err != nil || it.Database != "prod" {
		t.Errorf("single db should resolve without spec, got %+v %v", it, err)
	}
}

func TestPgssEvictedBetween(t *testing.T) {
	mk := func(dealloc int64) *model.Context {
		return &model.Context{Queries: &model.Queries{PgssDealloc: dealloc}}
	}
	if !pgssEvictedBetween(mk(5), mk(20)) {
		t.Error("a rising dealloc count means eviction happened between snapshots")
	}
	if pgssEvictedBetween(mk(20), mk(20)) {
		t.Error("no change in dealloc → no eviction")
	}
	if pgssEvictedBetween(&model.Context{}, &model.Context{}) {
		t.Error("nil Queries → not detectable, must be false")
	}
}

// The renderer must print the ACTUAL interval and flag a substitution rather than
// silently claiming the requested one (B4 requirement 1).
func TestDiffReport_flagsIntervalSubstitution(t *testing.T) {
	var out strings.Builder
	base := time.Date(2026, 8, 16, 5, 0, 0, 0, time.UTC)
	cur := base.Add(31 * time.Hour) // asked for 24h, nearest is 31h
	render.DiffReport(&out, render.DiffInput{
		Database: "prod", Fingerprint: "abc123", BaselineAt: base, CurrentAt: cur,
		Requested: 24 * time.Hour, Actual: 31 * time.Hour, Deltas: &model.Deltas{},
	})
	s := out.String()
	if !strings.Contains(s, "31h") {
		t.Errorf("must print the actual 31h interval:\n%s", s)
	}
	if !strings.Contains(s, "asked for") || !strings.Contains(s, "24h") {
		t.Errorf("must flag that 24h was requested but 31h compared:\n%s", s)
	}
}

// Six databases all named "postgres" must still be tellable apart: the
// disambiguation listing carries version, provider, and recency alongside
// the name — the identity the store has, given snapshots hold no host.
func TestListDatabases_disambiguatesSameName(t *testing.T) {
	now := time.Now().UTC()
	out := listDatabases([]store.ListItem{
		{Fingerprint: "d1531628fd9c58aa", Database: "postgres", Version: "17.4", Provider: "neon", Count: 26, Newest: now.Add(-2 * time.Hour)},
		{Fingerprint: "7c7c3ef0d65cbb00", Database: "postgres", Version: "15.12", Count: 11, Newest: now.Add(-3 * 24 * time.Hour)},
	})
	for _, want := range []string{"17.4", "neon", "15.12", "2h ago", "3d ago"} {
		if !strings.Contains(out, want) {
			t.Errorf("listing missing %q:\n%s", want, out)
		}
	}
}
