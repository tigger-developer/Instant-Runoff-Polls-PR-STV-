// ABOUTME: Verifies persisted decision replay through the real W002 engine.
// ABOUTME: It protects one authoritative result across interrupted count work.
package workflow

import (
	"bytes"
	"context"
	"encoding/json"
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
	if err != nil || summary.Counted != 1 {
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
	summary, err = handler(ctx, &store.ClaimedWork{ID: "w", PollID: "p", Kind: "count", ClaimToken: "token", ClaimExpiresAt: 200})
	if err != nil || summary.Counted != 0 {
		t.Fatalf("replay summary=%#v error=%v", summary, err)
	}
}
