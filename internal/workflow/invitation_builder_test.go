// ABOUTME: Verifies invitation links use recoverable grants and configured URL.
// ABOUTME: It ensures retries reuse claims without persisting a raw bearer token.
package workflow

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/tigger-developer/Instant-Runoff-Polls-PR-STV-/internal/store"
)

func TestInvitationBuilderPersistsClaimsAndReusesExactBearer(t *testing.T) {
	st := deliveryHandlerStore(t)
	defer st.Close()
	key := bytes.Repeat([]byte{7}, 32)
	randomness := bytes.NewReader(bytes.Repeat([]byte{9}, 128))
	builder := NewInvitationMessageBuilder(st, "https://poll.example/", "key-1", key, randomness, func() time.Time { return time.Unix(100, 0) })
	attempt := store.DeliveryAttempt{PollID: "poll", ContactID: "contact", ParticipantID: "person", RecipientEmail: "reader@example.test", MessageKind: "invitation", Question: "Question"}
	item := &store.ClaimedWork{ID: "mail", ClaimToken: "token"}
	first, err := builder(context.Background(), item, attempt)
	if err != nil {
		t.Fatal(err)
	}
	second, err := builder(context.Background(), item, attempt)
	if err != nil {
		t.Fatal(err)
	}
	if first.Body != second.Body || !strings.Contains(first.Body, "https://poll.example/auth/verify?grant=") {
		t.Fatalf("messages did not reuse configured bearer link\nfirst=%s\nsecond=%s", first.Body, second.Body)
	}
	var count int
	var storedPayload []byte
	if err := st.DB.QueryRow("SELECT count(*),max(claims_json) FROM grants").Scan(&count, &storedPayload); err != nil {
		t.Fatal(err)
	}
	link := strings.TrimSpace(strings.Split(first.Body, "\n\n")[1])
	if count != 1 || bytes.Contains(storedPayload, []byte(link)) {
		t.Fatalf("grant count=%d raw bearer persisted=%v", count, bytes.Contains(storedPayload, []byte(link)))
	}
}

func TestInvitationBuilderRoutesAnnouncementWithoutGrant(t *testing.T) {
	builder := NewInvitationMessageBuilder(nil, "", "", nil, nil, nil)
	message, err := builder(context.Background(), &store.ClaimedWork{}, store.DeliveryAttempt{RecipientEmail: "reader@example.test", MessageKind: "announcement", Question: "Question", Winners: []string{"Alice"}})
	if err != nil || !strings.Contains(message.Body, "Alice") || message.Subject != "Result: Question" {
		t.Fatalf("message=%#v error=%v", message, err)
	}
}
