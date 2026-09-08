// ABOUTME: Verifies SMTP acceptance, retry, and terminal delivery outcomes.
// ABOUTME: It checks that each attempt is persisted through the real store.
package workflow

import (
	"context"
	"errors"
	"net"
	"net/textproto"
	"testing"
	"time"

	"github.com/tigger-developer/Instant-Runoff-Polls-PR-STV-/internal/store"
)

type senderFunc func(context.Context, Message) error

func (send senderFunc) Send(ctx context.Context, message Message) error { return send(ctx, message) }

func TestDeliveryHandlerPersistsAcceptedTemporaryAndPermanentOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name        string
		sendErr     error
		wantStatus  string
		wantRetry   int
		wantAccept  int
		wantError   bool
		wantHandled bool
	}{
		{name: "accepted", wantStatus: "smtp_accepted", wantAccept: 1, wantError: true, wantHandled: true},
		{name: "temporary", sendErr: &net.DNSError{IsTimeout: true}, wantStatus: "retrying", wantRetry: 1, wantError: true},
		{name: "permanent", sendErr: &textproto.Error{Code: 550, Msg: "rejected"}, wantStatus: "failed", wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := deliveryHandlerStore(t)
			defer st.Close()
			handler := NewDeliveryHandler(st, senderFunc(func(context.Context, Message) error { return tc.sendErr }), func(_ context.Context, _ *store.ClaimedWork, attempt store.DeliveryAttempt) (Message, error) {
				return InvitationMessage(attempt.RecipientEmails, attempt.Question, "https://poll.example/vote/token")
			}, func() (float64, error) { return 0, nil }, func() time.Time { return time.Unix(100, 0) })
			summary, err := handler(context.Background(), &store.ClaimedWork{ID: "mail", Kind: "delivery", ClaimToken: "token"})
			if (err != nil) != tc.wantError || summary.Retrying != tc.wantRetry || summary.SMTPAccepted != tc.wantAccept {
				t.Fatalf("summary=%#v error=%v", summary, err)
			}
			if tc.wantHandled && !errors.Is(err, ErrWorkHandled) {
				t.Fatalf("error=%v", err)
			}
			var status, workStatus string
			if err := st.DB.QueryRow("SELECT status FROM deliveries WHERE work_id='mail'").Scan(&status); err != nil {
				t.Fatal(err)
			}
			if err := st.DB.QueryRow("SELECT status FROM work_items WHERE id='mail'").Scan(&workStatus); err != nil {
				t.Fatal(err)
			}
			if status != tc.wantStatus {
				t.Fatalf("status=%s", status)
			}
			if tc.name == "accepted" && workStatus != "succeeded" {
				t.Fatalf("accepted work status=%s", workStatus)
			}
		})
	}
}

func TestDeliveryHandlerSixthTemporaryFailureIsTerminal(t *testing.T) {
	st := deliveryHandlerStore(t)
	defer st.Close()
	if _, err := st.DB.Exec("UPDATE work_items SET attempts=5 WHERE id='mail'"); err != nil {
		t.Fatal(err)
	}
	handler := NewDeliveryHandler(st, senderFunc(func(context.Context, Message) error { return &net.DNSError{IsTimeout: true} }), func(context.Context, *store.ClaimedWork, store.DeliveryAttempt) (Message, error) {
		return Message{To: []string{"reader@example.test"}, Subject: "Vote", Body: "Body"}, nil
	}, func() (float64, error) { return 0, nil }, func() time.Time { return time.Unix(100, 0) })
	_, err := handler(context.Background(), &store.ClaimedWork{ID: "mail", Kind: "delivery", ClaimToken: "token"})
	if !errors.Is(err, ErrWorkFinalized) {
		t.Fatalf("error=%v", err)
	}
	var attempts int
	var status string
	if err := st.DB.QueryRow("SELECT attempts,status FROM work_items WHERE id='mail'").Scan(&attempts, &status); err != nil {
		t.Fatal(err)
	}
	if attempts != 6 || status != "failed" {
		t.Fatalf("attempts=%d status=%s", attempts, status)
	}
}

func TestDeliveryHandlerFinalizesStateWhenRetrySchedulingFails(t *testing.T) {
	st := deliveryHandlerStore(t)
	defer st.Close()
	handler := NewDeliveryHandler(st, senderFunc(func(context.Context, Message) error {
		return &net.DNSError{IsTimeout: true}
	}), func(context.Context, *store.ClaimedWork, store.DeliveryAttempt) (Message, error) {
		return Message{To: []string{"reader@example.test"}, Subject: "Vote", Body: "Body"}, nil
	}, func() (float64, error) { return 0, errors.New("randomness unavailable") }, func() time.Time { return time.Unix(100, 0) })
	_, err := handler(context.Background(), &store.ClaimedWork{ID: "mail", Kind: "delivery", ClaimToken: "token"})
	if !errors.Is(err, ErrWorkFinalized) {
		t.Fatalf("error=%v", err)
	}
	var deliveryStatus, workStatus, failureClass string
	if err := st.DB.QueryRow("SELECT status,smtp_outcome FROM deliveries WHERE work_id='mail'").Scan(&deliveryStatus, &failureClass); err != nil {
		t.Fatal(err)
	}
	if err := st.DB.QueryRow("SELECT status FROM work_items WHERE id='mail'").Scan(&workStatus); err != nil {
		t.Fatal(err)
	}
	if deliveryStatus != "failed" || workStatus != "failed" || failureClass != "delivery retry scheduling failure" {
		t.Fatalf("delivery=%s work=%s failure=%s", deliveryStatus, workStatus, failureClass)
	}
}

func deliveryHandlerStore(t *testing.T) *store.Store {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		"INSERT INTO moderators(id,normalized_email) VALUES ('owner','owner@example.test')",
		"INSERT INTO polls(id,owner_id,question,deadline,places,state,version) VALUES ('poll','owner','Question',500,1,'open',2)",
		"INSERT INTO participants(id,poll_id) VALUES ('person','poll')",
		"INSERT INTO contacts(id,poll_id,participant_id,delivery_email,normalized_email) VALUES ('contact','poll','person','reader@example.test','reader@example.test')",
		"INSERT INTO work_items(id,poll_id,kind,logical_key,due_at,status,claim_token,claim_expires_at) VALUES ('mail','poll','delivery','invitation:poll:contact',10,'claimed','token',300)",
		"INSERT INTO deliveries(id,work_id,contact_id,recipient_email,message_kind,status,next_due) VALUES ('delivery','mail','contact','reader@example.test','invitation','pending',10)",
	} {
		if _, err := st.DB.ExecContext(ctx, statement); err != nil {
			st.Close()
			t.Fatal(err)
		}
	}
	return st
}
