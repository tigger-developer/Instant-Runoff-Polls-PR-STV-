package store

import (
	"context"
	"database/sql"
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

func TestOpenRejectsUnsupportedSchemaAndRollsBackFailedMigration(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	first, err := OpenWithMigrations(ctx, directory, []Migration{{Version: 1, Statements: []string{"CREATE TABLE retained (id INTEGER PRIMARY KEY)"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.DB.ExecContext(ctx, "INSERT INTO retained(id) VALUES (1)"); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	failed := []Migration{
		{Version: 1, Statements: []string{"CREATE TABLE retained (id INTEGER PRIMARY KEY)"}},
		{Version: 2, Statements: []string{"CREATE TABLE transient_table (id INTEGER PRIMARY KEY)", "THIS IS NOT SQL"}},
	}
	if _, err := OpenWithMigrations(ctx, directory, failed); err == nil {
		t.Fatal("failed migration unexpectedly succeeded")
	}
	reopened, err := OpenWithMigrations(ctx, directory, []Migration{{Version: 1, Statements: []string{"CREATE TABLE retained (id INTEGER PRIMARY KEY)"}}})
	if err != nil {
		t.Fatalf("reopen after failed migration: %v", err)
	}
	defer reopened.Close()
	var count int
	if err := reopened.DB.QueryRowContext(ctx, "SELECT count(*) FROM retained").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("retained rows = %d", count)
	}
	var table string
	err = reopened.DB.QueryRowContext(ctx, "SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'transient_table'").Scan(&table)
	if err != sql.ErrNoRows {
		t.Fatalf("failed migration left transient table: %q, %v", table, err)
	}

	if _, err := reopened.DB.ExecContext(ctx, "UPDATE schema_version SET version = 99"); err != nil {
		t.Fatal(err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenWithMigrations(ctx, directory, []Migration{{Version: 1}}); err == nil {
		t.Fatal("newer schema unexpectedly opened")
	}
}

func TestOpenRejectsMissingStateDirectory(t *testing.T) {
	if _, err := Open(context.Background(), filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing state directory unexpectedly opened")
	}
}

func TestOpenRejectsUnorderedMigrations(t *testing.T) {
	migrations := []Migration{{Version: 2}, {Version: 1}}
	if _, err := OpenWithMigrations(context.Background(), t.TempDir(), migrations); err == nil {
		t.Fatal("unordered migrations unexpectedly opened")
	}
}

func TestOpenCreatesInvitedPollWorkflowSchema(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	for _, table := range []string{"moderators", "polls", "options", "participants", "contacts", "grants", "sessions", "ballots", "count_snapshots", "count_decisions", "count_results", "work_items", "deliveries", "delivery_recipients", "link_requests"} {
		var found string
		if err := st.DB.QueryRowContext(ctx, "SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?", table).Scan(&found); err != nil {
			t.Fatalf("table %s: %v", table, err)
		}
	}

	if _, err := st.DB.ExecContext(ctx, "INSERT INTO moderators(id, normalized_email) VALUES ('m1', 'same@example.test'), ('m2', 'same@example.test')"); err == nil {
		t.Fatal("duplicate moderator email unexpectedly succeeded")
	}
	if _, err := st.DB.ExecContext(ctx, "INSERT INTO polls(id, owner_id, question, deadline, places, state, version) VALUES ('p1', 'missing', 'Question', 1, 1, 'draft', 1)"); err == nil {
		t.Fatal("poll with missing owner unexpectedly succeeded")
	}
}

func TestOpenMigratesLegacyDeliveriesIntoRecipientRows(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	legacy, err := OpenWithMigrations(ctx, directory, migrations[:2])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.DB.ExecContext(ctx, "INSERT INTO work_items(id,kind,logical_key,due_at,status) VALUES ('work','delivery','legacy-delivery',100,'pending')"); err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.DB.ExecContext(ctx, "INSERT INTO deliveries(id,work_id,recipient_email,message_kind,status,next_due) VALUES ('delivery','work','legacy@example.test','invitation','pending',100)"); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	upgraded, err := Open(ctx, directory)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	var email string
	var order, version int
	if err := upgraded.DB.QueryRowContext(ctx, "SELECT email,display_order FROM delivery_recipients WHERE delivery_id='delivery'").Scan(&email, &order); err != nil {
		t.Fatal(err)
	}
	if err := upgraded.DB.QueryRowContext(ctx, "SELECT version FROM schema_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if email != "legacy@example.test" || order != 1 || version != 3 {
		t.Fatalf("email=%q order=%d schema=%d", email, order, version)
	}
}
