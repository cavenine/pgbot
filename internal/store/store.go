// Package store is the local baseline persistence — the thing that makes pgbot
// temporal rather than just a stats reader. Every inspect run writes a snapshot;
// the diff package reads them back to say what changed. Pure-Go SQLite
// (modernc.org/sqlite) keeps the binary CGO-free.
package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/pgrundev/pgbot/internal/model"
	_ "modernc.org/sqlite"
)

type Store struct {
	db   *sql.DB
	path string
}

// Snapshot is one stored inspect run.
type Snapshot struct {
	ID          int64
	Fingerprint string
	CollectedAt time.Time
	Context     *model.Context
}

// Open opens (creating if needed) the baseline DB under the state dir and runs
// migrations. Pass "" for the default location.
func Open(path string) (*Store, error) {
	if path == "" {
		var err error
		if path, err = defaultPath(); err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // one writer; the workload is a single CLI run
	s := &Store{db: db, path: path}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }
func (s *Store) Path() string { return s.path }

// defaultPath is $XDG_STATE_HOME/pgbot/baselines.db, else ~/.local/state/pgbot/.
func defaultPath() (string, error) {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "pgbot", "baselines.db"), nil
}

// Fingerprint is a stable key for a target database. Preferring the system
// identifier means a restored/renamed replica is recognised as the same
// database; host+port+db is the fallback when the identifier isn't readable.
//
// The system identifier is CLUSTER-wide, so it must be combined with the
// database name — otherwise two databases on one cluster (app_prod, analytics)
// share a fingerprint and their snapshots interleave into one fictional series,
// corrupting every delta. host+port+db already includes the database name.
func Fingerprint(host, port, dbname, systemIdentifier string) string {
	seed := host + "|" + port + "|" + dbname
	if systemIdentifier != "" {
		seed = systemIdentifier + "|" + dbname
	}
	sum := sha256.Sum256([]byte(seed))
	return fmt.Sprintf("%x", sum[:8])
}

// Save writes one snapshot, extracting the scalar columns worth trending, then
// applies retention.
func (s *Store) Save(c *model.Context) (int64, error) {
	raw, err := json.Marshal(c)
	if err != nil {
		return 0, err
	}
	sc := extractScalars(c)
	res, err := s.db.Exec(`
		INSERT INTO snapshots
		  (fingerprint, collected_at, schema_version, context_json,
		   tps, cache_hit, connections, db_size_bytes, dead_ratio_max, longest_xact_s)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.Fingerprint, c.CollectedAt.UTC().Unix(), c.SchemaVersion, string(raw),
		sc.tps, sc.cacheHit, sc.connections, sc.dbSize, sc.deadRatioMax, sc.longestXact)
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	if err := s.prune(c.Fingerprint); err != nil {
		return id, err // saved, but surface prune trouble
	}
	return id, nil
}

// Previous returns the most recent snapshot at least minAge old (so we compare
// against a distinct earlier state, not the run we just wrote), or nil.
func (s *Store) Previous(fingerprint string, now time.Time, minAge time.Duration) (*Snapshot, error) {
	cutoff := now.Add(-minAge).Unix()
	return s.one(`SELECT id, fingerprint, collected_at, context_json
		FROM snapshots WHERE fingerprint = ? AND collected_at <= ?
		ORDER BY collected_at DESC LIMIT 1`, fingerprint, cutoff)
}

// SameHourYesterday returns the snapshot closest to 24h ago (±30 min), or nil.
func (s *Store) SameHourYesterday(fingerprint string, now time.Time) (*Snapshot, error) {
	target := now.Add(-24 * time.Hour).Unix()
	lo, hi := target-1800, target+1800
	return s.one(`SELECT id, fingerprint, collected_at, context_json
		FROM snapshots WHERE fingerprint = ? AND collected_at BETWEEN ? AND ?
		ORDER BY abs(collected_at - ?) ASC LIMIT 1`, fingerprint, lo, hi, target)
}

// Trend returns up to n recent scalar values of one metric for sparklines,
// oldest-first.
func (s *Store) Trend(fingerprint, column string, n int) ([]float64, error) {
	if !allowedScalar[column] {
		return nil, fmt.Errorf("unknown trend column %q", column) // guard: column is interpolated
	}
	rows, err := s.db.Query(fmt.Sprintf(
		`SELECT %s FROM snapshots WHERE fingerprint = ? AND %s IS NOT NULL
		 ORDER BY collected_at DESC LIMIT ?`, column, column), fingerprint, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var vals []float64
	for rows.Next() {
		var v float64
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		vals = append(vals, v)
	}
	// reverse to oldest-first
	for i, j := 0, len(vals)-1; i < j; i, j = i+1, j-1 {
		vals[i], vals[j] = vals[j], vals[i]
	}
	return vals, rows.Err()
}

// LoadRange returns the fingerprint's snapshots newer than since, oldest
// first, contexts decoded — the input the why-engine's series builder walks.
func (s *Store) LoadRange(fingerprint string, since time.Time) ([]Snapshot, error) {
	rows, err := s.db.Query(`SELECT id, fingerprint, collected_at, context_json
		FROM snapshots WHERE fingerprint = ? AND collected_at >= ?
		ORDER BY collected_at ASC`, fingerprint, since.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Snapshot
	for rows.Next() {
		var (
			snap Snapshot
			unix int64
			raw  string
		)
		if err := rows.Scan(&snap.ID, &snap.Fingerprint, &unix, &raw); err != nil {
			return nil, err
		}
		snap.CollectedAt = time.Unix(unix, 0).UTC()
		var c model.Context
		if err := json.Unmarshal([]byte(raw), &c); err != nil {
			return nil, err
		}
		snap.Context = &c
		out = append(out, snap)
	}
	return out, rows.Err()
}

func (s *Store) one(q string, args ...any) (*Snapshot, error) {
	var (
		snap Snapshot
		unix int64
		raw  string
	)
	err := s.db.QueryRow(q, args...).Scan(&snap.ID, &snap.Fingerprint, &unix, &raw)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	snap.CollectedAt = time.Unix(unix, 0).UTC()
	var c model.Context
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		return nil, err
	}
	snap.Context = &c
	return &snap, nil
}
