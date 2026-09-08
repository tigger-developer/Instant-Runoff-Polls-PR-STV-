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

var migrations = []Migration{{Version: 1}}

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
