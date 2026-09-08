// ABOUTME: Exercises one durable close-count-announcement sweep through real SQLite.
// ABOUTME: It protects the signed worker summary and restart-safe operation ordering.
package workflow

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/tigger-developer/Instant-Runoff-Polls-PR-STV-/internal/store"
)

func TestProcessDueWorkClosesCountsAndAnnouncesOnePoll(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for _, statement := range []string{
		"INSERT INTO moderators(id,normalized_email) VALUES ('owner','owner@example.test')",
		"INSERT INTO polls(id,owner_id,question,deadline,places,announce,state,version) VALUES ('poll','owner','Question',100,1,1,'open',2)",
		"INSERT INTO options(poll_id,id,label,display_order) VALUES ('poll','a','Alice',1),('poll','b','Bob',2)",
		"INSERT INTO participants(id,poll_id) VALUES ('one','poll'),('two','poll')",
		"INSERT INTO contacts(id,poll_id,participant_id,delivery_email,normalized_email) VALUES ('contact-one','poll','one','one@example.test','one@example.test'),('contact-two','poll','two','two@example.test','two@example.test')",
		`INSERT INTO ballots(poll_id,participant_id,version,preferences_json,accepted_at) VALUES ('poll','one',1,'["a","b"]',90),('poll','two',1,'["a","b"]',91)`,
		"INSERT INTO work_items(id,poll_id,kind,logical_key,due_at,status) VALUES ('close','poll','close','close:poll',100,'pending')",
	} {
		if _, err := st.DB.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	now := func() time.Time { return time.Unix(100, 0) }
	randomness := bytes.NewReader(bytes.Repeat([]byte{0}, 512))
	sent := 0
	sender := senderFunc(func(_ context.Context, message Message) error {
		sent++
		if message.Subject != "Result: Question" {
			return fmt.Errorf("unexpected subject %q", message.Subject)
		}
		return nil
	})
	builder := NewInvitationMessageBuilder(st, "https://poll.example", "key", bytes.Repeat([]byte{7}, 32), map[string]string{}, randomness, now)
	handlers := map[string]WorkHandler{
		"close":    NewCloseHandler(st, randomness, now),
		"count":    NewCountHandler(st, randomness, now),
		"delivery": NewDeliveryHandler(st, sender, builder, func() (float64, error) { return 0, nil }, now),
	}
	nextToken := 0
	summary, err := ProcessDueWork(ctx, st, handlers, func() string {
		nextToken++
		return fmt.Sprintf("claim-%d", nextToken)
	}, now)
	want := Summary{Version: 1, Closed: 1, Counted: 1, SMTPAccepted: 2}
	if err != nil || summary != want || sent != 2 {
		t.Fatalf("summary=%#v sent=%d error=%v", summary, sent, err)
	}
	var resultCount, pendingWork int
	if err := st.DB.QueryRowContext(ctx, "SELECT count(*) FROM count_results").Scan(&resultCount); err != nil {
		t.Fatal(err)
	}
	if err := st.DB.QueryRowContext(ctx, "SELECT count(*) FROM work_items WHERE status IN ('pending','claimed')").Scan(&pendingWork); err != nil {
		t.Fatal(err)
	}
	if resultCount != 1 || pendingWork != 0 {
		t.Fatalf("results=%d pending=%d", resultCount, pendingWork)
	}
}
