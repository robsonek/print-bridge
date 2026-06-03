// Package idempotency persists print-job idempotency keys to SQLite, enabling
// resume-by-key: a retried request replays a terminal result or resumes polling
// of an in-flight CUPS job instead of resubmitting.
package idempotency

import (
	"database/sql"
	"time"

	_ "modernc.org/sqlite"
)

type Record struct {
	Key          string
	ResponseJSON string
	CUPSJobID    string
	Terminal     bool
	CreatedAt    time.Time
}

type Store struct {
	db  *sql.DB
	ttl time.Duration
}

func Open(path string, ttlDays int) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	// WAL improves concurrent read/write tolerance for the agent's single writer.
	if _, err := db.Exec(`PRAGMA journal_mode=WAL;`); err != nil {
		return nil, err
	}
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS idempotency (
			key          TEXT PRIMARY KEY,
			response_json TEXT NOT NULL DEFAULT '',
			cups_job_id  TEXT NOT NULL DEFAULT '',
			terminal     INTEGER NOT NULL DEFAULT 0,
			created_at   TEXT NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_idem_created ON idempotency(created_at);
	`); err != nil {
		return nil, err
	}
	return &Store{db: db, ttl: time.Duration(ttlDays) * 24 * time.Hour}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// Get returns the record for key. Records older than TTL are treated as absent.
func (s *Store) Get(key string) (Record, bool, error) {
	var (
		r       Record
		term    int
		created string
	)
	err := s.db.QueryRow(
		`SELECT key, response_json, cups_job_id, terminal, created_at FROM idempotency WHERE key = ?`, key,
	).Scan(&r.Key, &r.ResponseJSON, &r.CUPSJobID, &term, &created)
	if err == sql.ErrNoRows {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, err
	}
	r.Terminal = term == 1
	if t, perr := time.Parse(time.RFC3339, created); perr == nil {
		r.CreatedAt = t
		if time.Since(t) > s.ttl {
			return Record{}, false, nil
		}
	}
	return r, true, nil
}

func (s *Store) SavePending(key, cupsJobID string) error {
	_, err := s.db.Exec(`
		INSERT INTO idempotency(key, cups_job_id, terminal, created_at)
		VALUES(?, ?, 0, ?)
		ON CONFLICT(key) DO UPDATE SET cups_job_id = excluded.cups_job_id`,
		key, cupsJobID, time.Now().UTC().Format(time.RFC3339))
	return err
}

func (s *Store) SaveTerminal(key, responseJSON string) error {
	_, err := s.db.Exec(`
		INSERT INTO idempotency(key, response_json, terminal, created_at)
		VALUES(?, ?, 1, ?)
		ON CONFLICT(key) DO UPDATE SET response_json = excluded.response_json, terminal = 1`,
		key, responseJSON, time.Now().UTC().Format(time.RFC3339))
	return err
}

// Cleanup deletes rows older than TTL relative to now. Returns rows removed.
func (s *Store) Cleanup(now time.Time) (int64, error) {
	cutoff := now.Add(-s.ttl).UTC().Format(time.RFC3339)
	res, err := s.db.Exec(`DELETE FROM idempotency WHERE created_at < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
