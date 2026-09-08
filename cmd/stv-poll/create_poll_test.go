// ABOUTME: Verifies voter-first poll creation through the emergency CLI boundary.
// ABOUTME: It protects grouped entitlements and one multi-recipient invitation per user.
package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/tigger-developer/Instant-Runoff-Polls-PR-STV-/internal/config"
	"github.com/tigger-developer/Instant-Runoff-Polls-PR-STV-/internal/store"
)

func TestCreatePollDefinitionOpensGroupedPollAndQueuesEveryAddress(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SyncModerators(ctx, []store.ConfiguredModerator{{ID: "owner", NormalizedEmail: "owner@example.test"}}); err != nil {
		t.Fatal(err)
	}
	definition := strings.NewReader(`id: first-poll
owner_id: owner
question: Where shall we eat?
options:
  - Cafe
  - Pizza
  - Sushi
places: 1
deadline: 2030-01-01T18:00:00Z
announce: false
participants:
  - name: Alex
    emails:
      - one@example.test
      - alias@example.test
  - name: Sam
    emails:
      - two@example.test
`)
	created, err := createPollFromDefinition(ctx, st, config.Config{BaseURL: "https://poll.example"}, definition, deterministicIDMaterial(), time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	if created.PollURL != "https://poll.example/polls/first-poll" || created.Invitations != 2 {
		t.Fatalf("created=%#v", created)
	}
	var state string
	var participants, contacts, invitations, recipients int
	var firstName string
	if err := st.DB.QueryRowContext(ctx, "SELECT state FROM polls WHERE id='first-poll'").Scan(&state); err != nil {
		t.Fatal(err)
	}
	if err := st.DB.QueryRowContext(ctx, "SELECT count(*) FROM participants WHERE poll_id='first-poll'").Scan(&participants); err != nil {
		t.Fatal(err)
	}
	if err := st.DB.QueryRowContext(ctx, "SELECT display_name FROM participants WHERE poll_id='first-poll' ORDER BY display_name LIMIT 1").Scan(&firstName); err != nil {
		t.Fatal(err)
	}
	if err := st.DB.QueryRowContext(ctx, "SELECT count(*) FROM contacts WHERE poll_id='first-poll'").Scan(&contacts); err != nil {
		t.Fatal(err)
	}
	if err := st.DB.QueryRowContext(ctx, "SELECT count(*) FROM deliveries WHERE work_id IN (SELECT id FROM work_items WHERE poll_id='first-poll') AND message_kind='invitation'").Scan(&invitations); err != nil {
		t.Fatal(err)
	}
	if err := st.DB.QueryRowContext(ctx, "SELECT count(*) FROM delivery_recipients WHERE delivery_id IN (SELECT id FROM deliveries WHERE message_kind='invitation')").Scan(&recipients); err != nil {
		t.Fatal(err)
	}
	if state != "open" || participants != 2 || firstName != "Alex" || contacts != 3 || invitations != 2 || recipients != 3 {
		t.Fatalf("state=%s participants=%d first name=%s contacts=%d invitations=%d recipients=%d", state, participants, firstName, contacts, invitations, recipients)
	}
}

func deterministicIDMaterial() *bytes.Reader {
	material := make([]byte, 0, 512)
	for value := byte(1); value <= 32; value++ {
		material = append(material, bytes.Repeat([]byte{value}, 16)...)
	}
	return bytes.NewReader(material)
}

func TestCreatePollDefinitionRejectsInvalidOrAmbiguousInputWithoutPoll(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SyncModerators(ctx, []store.ConfiguredModerator{{ID: "owner", NormalizedEmail: "owner@example.test"}}); err != nil {
		t.Fatal(err)
	}
	definition := strings.NewReader("id: bad\nowner_id: owner\nquestion: Question\noptions: [A, A]\nplaces: 1\ndeadline: 2030-01-01T18:00:00Z\nparticipants:\n  - name: One\n    emails: [same@example.test]\n  - name: Two\n    emails: [SAME@example.test]\n")
	if _, err := createPollFromDefinition(ctx, st, config.Config{BaseURL: "https://poll.example"}, definition, bytes.NewReader(make([]byte, 256)), time.Unix(100, 0)); err == nil {
		t.Fatal("invalid definition unexpectedly created a poll")
	}
	var polls int
	if err := st.DB.QueryRowContext(ctx, "SELECT count(*) FROM polls").Scan(&polls); err != nil || polls != 0 {
		t.Fatalf("polls=%d error=%v", polls, err)
	}
}
