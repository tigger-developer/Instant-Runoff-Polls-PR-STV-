// ABOUTME: Verifies transactional persistence for invited-poll workflow mutations.
// ABOUTME: It protects ownership, grouping, uniqueness, and optimistic versions.
package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
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

func TestOpenPollCommitsStateAndOneLogicalInvitationPerContact(t *testing.T) {
	ctx := context.Background()
	st := workflowStore(t)
	defer st.Close()
	if _, err := st.DB.ExecContext(ctx, "INSERT INTO participants(id,poll_id) VALUES ('person-1','poll-1')"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.ExecContext(ctx, "INSERT INTO options(poll_id,id,label,display_order) VALUES ('poll-1','a','A',1),('poll-1','b','B',2)"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.ExecContext(ctx, "INSERT INTO contacts(id,poll_id,participant_id,delivery_email,normalized_email) VALUES ('contact-1','poll-1','person-1','one@example.test','one@example.test'),('contact-2','poll-1','person-1','other@example.test','other@example.test')"); err != nil {
		t.Fatal(err)
	}
	invitations := []InvitationWork{{WorkID: "work-1", DeliveryID: "delivery-1", ContactID: "contact-1"}, {WorkID: "work-2", DeliveryID: "delivery-2", ContactID: "contact-2"}}
	if changed, err := st.OpenPoll(ctx, "moderator-1", "poll-1", 1, invitations, time.Unix(100, 0)); err != nil || !changed {
		t.Fatalf("open changed=%v error=%v", changed, err)
	}
	if changed, err := st.OpenPoll(ctx, "moderator-1", "poll-1", 2, invitations, time.Unix(100, 0)); err != nil || changed {
		t.Fatalf("repeat open changed=%v error=%v", changed, err)
	}
	var state string
	var workCount int
	if err := st.DB.QueryRowContext(ctx, "SELECT state FROM polls WHERE id='poll-1'").Scan(&state); err != nil {
		t.Fatal(err)
	}
	if err := st.DB.QueryRowContext(ctx, "SELECT count(*) FROM work_items WHERE poll_id='poll-1' AND kind='invitation'").Scan(&workCount); err != nil {
		t.Fatal(err)
	}
	if state != "open" || workCount != 2 {
		t.Fatalf("state=%s work=%d", state, workCount)
	}
}

func TestClosePollCommitsSnapshotAndOneCountWork(t *testing.T) {
	ctx := context.Background()
	st := workflowStore(t)
	defer st.Close()
	if _, err := st.DB.ExecContext(ctx, "UPDATE polls SET state='paused', version=4 WHERE id='poll-1'"); err != nil {
		t.Fatal(err)
	}
	snapshot := CountSnapshot{ID: "snapshot-1", SchemaVersion: 1, Rule: "irish-guided-stv-v1", InputFingerprint: "fingerprint", InputJSON: []byte(`{"ballots":[]}`)}
	if changed, err := st.ClosePoll(ctx, "moderator-1", "poll-1", 4, snapshot, "count-work-1", time.Unix(100, 0)); err != nil || !changed {
		t.Fatalf("close changed=%v error=%v", changed, err)
	}
	if changed, err := st.ClosePoll(ctx, "moderator-1", "poll-1", 5, snapshot, "count-work-1", time.Unix(101, 0)); err != nil || changed {
		t.Fatalf("repeat close changed=%v error=%v", changed, err)
	}
	var snapshots, work int
	if err := st.DB.QueryRowContext(ctx, "SELECT count(*) FROM count_snapshots WHERE poll_id='poll-1'").Scan(&snapshots); err != nil {
		t.Fatal(err)
	}
	if err := st.DB.QueryRowContext(ctx, "SELECT count(*) FROM work_items WHERE logical_key='count:poll-1'").Scan(&work); err != nil {
		t.Fatal(err)
	}
	if snapshots != 1 || work != 1 {
		t.Fatalf("snapshots=%d work=%d", snapshots, work)
	}
}

func TestConsumeModeratorGrantIsAtomicAndOneUse(t *testing.T) {
	ctx := context.Background()
	st := workflowStore(t)
	defer st.Close()
	now := time.Unix(100, 0)
	grantHash := bytes.Repeat([]byte{1}, 32)
	if _, err := st.DB.ExecContext(ctx, "INSERT INTO grants(id,purpose,principal_id,key_id,token_hash,claims_json,issued_at,expires_at) VALUES ('grant-1','moderator','moderator-1','key-1',?, '{}',0,200)", grantHash); err != nil {
		t.Fatal(err)
	}
	if err := st.ConsumeModeratorGrant(ctx, grantHash, "moderator-1", bytes.Repeat([]byte{2}, 32), bytes.Repeat([]byte{3}, 32), now.Add(12*time.Hour), now); err != nil {
		t.Fatal(err)
	}
	if err := st.ConsumeModeratorGrant(ctx, grantHash, "moderator-1", bytes.Repeat([]byte{4}, 32), bytes.Repeat([]byte{5}, 32), now.Add(12*time.Hour), now); !errors.Is(err, ErrConflict) {
		t.Fatalf("repeat grant error = %v", err)
	}
	var consumed, sessions int
	if err := st.DB.QueryRowContext(ctx, "SELECT count(*) FROM grants WHERE consumed_at=100").Scan(&consumed); err != nil {
		t.Fatal(err)
	}
	if err := st.DB.QueryRowContext(ctx, "SELECT count(*) FROM sessions WHERE purpose='moderator'").Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if consumed != 1 || sessions != 1 {
		t.Fatalf("consumed=%d sessions=%d", consumed, sessions)
	}
}

func TestCreatePreAuthSessionEnforcesCapacityAndCleansExpiry(t *testing.T) {
	ctx := context.Background()
	st := workflowStore(t)
	defer st.Close()
	now := time.Unix(100, 0)
	for index := 0; index < 1000; index++ {
		token := sha256.Sum256([]byte("token-" + fmt.Sprint(index)))
		csrf := sha256.Sum256([]byte("csrf-" + fmt.Sprint(index)))
		if err := st.CreatePreAuthSession(ctx, token[:], csrf[:], now.Add(15*time.Minute), now); err != nil {
			t.Fatalf("session %d: %v", index, err)
		}
	}
	overflow := sha256.Sum256([]byte("overflow"))
	csrf := sha256.Sum256([]byte("csrf"))
	if err := st.CreatePreAuthSession(ctx, overflow[:], csrf[:], now.Add(15*time.Minute), now); !errors.Is(err, ErrCapacity) {
		t.Fatalf("capacity error = %v", err)
	}
	replacement := sha256.Sum256([]byte("replacement"))
	if err := st.CreatePreAuthSession(ctx, replacement[:], csrf[:], now.Add(31*time.Minute), now.Add(16*time.Minute)); err != nil {
		t.Fatalf("post-expiry session: %v", err)
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
