// ABOUTME: Verifies scheduled closure freezes count input or records no votes.
// ABOUTME: It exercises the real SQLite transaction and worker claim boundary.
package workflow

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/tigger-developer/Instant-Runoff-Polls-PR-STV-/internal/store"
)

func TestCloseHandlerCommitsSnapshotFromEffectiveBallots(t *testing.T) {
	st := closeHandlerStore(t, true)
	defer st.Close()
	handler := NewCloseHandler(st, bytes.NewReader(bytes.Repeat([]byte{0}, 32)), func() time.Time { return time.Unix(100, 0) })
	summary, err := handler(context.Background(), &store.ClaimedWork{ID: "close-work", PollID: "poll", Kind: "close", ClaimToken: "token"})
	if err != nil || summary.Closed != 1 || summary.NoVotes != 0 {
		t.Fatalf("summary=%#v error=%v", summary, err)
	}
	var state, countingStatus string
	var snapshots, countWork int
	if err := st.DB.QueryRow("SELECT state,counting_status FROM polls WHERE id='poll'").Scan(&state, &countingStatus); err != nil {
		t.Fatal(err)
	}
	if err := st.DB.QueryRow("SELECT count(*) FROM count_snapshots WHERE poll_id='poll'").Scan(&snapshots); err != nil {
		t.Fatal(err)
	}
	if err := st.DB.QueryRow("SELECT count(*) FROM work_items WHERE kind='count' AND poll_id='poll'").Scan(&countWork); err != nil {
		t.Fatal(err)
	}
	if state != "closed" || countingStatus != "pending" || snapshots != 1 || countWork != 1 {
		t.Fatalf("state=%s counting=%s snapshots=%d count-work=%d", state, countingStatus, snapshots, countWork)
	}
}

func TestCloseHandlerCommitsZeroTurnoutWithoutCountWork(t *testing.T) {
	st := closeHandlerStore(t, false)
	defer st.Close()
	handler := NewCloseHandler(st, bytes.NewReader(make([]byte, 32)), func() time.Time { return time.Unix(100, 0) })
	summary, err := handler(context.Background(), &store.ClaimedWork{ID: "close-work", PollID: "poll", Kind: "close", ClaimToken: "token"})
	if err != nil || summary != (Summary{Closed: 1, NoVotes: 1}) {
		t.Fatalf("summary=%#v error=%v", summary, err)
	}
	var status string
	var countWork int
	if err := st.DB.QueryRow("SELECT counting_status FROM polls WHERE id='poll'").Scan(&status); err != nil {
		t.Fatal(err)
	}
	if err := st.DB.QueryRow("SELECT count(*) FROM work_items WHERE kind='count'").Scan(&countWork); err != nil {
		t.Fatal(err)
	}
	if status != "no_votes" || countWork != 0 {
		t.Fatalf("status=%s count-work=%d", status, countWork)
	}
}

func closeHandlerStore(t *testing.T, withBallot bool) *store.Store {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	statements := []string{
		"INSERT INTO moderators(id,normalized_email) VALUES ('owner','owner@example.test')",
		"INSERT INTO polls(id,owner_id,question,deadline,places,state,version) VALUES ('poll','owner','Question',100,1,'open',2)",
		"INSERT INTO options(poll_id,id,label,display_order) VALUES ('poll','a','A',1),('poll','b','B',2)",
		"INSERT INTO participants(id,poll_id) VALUES ('person','poll')",
		"INSERT INTO work_items(id,poll_id,kind,logical_key,due_at,status,claim_token,claim_expires_at) VALUES ('close-work','poll','close','close:poll',100,'claimed','token',300)",
	}
	if withBallot {
		statements = append(statements, `INSERT INTO ballots(poll_id,participant_id,version,preferences_json,accepted_at) VALUES ('poll','person',1,'["a","b"]',90)`)
	}
	for _, statement := range statements {
		if _, err := st.DB.ExecContext(ctx, statement); err != nil {
			st.Close()
			t.Fatal(err)
		}
	}
	return st
}
