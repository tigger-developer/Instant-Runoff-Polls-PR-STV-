// ABOUTME: Verifies persisted decision replay through the real W002 engine.
// ABOUTME: It protects one authoritative result across interrupted count work.
package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tigger-developer/Instant-Runoff-Polls-PR-STV-/internal/count"
	"github.com/tigger-developer/Instant-Runoff-Polls-PR-STV-/internal/store"
)

func TestCountHandlerPersistsLotAndAuthoritativeResult(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	input := count.Input{SchemaVersion: 1, Rule: count.RuleIrishGuidedSTV, Options: []string{"a", "b"}, Places: 1, Ballots: []count.Ballot{{ID: "b1", Preferences: []string{"a"}}, {ID: "b2", Preferences: []string{"b"}}}}
	inputJSON, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.ExecContext(ctx, "INSERT INTO moderators(id,normalized_email) VALUES ('m','m@example.test')"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.ExecContext(ctx, "INSERT INTO polls(id,owner_id,question,deadline,places,state,counting_status,version) VALUES ('p','m','Question',200,1,'closed','pending',1)"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.ExecContext(ctx, "INSERT INTO count_snapshots(id,poll_id,schema_version,rule,input_fingerprint,input_json,created_at) VALUES ('s','p',1,?,?,?,10)", count.RuleIrishGuidedSTV, "fingerprint", inputJSON); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.ExecContext(ctx, "INSERT INTO work_items(id,poll_id,kind,logical_key,due_at,status,claim_token,claim_expires_at) VALUES ('w','p','count','count:p',10,'claimed','token',200)"); err != nil {
		t.Fatal(err)
	}
	handler := NewCountHandler(st, bytes.NewReader(bytes.Repeat([]byte{0}, 128)), func() time.Time { return time.Unix(100, 0) })
	summary, err := handler(ctx, &store.ClaimedWork{ID: "w", PollID: "p", Kind: "count", ClaimToken: "token", ClaimExpiresAt: 200})
	if !errors.Is(err, ErrWorkHandled) || summary.Counted != 1 {
		t.Fatalf("summary=%#v error=%v", summary, err)
	}
	var decisions, results int
	if err := st.DB.QueryRowContext(ctx, "SELECT count(*) FROM count_decisions WHERE snapshot_id='s'").Scan(&decisions); err != nil {
		t.Fatal(err)
	}
	if err := st.DB.QueryRowContext(ctx, "SELECT count(*) FROM count_results WHERE snapshot_id='s'").Scan(&results); err != nil {
		t.Fatal(err)
	}
	if decisions != 1 || results != 1 {
		t.Fatalf("decisions=%d results=%d", decisions, results)
	}
	claimed, err := st.ClaimDueWork(ctx, "count", "replacement", time.Unix(101, 0))
	if err != nil || claimed != nil {
		t.Fatalf("replay claim=%#v error=%v", claimed, err)
	}
}

func TestCountHandlerMarksFailureWithoutRemovingSnapshot(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	input := count.Input{SchemaVersion: 1, Rule: count.RuleIrishGuidedSTV, Options: []string{"a", "b"}, Places: 1, Ballots: []count.Ballot{{ID: "b1", Preferences: []string{"a"}}, {ID: "b2", Preferences: []string{"b"}}}}
	encoded, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		"INSERT INTO moderators(id,normalized_email) VALUES ('owner','owner@example.test')",
		"INSERT INTO polls(id,owner_id,question,deadline,places,state,version) VALUES ('p','owner','Question',50,1,'closed',1)",
		"INSERT INTO count_snapshots(id,poll_id,schema_version,rule,input_fingerprint,input_json,created_at) VALUES ('s','p',1,'irish-guided-stv-v1','fingerprint',?,10)",
		"INSERT INTO work_items(id,poll_id,kind,logical_key,due_at,status,claim_token,claim_expires_at) VALUES ('w','p','count','count:p',10,'claimed','token',200)",
	} {
		var execErr error
		if strings.Contains(statement, "count_snapshots") {
			_, execErr = st.DB.ExecContext(ctx, statement, encoded)
		} else {
			_, execErr = st.DB.ExecContext(ctx, statement)
		}
		if execErr != nil {
			t.Fatal(execErr)
		}
	}
	handler := NewCountHandler(st, bytes.NewReader(nil), func() time.Time { return time.Unix(100, 0) })
	if _, err := handler(ctx, &store.ClaimedWork{ID: "w", PollID: "p", Kind: "count", ClaimToken: "token"}); err == nil {
		t.Fatal("randomness failure unexpectedly succeeded")
	}
	var status string
	var snapshots int
	if err := st.DB.QueryRowContext(ctx, "SELECT counting_status FROM polls WHERE id='p'").Scan(&status); err != nil {
		t.Fatal(err)
	}
	if err := st.DB.QueryRowContext(ctx, "SELECT count(*) FROM count_snapshots WHERE poll_id='p'").Scan(&snapshots); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || snapshots != 1 {
		t.Fatalf("status=%s snapshots=%d", status, snapshots)
	}
}
