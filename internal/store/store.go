// ABOUTME: Owns the SQLite connection and foundation schema lifecycle.
// ABOUTME: Applies ordered migrations transactionally and exposes health checks.
package store

import (
	"context"
	"database/sql"
	"fmt"
	_ "modernc.org/sqlite"
	"os"
	"path/filepath"
)

type Migration struct {
	Version    int
	Statements []string
}

var migrations = []Migration{
	{Version: 1},
	{Version: 2, Statements: []string{
		`CREATE TABLE moderators (id TEXT PRIMARY KEY, normalized_email TEXT NOT NULL UNIQUE)`,
		`CREATE TABLE polls (id TEXT PRIMARY KEY, owner_id TEXT NOT NULL REFERENCES moderators(id), question TEXT NOT NULL, deadline INTEGER NOT NULL, display_offset TEXT NOT NULL DEFAULT '', places INTEGER NOT NULL CHECK (places > 0), announce INTEGER NOT NULL DEFAULT 0 CHECK (announce IN (0,1)), state TEXT NOT NULL CHECK (state IN ('draft','open','paused','closed')), counting_status TEXT NOT NULL DEFAULT 'pending' CHECK (counting_status IN ('pending','awaiting_decision','succeeded','failed','no_votes')), version INTEGER NOT NULL CHECK (version > 0), created_at INTEGER NOT NULL DEFAULT 0, closed_at INTEGER)`,
		`CREATE TABLE options (poll_id TEXT NOT NULL REFERENCES polls(id) ON DELETE CASCADE, id TEXT NOT NULL, label TEXT NOT NULL, display_order INTEGER NOT NULL, PRIMARY KEY (poll_id,id), UNIQUE (poll_id,display_order))`,
		`CREATE TABLE participants (id TEXT PRIMARY KEY, poll_id TEXT NOT NULL REFERENCES polls(id) ON DELETE CASCADE, display_name TEXT NOT NULL DEFAULT '')`,
		`CREATE TABLE contacts (id TEXT PRIMARY KEY, poll_id TEXT NOT NULL REFERENCES polls(id) ON DELETE CASCADE, participant_id TEXT NOT NULL REFERENCES participants(id) ON DELETE CASCADE, delivery_email TEXT NOT NULL, normalized_email TEXT NOT NULL, UNIQUE (poll_id,normalized_email))`,
		`CREATE TABLE grants (id TEXT PRIMARY KEY, purpose TEXT NOT NULL, principal_id TEXT NOT NULL, poll_id TEXT REFERENCES polls(id) ON DELETE CASCADE, contact_id TEXT REFERENCES contacts(id) ON DELETE CASCADE, key_id TEXT NOT NULL, token_hash BLOB NOT NULL UNIQUE, claims_json BLOB NOT NULL, issued_at INTEGER NOT NULL, expires_at INTEGER NOT NULL, consumed_at INTEGER, revoked_at INTEGER)`,
		`CREATE TABLE sessions (token_hash BLOB PRIMARY KEY, purpose TEXT NOT NULL, principal_id TEXT NOT NULL, poll_id TEXT REFERENCES polls(id) ON DELETE CASCADE, csrf_hash BLOB NOT NULL, expires_at INTEGER NOT NULL, revoked_at INTEGER)`,
		`CREATE TABLE ballots (poll_id TEXT NOT NULL REFERENCES polls(id) ON DELETE CASCADE, participant_id TEXT NOT NULL REFERENCES participants(id) ON DELETE CASCADE, version INTEGER NOT NULL, preferences_json BLOB NOT NULL, accepted_at INTEGER NOT NULL, PRIMARY KEY (poll_id,participant_id))`,
		`CREATE TABLE count_snapshots (id TEXT PRIMARY KEY, poll_id TEXT NOT NULL UNIQUE REFERENCES polls(id) ON DELETE CASCADE, schema_version INTEGER NOT NULL, rule TEXT NOT NULL, input_fingerprint TEXT NOT NULL, input_json BLOB NOT NULL, created_at INTEGER NOT NULL)`,
		`CREATE TABLE count_decisions (snapshot_id TEXT NOT NULL REFERENCES count_snapshots(id) ON DELETE CASCADE, sequence INTEGER NOT NULL, request_fingerprint TEXT NOT NULL, decision_json BLOB NOT NULL, PRIMARY KEY (snapshot_id,sequence))`,
		`CREATE TABLE count_results (snapshot_id TEXT PRIMARY KEY REFERENCES count_snapshots(id) ON DELETE CASCADE, result_json BLOB NOT NULL, committed_at INTEGER NOT NULL)`,
		`CREATE TABLE work_items (id TEXT PRIMARY KEY, poll_id TEXT REFERENCES polls(id) ON DELETE CASCADE, kind TEXT NOT NULL, logical_key TEXT NOT NULL UNIQUE, due_at INTEGER NOT NULL, status TEXT NOT NULL, attempts INTEGER NOT NULL DEFAULT 0, claim_token TEXT, claim_expires_at INTEGER, failure_class TEXT)`,
		`CREATE TABLE deliveries (id TEXT PRIMARY KEY, work_id TEXT NOT NULL UNIQUE REFERENCES work_items(id) ON DELETE CASCADE, grant_id TEXT REFERENCES grants(id), contact_id TEXT REFERENCES contacts(id), recipient_email TEXT NOT NULL, message_kind TEXT NOT NULL, status TEXT NOT NULL, next_due INTEGER NOT NULL, smtp_outcome TEXT)`,
		`CREATE TABLE link_requests (request_hash BLOB NOT NULL, purpose TEXT NOT NULL, poll_id TEXT, submitted_at INTEGER NOT NULL, accepted INTEGER NOT NULL CHECK (accepted IN (0,1)))`,
		`CREATE INDEX link_requests_submitted_at ON link_requests(submitted_at)`,
		`CREATE INDEX work_items_due ON work_items(status,due_at,id)`,
	}},
}

type Store struct{ DB *sql.DB }

func Open(ctx context.Context, stateDirectory string) (*Store, error) {
	return OpenWithMigrations(ctx, stateDirectory, migrations)
}

func OpenWithMigrations(ctx context.Context, stateDirectory string, appliedMigrations []Migration) (*Store, error) {
	if stateDirectory == "" {
		return nil, fmt.Errorf("state directory is required")
	}
	info, err := os.Stat(stateDirectory)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("state directory is unavailable: %q", stateDirectory)
	}
	path := filepath.Join(stateDirectory, "stv-poll.sqlite")
	_, statErr := os.Stat(path)
	db, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	db.SetMaxOpenConns(1)
	st := &Store{DB: db}
	if err := st.migrate(ctx, appliedMigrations); err != nil {
		_ = db.Close()
		return nil, err
	}
	if os.IsNotExist(statErr) {
		if err := os.Chmod(path, 0o600); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("set database permissions: %w", err)
		}
	}
	return st, nil
}
func (s *Store) migrate(ctx context.Context, appliedMigrations []Migration) error {
	if len(appliedMigrations) == 0 {
		return fmt.Errorf("no migrations configured")
	}
	for index, migration := range appliedMigrations {
		if migration.Version <= 0 {
			return fmt.Errorf("migration version %d must be positive", migration.Version)
		}
		if index > 0 && migration.Version <= appliedMigrations[index-1].Version {
			return fmt.Errorf("migrations must be ordered by increasing version")
		}
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration: %w", err)
	}
	// Rollback is best-effort cleanup; a committed transaction returns sql.ErrTxDone.
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL)"); err != nil {
		return fmt.Errorf("create schema version: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO schema_version(version) SELECT 0 WHERE NOT EXISTS (SELECT 1 FROM schema_version)"); err != nil {
		return fmt.Errorf("initialize schema version: %w", err)
	}
	var version int
	if err := tx.QueryRowContext(ctx, "SELECT version FROM schema_version").Scan(&version); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if version > appliedMigrations[len(appliedMigrations)-1].Version {
		return fmt.Errorf("unsupported schema version %d", version)
	}
	for _, m := range appliedMigrations {
		if m.Version <= version {
			continue
		}
		for _, statement := range m.Statements {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("apply migration %d: %w", m.Version, err)
			}
		}
		if _, err := tx.ExecContext(ctx, "UPDATE schema_version SET version = ?", m.Version); err != nil {
			return fmt.Errorf("record migration %d: %w", m.Version, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration: %w", err)
	}
	return nil
}
func (s *Store) Healthy(ctx context.Context) bool { return s != nil && s.DB.PingContext(ctx) == nil }
func (s *Store) Close() error {
	if s == nil || s.DB == nil {
		return nil
	}
	return s.DB.Close()
}
