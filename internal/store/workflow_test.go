// ABOUTME: Verifies transactional persistence for invited-poll workflow mutations.
// ABOUTME: It protects ownership, grouping, uniqueness, and optimistic versions.
package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestReplaceElectorateCommitsGroupedContactsAndVersion(t *testing.T) {
	ctx := context.Background()
	st := workflowStore(t)
	defer st.Close()
	participants := []ElectorateParticipant{
		{ID: "person-1", Contacts: []Contact{{ID: "contact-1", DeliveryEmail: "One@example.test", NormalizedEmail: "one@example.test"}, {ID: "contact-2", DeliveryEmail: "other@example.test", NormalizedEmail: "other@example.test"}}},
		{ID: "person-2", Contacts: []Contact{{ID: "contact-3", DeliveryEmail: "two@example.test", NormalizedEmail: "two@example.test"}}},
	}
	if err := st.ReplaceElectorate(ctx, "moderator-1", "poll-1", 1, participants); err != nil {
		t.Fatal(err)
	}
	var participantCount, contactCount, version int
	if err := st.DB.QueryRowContext(ctx, "SELECT count(*) FROM participants WHERE poll_id = 'poll-1'").Scan(&participantCount); err != nil {
		t.Fatal(err)
	}
	if err := st.DB.QueryRowContext(ctx, "SELECT count(*) FROM contacts WHERE poll_id = 'poll-1'").Scan(&contactCount); err != nil {
		t.Fatal(err)
	}
	if err := st.DB.QueryRowContext(ctx, "SELECT version FROM polls WHERE id = 'poll-1'").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if participantCount != 2 || contactCount != 3 || version != 2 {
		t.Fatalf("counts = participants %d, contacts %d, version %d", participantCount, contactCount, version)
	}
}

func TestReplaceBallotEnforcesDeadlineAndIndependentVersion(t *testing.T) {
	ctx := context.Background()
	st := workflowStore(t)
	defer st.Close()
	if _, err := st.DB.ExecContext(ctx, "INSERT INTO participants(id, poll_id) VALUES ('person-1','poll-1')"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.ExecContext(ctx, "UPDATE polls SET state = 'open', deadline = 200 WHERE id = 'poll-1'"); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceBallot(ctx, "poll-1", "person-1", 0, []byte(`["a"]`), time.Unix(199, 0)); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceBallot(ctx, "poll-1", "person-1", 0, []byte(`["b"]`), time.Unix(199, 0)); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale ballot error = %v", err)
	}
	if err := st.ReplaceBallot(ctx, "poll-1", "person-1", 1, []byte(`["b"]`), time.Unix(200, 0)); !errors.Is(err, ErrConflict) {
		t.Fatalf("deadline ballot error = %v", err)
	}
	var version int
	var preferences string
	if err := st.DB.QueryRowContext(ctx, "SELECT version, preferences_json FROM ballots WHERE poll_id = 'poll-1' AND participant_id = 'person-1'").Scan(&version, &preferences); err != nil {
		t.Fatal(err)
	}
	if version != 1 || preferences != `["a"]` {
		t.Fatalf("ballot version=%d preferences=%s", version, preferences)
	}
}

func TestReplaceElectorateRollsBackDuplicateAndRejectsOwnerOrVersion(t *testing.T) {
	ctx := context.Background()
	st := workflowStore(t)
	defer st.Close()
	duplicate := []ElectorateParticipant{
		{ID: "person-1", Contacts: []Contact{{ID: "contact-1", DeliveryEmail: "same@example.test", NormalizedEmail: "same@example.test"}}},
		{ID: "person-2", Contacts: []Contact{{ID: "contact-2", DeliveryEmail: "SAME@example.test", NormalizedEmail: "same@example.test"}}},
	}
	if err := st.ReplaceElectorate(ctx, "moderator-1", "poll-1", 1, duplicate); err == nil {
		t.Fatal("duplicate contact unexpectedly committed")
	}
	for _, tc := range []struct {
		owner   string
		version int
	}{
		{owner: "moderator-2", version: 1},
		{owner: "moderator-1", version: 9},
	} {
		if err := st.ReplaceElectorate(ctx, tc.owner, "poll-1", tc.version, []ElectorateParticipant{{ID: "p", Contacts: []Contact{{ID: "c", DeliveryEmail: "p@example.test", NormalizedEmail: "p@example.test"}}}}); !errors.Is(err, ErrConflict) {
			t.Fatalf("owner %s version %d error = %v", tc.owner, tc.version, err)
		}
	}
	var count int
	if err := st.DB.QueryRowContext(ctx, "SELECT count(*) FROM participants WHERE poll_id = 'poll-1'").Scan(&count); err != nil || count != 0 {
		t.Fatalf("rollback participant count = %d, %v", count, err)
	}
}

func workflowStore(t *testing.T) *Store {
	t.Helper()
	ctx := context.Background()
	st, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.ExecContext(ctx, "INSERT INTO moderators(id, normalized_email) VALUES ('moderator-1','one@example.test'),('moderator-2','two@example.test')"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.ExecContext(ctx, "INSERT INTO polls(id, owner_id, question, deadline, places, state, version) VALUES ('poll-1','moderator-1','Question',9999999999,1,'draft',1)"); err != nil {
		t.Fatal(err)
	}
	return st
}
