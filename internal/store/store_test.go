package store

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/pgrundev/pgbot/internal/model"
)

func tempStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "b.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func ctxAt(fp string, at time.Time, tps float64) *model.Context {
	v := tps
	return &model.Context{
		SchemaVersion: model.SchemaVersion, Fingerprint: fp, CollectedAt: at,
		Health: &model.Health{Connections: 10, TPS: &v},
		Tables: &model.Tables{DBSizeBytes: 1 << 30},
	}
}

func TestSaveAndPrevious(t *testing.T) {
	st := tempStore(t)
	now := time.Now().UTC()
	fp := "abc123"

	if _, err := st.Save(ctxAt(fp, now.Add(-30*time.Minute), 100)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Save(ctxAt(fp, now, 200)); err != nil {
		t.Fatal(err)
	}

	prev, err := st.Previous(fp, now, 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if prev == nil {
		t.Fatal("expected a baseline ≥15min old")
	}
	if prev.Context.Health.TPS == nil || *prev.Context.Health.TPS != 100 {
		t.Errorf("expected the 30-min-old snapshot (tps 100), got %+v", prev.Context.Health)
	}
}

func TestFingerprint_prefersSystemIdentifier(t *testing.T) {
	a := Fingerprint("h1", "5432", "db", "sysid-42")
	b := Fingerprint("DIFFERENT-HOST", "6000", "db", "sysid-42")
	if a != b {
		t.Error("same system identifier + same database must yield the same fingerprint regardless of host")
	}
	c := Fingerprint("h1", "5432", "db", "")
	if c == a {
		t.Error("fallback fingerprint should differ from the system-identifier one")
	}
}

// P0-1: two databases on the SAME cluster (same system identifier) must not
// collide, or their snapshots interleave into one fictional baseline series.
func TestFingerprint_perDatabaseWithinCluster(t *testing.T) {
	appProd := Fingerprint("h1", "5432", "app_prod", "sysid-42")
	analytics := Fingerprint("h1", "5432", "analytics", "sysid-42")
	if appProd == analytics {
		t.Error("same system identifier but different database must yield different fingerprints")
	}
	// Fallback path already includes dbname; confirm it too stays per-database.
	if Fingerprint("h1", "5432", "app_prod", "") == Fingerprint("h1", "5432", "analytics", "") {
		t.Error("fallback fingerprints must also differ per database")
	}
}

// P0-1 migration: a store written before per-database fingerprints must open
// cleanly after upgrade and surface a one-time notice (only when history exists).
func TestFingerprintScheme_migrationNoticeOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a pre-v2 store: it has history but no scheme marker.
	if _, err := st.Save(ctxAt("oldfp", time.Now().UTC(), 100)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`DELETE FROM meta WHERE key = 'fingerprint_scheme'`); err != nil {
		t.Fatal(err)
	}
	st.Close()

	// Reopen — Open runs migrate, which must succeed and flag the transition.
	st2, err := Open(path)
	if err != nil {
		t.Fatalf("old-scheme store must open without error after upgrade: %v", err)
	}
	t.Cleanup(func() { st2.Close() })
	if got := st2.UpgradeNotice(); got == "" {
		t.Error("expected a one-time upgrade notice after crossing the fingerprint scheme change with history")
	}
	if got := st2.UpgradeNotice(); got != "" {
		t.Error("upgrade notice must fire only once")
	}
}

// A fresh store (no prior history) upgrades silently — no notice.
func TestFingerprintScheme_freshStoreNoNotice(t *testing.T) {
	st := tempStore(t)
	if got := st.UpgradeNotice(); got != "" {
		t.Errorf("a fresh store must not emit an upgrade notice, got %q", got)
	}
}

func TestTrend_guardsColumnName(t *testing.T) {
	st := tempStore(t)
	if _, err := st.Trend("fp", "tps; DROP TABLE snapshots", 10); err == nil {
		t.Error("Trend must reject an unknown/injected column name")
	}
}

func TestListPruneExport(t *testing.T) {
	st := tempStore(t)
	fp := "fp1"
	for i := 0; i < 3; i++ {
		if _, err := st.Save(ctxAt(fp, time.Now().UTC().Add(time.Duration(-i)*time.Hour), float64(100*i))); err != nil {
			t.Fatal(err)
		}
	}
	items, err := st.List()
	if err != nil || len(items) != 1 || items[0].Count != 3 {
		t.Fatalf("expected 1 group of 3, got %+v (%v)", items, err)
	}

	var buf bytes.Buffer
	if err := st.Export(fp, &buf); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(buf.Bytes(), []byte(`"schema_version"`)) {
		t.Error("export should contain full Context JSON")
	}

	n, err := st.Prune(fp)
	if err != nil || n != 3 {
		t.Fatalf("expected to prune 3, got %d (%v)", n, err)
	}
	if items, _ := st.List(); len(items) != 0 {
		t.Errorf("store should be empty after prune, got %+v", items)
	}
}

func TestSaveWaitProfile_accumulateAndFold(t *testing.T) {
	st := tempStore(t)
	fp := "waitfp"
	prof := func(lock, cpu int) *model.WaitProfile {
		return &model.WaitProfile{Available: true, Samples: lock + cpu, Buckets: []model.WaitBucket{
			{Type: "Lock", Count: lock, Events: []model.WaitEvent{{Event: "transactionid", Count: lock}}},
			{Type: "CPU", Count: cpu},
		}}
	}
	// Two profiles in the SAME minute accumulate into one minute bucket.
	base := time.Date(2026, 8, 12, 10, 30, 20, 0, time.UTC)
	if err := st.SaveWaitProfile(fp, base, prof(30, 10)); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveWaitProfile(fp, base.Add(15*time.Second), prof(10, 5)); err != nil {
		t.Fatal(err)
	}
	var lockSamples int
	err := st.db.QueryRow(`SELECT samples FROM wait_rollups
		WHERE target_id=? AND granularity='minute' AND wait_type='Lock' AND wait_event='transactionid'`, fp).Scan(&lockSamples)
	if err != nil {
		t.Fatal(err)
	}
	if lockSamples != 40 {
		t.Errorf("same-minute Lock samples should accumulate to 40, got %d", lockSamples)
	}

	// A profile far in the past should end up folded into an hourly bucket by a
	// later run's prune (minute rows past the 7d horizon are folded).
	old := base.Add(-10 * 24 * time.Hour)
	if err := st.SaveWaitProfile(fp, old, prof(5, 5)); err != nil {
		t.Fatal(err)
	}
	// Trigger prune again with a current-time save so the old minute rows fold.
	if err := st.SaveWaitProfile(fp, base.Add(2*time.Minute), prof(1, 1)); err != nil {
		t.Fatal(err)
	}
	var oldMinute, oldHour int
	st.db.QueryRow(`SELECT count(*) FROM wait_rollups WHERE target_id=? AND granularity='minute' AND bucket_ts < ?`,
		fp, base.Add(-7*24*time.Hour).Unix()).Scan(&oldMinute)
	st.db.QueryRow(`SELECT count(*) FROM wait_rollups WHERE target_id=? AND granularity='hour'`, fp).Scan(&oldHour)
	if oldMinute != 0 {
		t.Errorf("aged minute rows should be folded away, %d remain", oldMinute)
	}
	if oldHour == 0 {
		t.Error("aged minute rows should have been folded into hourly buckets")
	}
}

func TestPrune_thinsAgedToHourlyAndDropsAncient(t *testing.T) {
	st := tempStore(t)
	fp := "ret"
	now := time.Now().UTC()
	save := func(at time.Time) {
		if _, err := st.Save(ctxAt(fp, at, 1)); err != nil {
			t.Fatal(err)
		}
	}

	// Past the 90-day horizon → dropped (even on its own Save's prune).
	save(now.Add(-100 * 24 * time.Hour))
	// Three in the SAME hour ~8 days ago (older than the 7-day full-resolution
	// window) → thinned to one per hour bucket.
	oldHour := now.Add(-8 * 24 * time.Hour).Truncate(time.Hour).Add(30 * time.Minute)
	save(oldHour)
	save(oldHour.Add(1 * time.Minute))
	save(oldHour.Add(2 * time.Minute))
	// Recent → kept at full resolution.
	save(now.Add(-1 * time.Hour))

	count := func(where string, args ...any) int {
		var n int
		if err := st.db.QueryRow("SELECT count(*) FROM snapshots WHERE fingerprint=? AND "+where, append([]any{fp}, args...)...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count("collected_at < ?", now.Add(-90*24*time.Hour).Unix()); n != 0 {
		t.Errorf("snapshots past the 90-day horizon should be dropped, %d remain", n)
	}
	if n := count("collected_at BETWEEN ? AND ?", oldHour.Add(-time.Hour).Unix(), oldHour.Add(time.Hour).Unix()); n != 1 {
		t.Errorf("three same-hour aged snapshots should thin to 1, got %d", n)
	}
	if n := count("collected_at > ?", now.Add(-2*time.Hour).Unix()); n != 1 {
		t.Errorf("the recent snapshot should be kept, got %d", n)
	}
}

func TestTrendAndSameHourYesterday(t *testing.T) {
	st := tempStore(t)
	fp := "trend"
	now := time.Now().UTC()

	// A yesterday-same-hour snapshot for the diff baseline.
	yday := now.Add(-24 * time.Hour)
	if _, err := st.Save(ctxAt(fp, yday, 500)); err != nil {
		t.Fatal(err)
	}
	// A short recent series for the sparkline trend.
	for i, tps := range []float64{100, 200, 300} {
		if _, err := st.Save(ctxAt(fp, now.Add(time.Duration(-2+i)*time.Minute), tps)); err != nil {
			t.Fatal(err)
		}
	}

	series, err := st.Trend(fp, "tps", 24)
	if err != nil {
		t.Fatal(err)
	}
	if len(series) < 3 {
		t.Errorf("Trend should return the recent tps series, got %v", series)
	}

	snap, err := st.SameHourYesterday(fp, now)
	if err != nil {
		t.Fatal(err)
	}
	if snap == nil {
		t.Fatal("SameHourYesterday should find the ~24h-old snapshot")
	}
	if snap.Context.Health == nil || snap.Context.Health.TPS == nil || *snap.Context.Health.TPS != 500 {
		t.Errorf("wrong yesterday snapshot: %+v", snap.Context.Health)
	}
}

// enforceSizeCap must actually shrink the file. DELETE alone never reclaims
// space in a WAL-mode SQLite database, so without the VACUUM the page count
// stays above the cap forever and every later run evicts another 10% — eating
// the whole history. This test lowers the cap, pushes the file over it with
// direct inserts (bypassing Save's prune), then calls enforceSizeCap once and
// asserts the eviction round both removes rows AND reclaims the space.
func TestEnforceSizeCap_evictsAndVacuums(t *testing.T) {
	st := tempStore(t)
	fp := "cap"
	now := time.Now().UTC()

	// Lower the cap for the test and restore it after.
	saved := maxBytes
	maxBytes = 96 << 10 // 96 KiB
	t.Cleanup(func() { maxBytes = saved })

	// Insert rows directly (Save would prune on every call). Each carries a
	// padded context_json so a handful of rows push the file over the cap.
	pad := make([]byte, 8<<10) // 8 KiB of padding per row
	for i := range pad {
		pad[i] = 'x'
	}
	blob := `{"pad":"` + string(pad) + `"}`
	for i := 0; i < 30; i++ {
		if _, err := st.db.Exec(
			`INSERT INTO snapshots (fingerprint, collected_at, schema_version, context_json) VALUES (?, ?, ?, ?)`,
			fp, now.Add(time.Duration(i)*time.Minute).Unix(), model.SchemaVersion, blob); err != nil {
			t.Fatal(err)
		}
	}

	fileSize := func() int64 {
		var pageCount, pageSize int64
		if err := st.db.QueryRow(`PRAGMA page_count`).Scan(&pageCount); err != nil {
			t.Fatal(err)
		}
		if err := st.db.QueryRow(`PRAGMA page_size`).Scan(&pageSize); err != nil {
			t.Fatal(err)
		}
		return pageCount * pageSize
	}
	before := fileSize()
	if before <= maxBytes {
		t.Fatalf("test setup: file should start over the cap, got %d <= %d", before, maxBytes)
	}

	if err := st.enforceSizeCap(); err != nil {
		t.Fatal(err)
	}

	after := fileSize()
	// The regression this guards: DELETE alone never shrinks a WAL-mode file, so
	// the old code left `after == before` and every later run evicted again. The
	// fix VACUUMs, so the file must actually get smaller. (Whether it lands at or
	// just under the cap depends on SQLite's 4 KB page granularity; convergence
	// to the cap happens across successive Saves. What must never happen is the
	// file staying the same size after an eviction round.)
	if after >= before {
		t.Errorf("file did not shrink after eviction+VACUUM: before=%d after=%d (space not reclaimed)", before, after)
	}

	// Rows were evicted oldest-first, and some remain.
	var n int
	if err := st.db.QueryRow(`SELECT count(*) FROM snapshots WHERE fingerprint=?`, fp).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Error("eviction removed every row — the cap loop over-ran")
	}
	if n >= 30 {
		t.Errorf("expected some rows evicted, still have %d", n)
	}
}

// The upgrade path the fix must survive: the pre-VACUUM code let free pages
// accumulate, so a file can be over the cap with almost no live rows. A
// VACUUM alone reclaims that space — eviction must not fire first and delete
// the snapshot Save just wrote (empirically: DELETE-before-VACUUM left 0 rows).
func TestEnforceSizeCap_vacuumFirstPreservesFreshRow(t *testing.T) {
	st := tempStore(t)
	fp := "upgrade"
	now := time.Now().UTC()

	saved := maxBytes
	maxBytes = 96 << 10
	t.Cleanup(func() { maxBytes = saved })

	// Bloat the file with rows, then delete all but ONE without vacuuming —
	// exactly the state an old-code store is in after 90-day retention ran.
	pad := make([]byte, 8<<10)
	for i := range pad {
		pad[i] = 'x'
	}
	blob := `{"pad":"` + string(pad) + `"}`
	for i := 0; i < 30; i++ {
		if _, err := st.db.Exec(
			`INSERT INTO snapshots (fingerprint, collected_at, schema_version, context_json) VALUES (?, ?, ?, ?)`,
			fp, now.Add(time.Duration(i)*time.Minute).Unix(), model.SchemaVersion, blob); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.db.Exec(
		`DELETE FROM snapshots WHERE id NOT IN (SELECT max(id) FROM snapshots)`); err != nil {
		t.Fatal(err)
	}

	if err := st.enforceSizeCap(); err != nil {
		t.Fatal(err)
	}

	var n int
	if err := st.db.QueryRow(`SELECT count(*) FROM snapshots`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("the one live snapshot must survive a free-page-only overage, got %d rows", n)
	}
}

// When deleting snapshots cannot get the file under the cap (another table
// holds the bulk), the loop must keep at least the newest snapshot rather
// than eating the row every Save just wrote, forever.
func TestEnforceSizeCap_neverDeletesLastRow(t *testing.T) {
	st := tempStore(t)
	saved := maxBytes
	maxBytes = 1 // impossible cap: nothing can ever satisfy it
	t.Cleanup(func() { maxBytes = saved })

	if _, err := st.db.Exec(
		`INSERT INTO snapshots (fingerprint, collected_at, schema_version, context_json) VALUES (?, ?, ?, ?)`,
		"only", time.Now().UTC().Unix(), model.SchemaVersion, `{}`); err != nil {
		t.Fatal(err)
	}
	if err := st.enforceSizeCap(); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := st.db.QueryRow(`SELECT count(*) FROM snapshots`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("an unreachable cap must not delete the last snapshot, got %d rows", n)
	}
}

// LoadRange returns the fingerprint's snapshots inside the window, oldest
// first, with decoded contexts — the input the why-engine's series builder
// walks. Other fingerprints and older rows stay out.
func TestLoadRange(t *testing.T) {
	st := tempStore(t)
	now := time.Now().UTC()
	save := func(fp string, age time.Duration, db string) {
		c := &model.Context{Server: model.ServerInfo{Database: db}}
		if _, err := st.db.Exec(
			`INSERT INTO snapshots (fingerprint, collected_at, schema_version, context_json) VALUES (?, ?, ?, ?)`,
			fp, now.Add(-age).Unix(), model.SchemaVersion, mustJSON(t, c)); err != nil {
			t.Fatal(err)
		}
	}
	save("db1", 9*24*time.Hour, "too-old")
	save("db1", 3*24*time.Hour, "in-window-a")
	save("db1", 1*24*time.Hour, "in-window-b")
	save("db2", 1*time.Hour, "other-fingerprint")

	snaps, err := st.LoadRange("db1", now.Add(-7*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 2 {
		t.Fatalf("expected the 2 in-window snapshots, got %d", len(snaps))
	}
	if !snaps[0].CollectedAt.Before(snaps[1].CollectedAt) {
		t.Error("snapshots must come back oldest first")
	}
	if snaps[0].Context == nil || snaps[0].Context.Server.Database != "in-window-a" {
		t.Errorf("contexts must be decoded, got %+v", snaps[0].Context)
	}
}

func mustJSON(t *testing.T, c *model.Context) string {
	t.Helper()
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
