package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenInitializesAndReopensPersistentDatabase(t *testing.T) {
	directory := t.TempDir()
	ctx := context.Background()
	fixtureMigrations := []Migration{{Version: 1, Statements: []string{"CREATE TABLE parent (id INTEGER PRIMARY KEY)", "CREATE TABLE child (parent_id INTEGER REFERENCES parent(id))"}}}
	first, err := OpenWithMigrations(ctx, directory, fixtureMigrations)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.DB.ExecContext(ctx, "INSERT INTO parent(id) VALUES (1)"); err != nil {
		t.Fatal(err)
	}
	if _, err := first.DB.ExecContext(ctx, "INSERT INTO child(parent_id) VALUES (1)"); err != nil {
		t.Fatal(err)
	}
	if _, err := first.DB.ExecContext(ctx, "INSERT INTO child(parent_id) VALUES (99)"); err == nil {
		t.Fatal("foreign-key violation unexpectedly succeeded")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := OpenWithMigrations(ctx, directory, fixtureMigrations)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	var count int
	if err := second.DB.QueryRowContext(ctx, "SELECT count(*) FROM child").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("child count = %d, want 1", count)
	}
	if _, err := os.Stat(filepath.Join(directory, "stv-poll.sqlite")); err != nil {
		t.Fatal(err)
	}
}
