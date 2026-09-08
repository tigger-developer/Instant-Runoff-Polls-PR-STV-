// ABOUTME: Verifies transactional persistence for invited-poll workflow mutations.
// ABOUTME: It protects ownership, grouping, uniqueness, and optimistic versions.
package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sync"
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
	if err := st.ReplaceBallot(ctx, "poll-1", "person-1", 0, []byte(`["a"]`), func() time.Time { return time.Unix(199, 0) }); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceBallot(ctx, "poll-1", "person-1", 0, []byte(`["b"]`), func() time.Time { return time.Unix(199, 0) }); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale ballot error = %v", err)
	}
	if err := st.ReplaceBallot(ctx, "poll-1", "person-1", 1, []byte(`["b"]`), func() time.Time { return time.Unix(200, 0) }); !errors.Is(err, ErrConflict) {
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

func TestClearBallotRequiresCurrentVersionAndOpenPoll(t *testing.T) {
	ctx := context.Background()
	st := workflowStore(t)
	defer st.Close()
	if _, err := st.DB.ExecContext(ctx, "INSERT INTO participants(id, poll_id) VALUES ('person-1','poll-1')"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.ExecContext(ctx, "UPDATE polls SET state='open', deadline=200 WHERE id='poll-1'"); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceBallot(ctx, "poll-1", "person-1", 0, []byte(`["a"]`), func() time.Time { return time.Unix(100, 0) }); err != nil {
		t.Fatal(err)
	}
	if err := st.ClearBallot(ctx, "poll-1", "person-1", 0, func() time.Time { return time.Unix(101, 0) }); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale clear error=%v", err)
	}
	if err := st.ClearBallot(ctx, "poll-1", "person-1", 1, func() time.Time { return time.Unix(101, 0) }); err != nil {
		t.Fatal(err)
	}
	var ballots int
	if err := st.DB.QueryRowContext(ctx, "SELECT count(*) FROM ballots WHERE poll_id='poll-1' AND participant_id='person-1'").Scan(&ballots); err != nil || ballots != 0 {
		t.Fatalf("ballots=%d error=%v", ballots, err)
	}
}

func TestReplaceBallotSamplesAcceptanceTimeInsideWriteBoundary(t *testing.T) {
	ctx := context.Background()
	st := workflowStore(t)
	defer st.Close()
	if _, err := st.DB.ExecContext(ctx, "INSERT INTO participants(id, poll_id) VALUES ('person-1','poll-1')"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.ExecContext(ctx, "UPDATE polls SET state='open', deadline=200 WHERE id='poll-1'"); err != nil {
		t.Fatal(err)
	}
	clockCalls := 0
	err := st.ReplaceBallot(ctx, "poll-1", "person-1", 0, []byte(`["a"]`), func() time.Time {
		clockCalls++
		return time.Unix(200, 0)
	})
	if !errors.Is(err, ErrConflict) || clockCalls != 1 {
		t.Fatalf("error=%v clock calls=%d", err, clockCalls)
	}
	var ballots int
	if err := st.DB.QueryRowContext(ctx, "SELECT count(*) FROM ballots WHERE poll_id='poll-1'").Scan(&ballots); err != nil || ballots != 0 {
		t.Fatalf("ballots=%d error=%v", ballots, err)
	}
}

func TestOpenPollCommitsOneMultiRecipientInvitationPerParticipant(t *testing.T) {
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
	invitations := []InvitationWork{{WorkID: "work-1", DeliveryID: "delivery-1", ParticipantID: "person-1", RecipientEmails: []string{"one@example.test", "other@example.test"}}}
	if changed, err := st.OpenPoll(ctx, "moderator-1", "poll-1", 1, "close-work", invitations, time.Unix(100, 0)); err != nil || !changed {
		t.Fatalf("open changed=%v error=%v", changed, err)
	}
	if changed, err := st.OpenPoll(ctx, "moderator-1", "poll-1", 2, "close-work", invitations, time.Unix(100, 0)); err != nil || changed {
		t.Fatalf("repeat open changed=%v error=%v", changed, err)
	}
	var state string
	var workCount, deliveryCount int
	if err := st.DB.QueryRowContext(ctx, "SELECT state FROM polls WHERE id='poll-1'").Scan(&state); err != nil {
		t.Fatal(err)
	}
	if err := st.DB.QueryRowContext(ctx, "SELECT count(*) FROM work_items WHERE poll_id='poll-1' AND kind='delivery'").Scan(&workCount); err != nil {
		t.Fatal(err)
	}
	if err := st.DB.QueryRowContext(ctx, "SELECT count(*) FROM deliveries WHERE work_id='work-1'").Scan(&deliveryCount); err != nil {
		t.Fatal(err)
	}
	var recipientCount int
	if err := st.DB.QueryRowContext(ctx, "SELECT count(*) FROM delivery_recipients WHERE delivery_id='delivery-1'").Scan(&recipientCount); err != nil {
		t.Fatal(err)
	}
	if state != "open" || workCount != 1 || deliveryCount != 1 || recipientCount != 2 {
		t.Fatalf("state=%s work=%d deliveries=%d recipients=%d", state, workCount, deliveryCount, recipientCount)
	}
	var firstRecipient, secondRecipient, logicalKey string
	if err := st.DB.QueryRowContext(ctx, "SELECT email FROM delivery_recipients WHERE delivery_id='delivery-1' AND display_order=1").Scan(&firstRecipient); err != nil {
		t.Fatal(err)
	}
	if err := st.DB.QueryRowContext(ctx, "SELECT email FROM delivery_recipients WHERE delivery_id='delivery-1' AND display_order=2").Scan(&secondRecipient); err != nil {
		t.Fatal(err)
	}
	if err := st.DB.QueryRowContext(ctx, "SELECT logical_key FROM work_items WHERE id='work-1'").Scan(&logicalKey); err != nil {
		t.Fatal(err)
	}
	if firstRecipient != "one@example.test" || secondRecipient != "other@example.test" || logicalKey != "invitation:poll-1:person-1" {
		t.Fatalf("recipients=%q,%q logical key=%q", firstRecipient, secondRecipient, logicalKey)
	}
	var closeDue int64
	if err := st.DB.QueryRowContext(ctx, "SELECT due_at FROM work_items WHERE id='close-work' AND kind='close' AND status='pending'").Scan(&closeDue); err != nil || closeDue != 9999999999 {
		t.Fatalf("close due=%d error=%v", closeDue, err)
	}
}

func TestClosePollCommitsSnapshotAndOneCountWork(t *testing.T) {
	ctx := context.Background()
	st := workflowStore(t)
	defer st.Close()
	if _, err := st.DB.ExecContext(ctx, "UPDATE polls SET state='paused', version=4 WHERE id='poll-1'"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.ExecContext(ctx, "INSERT INTO participants(id,poll_id) VALUES ('person','poll-1')"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.ExecContext(ctx, `INSERT INTO ballots(poll_id,participant_id,version,preferences_json,accepted_at) VALUES ('poll-1','person',1,'["a"]',90)`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.ExecContext(ctx, "INSERT INTO work_items(id,poll_id,kind,logical_key,due_at,status) VALUES ('scheduled-close','poll-1','close','close:poll-1',999,'pending')"); err != nil {
		t.Fatal(err)
	}
	build := func(CloseWork) (CountSnapshot, error) {
		return CountSnapshot{ID: "snapshot-1", SchemaVersion: 1, Rule: "irish-guided-stv-v1", InputFingerprint: "fingerprint", InputJSON: []byte(`{"ballots":[]}`)}, nil
	}
	if changed, _, err := st.ClosePoll(ctx, "moderator-1", "poll-1", 4, build, "count-work-1", time.Unix(100, 0)); err != nil || !changed {
		t.Fatalf("close changed=%v error=%v", changed, err)
	}
	if changed, _, err := st.ClosePoll(ctx, "moderator-1", "poll-1", 5, build, "count-work-1", time.Unix(101, 0)); err != nil || changed {
		t.Fatalf("repeat close changed=%v error=%v", changed, err)
	}
	var snapshots, work, staleClose int
	if err := st.DB.QueryRowContext(ctx, "SELECT count(*) FROM count_snapshots WHERE poll_id='poll-1'").Scan(&snapshots); err != nil {
		t.Fatal(err)
	}
	if err := st.DB.QueryRowContext(ctx, "SELECT count(*) FROM work_items WHERE logical_key='count:poll-1'").Scan(&work); err != nil {
		t.Fatal(err)
	}
	if err := st.DB.QueryRowContext(ctx, "SELECT count(*) FROM work_items WHERE id='scheduled-close' AND status='succeeded'").Scan(&staleClose); err != nil {
		t.Fatal(err)
	}
	if snapshots != 1 || work != 1 || staleClose != 1 {
		t.Fatalf("snapshots=%d work=%d stale-close=%d", snapshots, work, staleClose)
	}
}

func TestClosePollBuildsSnapshotFromBallotsInsideWriteBoundary(t *testing.T) {
	ctx := context.Background()
	st := workflowStore(t)
	defer st.Close()
	for _, statement := range []string{
		"UPDATE polls SET state='paused',version=3 WHERE id='poll-1'",
		"INSERT INTO options(poll_id,id,label,display_order) VALUES ('poll-1','a','A',1),('poll-1','b','B',2)",
		"INSERT INTO participants(id,poll_id) VALUES ('first','poll-1'),('second','poll-1')",
		`INSERT INTO ballots(poll_id,participant_id,version,preferences_json,accepted_at) VALUES ('poll-1','first',1,'["a"]',90)`,
	} {
		if _, err := st.DB.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.PollForClose(ctx, "moderator-1", "poll-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.ExecContext(ctx, `INSERT INTO ballots(poll_id,participant_id,version,preferences_json,accepted_at) VALUES ('poll-1','second',1,'["b"]',91)`); err != nil {
		t.Fatal(err)
	}
	seen := 0
	build := func(work CloseWork) (CountSnapshot, error) {
		seen = len(work.Ballots)
		return CountSnapshot{ID: "snapshot-1", SchemaVersion: 1, Rule: "irish-guided-stv-v1", InputFingerprint: "fingerprint", InputJSON: []byte(`{"ballots":[]}`)}, nil
	}
	if changed, _, err := st.ClosePoll(ctx, "moderator-1", "poll-1", 3, build, "count-work-1", time.Unix(100, 0)); err != nil || !changed || seen != 2 {
		t.Fatalf("changed=%v ballots=%d error=%v", changed, seen, err)
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
	if err := st.ConsumeModeratorGrant(ctx, grantHash, "moderator-1", bytes.Repeat([]byte{2}, 32), bytes.Repeat([]byte{3}, 32), nil, nil, now.Add(12*time.Hour), now); err != nil {
		t.Fatal(err)
	}
	if err := st.ConsumeModeratorGrant(ctx, grantHash, "moderator-1", bytes.Repeat([]byte{4}, 32), bytes.Repeat([]byte{5}, 32), nil, nil, now.Add(12*time.Hour), now); !errors.Is(err, ErrConflict) {
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

func TestConsumeModeratorGrantAllowsExactlyOneConcurrentExchange(t *testing.T) {
	ctx := context.Background()
	st := workflowStore(t)
	defer st.Close()
	now := time.Unix(100, 0)
	grantHash := bytes.Repeat([]byte{1}, 32)
	if _, err := st.DB.ExecContext(ctx, "INSERT INTO grants(id,purpose,principal_id,key_id,token_hash,claims_json,issued_at,expires_at) VALUES ('grant','moderator','moderator-1','key-1',?,'{}',0,200)", grantHash); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	var wait sync.WaitGroup
	for index := byte(2); index < 4; index++ {
		wait.Add(1)
		go func(value byte) {
			defer wait.Done()
			<-start
			results <- st.ConsumeModeratorGrant(ctx, grantHash, "moderator-1", bytes.Repeat([]byte{value}, 32), bytes.Repeat([]byte{value + 10}, 32), nil, nil, now.Add(time.Hour), now)
		}(index)
	}
	close(start)
	wait.Wait()
	close(results)
	succeeded, conflicted := 0, 0
	for err := range results {
		if err == nil {
			succeeded++
		} else if errors.Is(err, ErrConflict) {
			conflicted++
		} else {
			t.Fatalf("exchange error=%v", err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("succeeded=%d conflicted=%d", succeeded, conflicted)
	}
}

func TestBallotAndCloseRaceSerializesIntoSnapshot(t *testing.T) {
	ctx := context.Background()
	st := workflowStore(t)
	defer st.Close()
	for _, statement := range []string{
		"UPDATE polls SET state='open',deadline=200,version=1 WHERE id='poll-1'",
		"INSERT INTO options(poll_id,id,label,display_order) VALUES ('poll-1','a','A',1),('poll-1','b','B',2)",
		"INSERT INTO participants(id,poll_id) VALUES ('person','poll-1')",
	} {
		if _, err := st.DB.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	start := make(chan struct{})
	ballotResult := make(chan error, 1)
	closeResult := make(chan error, 1)
	seenBallots := 0
	go func() {
		<-start
		ballotResult <- st.ReplaceBallot(ctx, "poll-1", "person", 0, []byte(`["a"]`), func() time.Time { return time.Unix(99, 0) })
	}()
	go func() {
		<-start
		_, _, err := st.ClosePoll(ctx, "moderator-1", "poll-1", 1, func(work CloseWork) (CountSnapshot, error) {
			seenBallots = len(work.Ballots)
			return CountSnapshot{ID: "snapshot", SchemaVersion: 1, Rule: "irish-guided-stv-v1", InputFingerprint: "fingerprint", InputJSON: []byte(`{"ballots":[]}`)}, nil
		}, "count-work", time.Unix(100, 0))
		closeResult <- err
	}()
	close(start)
	ballotErr, closeErr := <-ballotResult, <-closeResult
	if closeErr != nil {
		t.Fatalf("close error=%v", closeErr)
	}
	if ballotErr == nil && seenBallots != 1 {
		t.Fatalf("accepted ballot omitted from snapshot: seen=%d", seenBallots)
	}
	if ballotErr != nil && !errors.Is(ballotErr, ErrConflict) {
		t.Fatalf("ballot error=%v", ballotErr)
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

func TestRecordLinkRequestEnforcesIdentityAndGlobalBounds(t *testing.T) {
	ctx := context.Background()
	st := workflowStore(t)
	defer st.Close()
	now := time.Unix(1_000, 0)
	identity := sha256.Sum256([]byte("known-or-unknown@example.test"))
	allowed, err := st.RecordLinkRequest(ctx, identity[:], "participant", "poll-1", now)
	if err != nil || !allowed {
		t.Fatalf("first request allowed=%v error=%v", allowed, err)
	}
	allowed, err = st.RecordLinkRequest(ctx, identity[:], "participant", "poll-1", now.Add(59*time.Second))
	if err != nil || allowed {
		t.Fatalf("minute request allowed=%v error=%v", allowed, err)
	}
	for index := 1; index < 5; index++ {
		allowed, err = st.RecordLinkRequest(ctx, identity[:], "participant", "poll-1", now.Add(time.Duration(index)*time.Minute))
		if err != nil || !allowed {
			t.Fatalf("request %d allowed=%v error=%v", index+1, allowed, err)
		}
	}
	allowed, err = st.RecordLinkRequest(ctx, identity[:], "participant", "poll-1", now.Add(14*time.Minute))
	if err != nil || allowed {
		t.Fatalf("rolling-window request allowed=%v error=%v", allowed, err)
	}

	if _, err := st.DB.ExecContext(ctx, "DELETE FROM link_requests"); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 1000; index++ {
		hash := sha256.Sum256([]byte(fmt.Sprintf("identity-%d", index)))
		allowed, err = st.RecordLinkRequest(ctx, hash[:], "moderator", "", now)
		if err != nil || !allowed {
			t.Fatalf("global request %d allowed=%v error=%v", index+1, allowed, err)
		}
	}
	overflow := sha256.Sum256([]byte("overflow-identity"))
	allowed, err = st.RecordLinkRequest(ctx, overflow[:], "moderator", "", now)
	if err != nil || allowed {
		t.Fatalf("global overflow allowed=%v error=%v", allowed, err)
	}
	allowed, err = st.RecordLinkRequest(ctx, overflow[:], "moderator", "", now.Add(16*time.Minute))
	if err != nil || !allowed {
		t.Fatalf("expired-window request allowed=%v error=%v", allowed, err)
	}
}

func TestClaimDueWorkUsesOrderAndLeaseToken(t *testing.T) {
	ctx := context.Background()
	st := workflowStore(t)
	defer st.Close()
	if _, err := st.DB.ExecContext(ctx, "INSERT INTO work_items(id,poll_id,kind,logical_key,due_at,status) VALUES ('later','poll-1','count','later',20,'pending'),('first','poll-1','count','first',10,'pending')"); err != nil {
		t.Fatal(err)
	}
	claimed, err := st.ClaimDueWork(ctx, "count", "token-one", time.Unix(30, 0))
	if err != nil {
		t.Fatal(err)
	}
	if claimed == nil || claimed.ID != "first" || claimed.ClaimToken != "token-one" || claimed.ClaimExpiresAt != 150 {
		t.Fatalf("claimed work = %#v", claimed)
	}
	if err := st.CompleteWork(ctx, "first", "wrong-token", time.Unix(31, 0)); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale completion error = %v", err)
	}
	if err := st.CompleteWork(ctx, "first", "token-one", time.Unix(31, 0)); err != nil {
		t.Fatal(err)
	}
	next, err := st.ClaimDueWork(ctx, "count", "token-two", time.Unix(30, 0))
	if err != nil || next == nil || next.ID != "later" {
		t.Fatalf("next = %#v, %v", next, err)
	}
}

func TestClaimDueWorkReclaimsExpiredLeaseAndRejectsLiveLease(t *testing.T) {
	ctx := context.Background()
	st := workflowStore(t)
	defer st.Close()
	if _, err := st.DB.ExecContext(ctx, "INSERT INTO work_items(id,poll_id,kind,logical_key,due_at,status,claim_token,claim_expires_at) VALUES ('work','poll-1','delivery','work',10,'claimed','old',100)"); err != nil {
		t.Fatal(err)
	}
	if claimed, err := st.ClaimDueWork(ctx, "delivery", "early", time.Unix(99, 0)); err != nil || claimed != nil {
		t.Fatalf("live lease claim = %#v, %v", claimed, err)
	}
	claimed, err := st.ClaimDueWork(ctx, "delivery", "replacement", time.Unix(100, 0))
	if err != nil || claimed == nil || claimed.ClaimToken != "replacement" {
		t.Fatalf("replacement claim = %#v, %v", claimed, err)
	}
	if err := st.CompleteWork(ctx, "work", "old", time.Unix(100, 0)); !errors.Is(err, ErrConflict) {
		t.Fatalf("old holder completion = %v", err)
	}
}

func TestCountEvidenceIsClaimBoundUniqueAndReplayable(t *testing.T) {
	ctx := context.Background()
	st := workflowStore(t)
	defer st.Close()
	if _, err := st.DB.ExecContext(ctx, "INSERT INTO count_snapshots(id,poll_id,schema_version,rule,input_fingerprint,input_json,created_at) VALUES ('snapshot-1','poll-1',1,'irish-guided-stv-v1','fingerprint',?,10)", []byte(`{"input":1}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.ExecContext(ctx, "INSERT INTO work_items(id,poll_id,kind,logical_key,due_at,status,claim_token,claim_expires_at) VALUES ('count-1','poll-1','count','count:poll-1',10,'claimed','token',200)"); err != nil {
		t.Fatal(err)
	}
	loaded, err := st.LoadCountWork(ctx, "count-1", "token", time.Unix(100, 0))
	if err != nil || loaded.SnapshotID != "snapshot-1" || string(loaded.InputJSON) != `{"input":1}` {
		t.Fatalf("loaded=%#v error=%v", loaded, err)
	}
	decision := []byte(`{"sequence":1,"selected_option_ids":["a"]}`)
	created, err := st.CommitCountDecision(ctx, "count-1", "token", time.Unix(100, 0), 1, "request-one", decision)
	if err != nil || !created {
		t.Fatalf("decision created=%v error=%v", created, err)
	}
	created, err = st.CommitCountDecision(ctx, "count-1", "token", time.Unix(100, 0), 1, "request-one", decision)
	if err != nil || created {
		t.Fatalf("repeat decision created=%v error=%v", created, err)
	}
	if _, err := st.CommitCountDecision(ctx, "count-1", "token", time.Unix(100, 0), 1, "different", []byte(`{}`)); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed decision error=%v", err)
	}
	loaded, err = st.LoadCountWork(ctx, "count-1", "token", time.Unix(100, 0))
	if err != nil || len(loaded.DecisionsJSON) != 1 || string(loaded.DecisionsJSON[0]) != string(decision) {
		t.Fatalf("replay load=%#v error=%v", loaded, err)
	}
	created, err = st.CommitCountResult(ctx, "count-1", "token", time.Unix(100, 0), []byte(`{"winners":["a"]}`))
	if err != nil || !created {
		t.Fatalf("result created=%v error=%v", created, err)
	}
	var countWorkStatus string
	if err := st.DB.QueryRowContext(ctx, "SELECT status FROM work_items WHERE id='count-1'").Scan(&countWorkStatus); err != nil || countWorkStatus != "succeeded" {
		t.Fatalf("count work status=%s error=%v", countWorkStatus, err)
	}
	if _, err = st.CommitCountResult(ctx, "count-1", "token", time.Unix(100, 0), []byte(`{"winners":["a"]}`)); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale result acknowledgement error=%v", err)
	}
	if _, err := st.CommitCountResult(ctx, "count-1", "stale", time.Unix(100, 0), []byte(`{}`)); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale result error=%v", err)
	}
}

func TestCountResultCreatesOneAnnouncementPerContactAfterCommit(t *testing.T) {
	ctx := context.Background()
	st := workflowStore(t)
	defer st.Close()
	statements := []string{
		"UPDATE polls SET state='closed',announce=1 WHERE id='poll-1'",
		"INSERT INTO options(poll_id,id,label,display_order) VALUES ('poll-1','a','Alice',1),('poll-1','b','Bob',2)",
		"INSERT INTO participants(id,poll_id) VALUES ('person','poll-1')",
		"INSERT INTO contacts(id,poll_id,participant_id,delivery_email,normalized_email) VALUES ('contact','poll-1','person','reader@example.test','reader@example.test')",
		`INSERT INTO count_snapshots(id,poll_id,schema_version,rule,input_fingerprint,input_json,created_at) VALUES ('snapshot','poll-1',1,'irish-guided-stv-v1','fingerprint','{}',10)`,
		"INSERT INTO work_items(id,poll_id,kind,logical_key,due_at,status,claim_token,claim_expires_at) VALUES ('count','poll-1','count','count:poll-1',10,'claimed','count-token',300)",
	}
	for _, statement := range statements {
		if _, err := st.DB.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	result := []byte(`{"winners":["a"]}`)
	if created, err := st.CommitCountResult(ctx, "count", "count-token", time.Unix(100, 0), result); err != nil || !created {
		t.Fatalf("created=%v error=%v", created, err)
	}
	claimed, err := st.ClaimDueWork(ctx, "delivery", "mail-token", time.Unix(100, 0))
	if err != nil || claimed == nil {
		t.Fatalf("claimed=%#v error=%v", claimed, err)
	}
	attempt, err := st.BeginDeliveryAttempt(ctx, claimed.ID, claimed.ClaimToken, time.Unix(100, 0))
	if err != nil || attempt.MessageKind != "announcement" || len(attempt.Winners) != 1 || attempt.Winners[0] != "Alice" || attempt.NoVotes {
		t.Fatalf("attempt=%#v error=%v", attempt, err)
	}
	var workCount int
	if err := st.DB.QueryRowContext(ctx, "SELECT count(*) FROM work_items WHERE logical_key LIKE 'announcement:%'").Scan(&workCount); err != nil || workCount != 1 {
		t.Fatalf("announcement work=%d error=%v", workCount, err)
	}
}

func TestDeliveryAttemptPersistsBeforeRetryAndReleasesClaim(t *testing.T) {
	ctx := context.Background()
	st := workflowStore(t)
	defer st.Close()
	if _, err := st.DB.ExecContext(ctx, "UPDATE polls SET state='open',deadline=500 WHERE id='poll-1'"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.ExecContext(ctx, "INSERT INTO participants(id,poll_id) VALUES ('person','poll-1')"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.ExecContext(ctx, "INSERT INTO contacts(id,poll_id,participant_id,delivery_email,normalized_email) VALUES ('contact','poll-1','person','reader@example.test','reader@example.test')"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.ExecContext(ctx, "INSERT INTO work_items(id,poll_id,kind,logical_key,due_at,status,claim_token,claim_expires_at) VALUES ('mail','poll-1','delivery','invitation:poll-1:contact',10,'claimed','token',300)"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.ExecContext(ctx, "INSERT INTO deliveries(id,work_id,contact_id,recipient_email,message_kind,status,next_due) VALUES ('delivery','mail','contact','reader@example.test','invitation','pending',10)"); err != nil {
		t.Fatal(err)
	}
	attempt, err := st.BeginDeliveryAttempt(ctx, "mail", "token", time.Unix(100, 0))
	if err != nil || attempt.Attempts != 1 || attempt.RecipientEmail != "reader@example.test" || attempt.Question != "Question" {
		t.Fatalf("attempt=%#v error=%v", attempt, err)
	}
	if err := st.RetryDelivery(ctx, "mail", "token", time.Unix(100, 0), time.Unix(160, 0), "temporary"); err != nil {
		t.Fatal(err)
	}
	var deliveryStatus, workStatus string
	var due int64
	if err := st.DB.QueryRowContext(ctx, "SELECT status,next_due FROM deliveries WHERE id='delivery'").Scan(&deliveryStatus, &due); err != nil {
		t.Fatal(err)
	}
	if err := st.DB.QueryRowContext(ctx, "SELECT status FROM work_items WHERE id='mail'").Scan(&workStatus); err != nil {
		t.Fatal(err)
	}
	if deliveryStatus != "retrying" || workStatus != "pending" || due != 160 {
		t.Fatalf("delivery=%s work=%s due=%d", deliveryStatus, workStatus, due)
	}
}

func TestDeliveryAttemptFinalizesExpiredSixthAttemptWithoutSendingAgain(t *testing.T) {
	ctx := context.Background()
	st := workflowStore(t)
	defer st.Close()
	if _, err := st.DB.ExecContext(ctx, "UPDATE polls SET state='open',deadline=500 WHERE id='poll-1'"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.ExecContext(ctx, "INSERT INTO participants(id,poll_id) VALUES ('person','poll-1')"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.ExecContext(ctx, "INSERT INTO contacts(id,poll_id,participant_id,delivery_email,normalized_email) VALUES ('contact','poll-1','person','reader@example.test','reader@example.test')"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.ExecContext(ctx, "INSERT INTO work_items(id,poll_id,kind,logical_key,due_at,status,attempts,claim_token,claim_expires_at) VALUES ('mail','poll-1','delivery','invitation:poll-1:contact',10,'claimed',6,'token',300)"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.ExecContext(ctx, "INSERT INTO deliveries(id,work_id,contact_id,recipient_email,message_kind,status,next_due) VALUES ('delivery','mail','contact','reader@example.test','invitation','in_flight',10)"); err != nil {
		t.Fatal(err)
	}

	if _, err := st.BeginDeliveryAttempt(ctx, "mail", "token", time.Unix(100, 0)); !errors.Is(err, ErrDeliveryExhausted) {
		t.Fatalf("error=%v", err)
	}
	var attempts int
	var workStatus, deliveryStatus string
	if err := st.DB.QueryRowContext(ctx, "SELECT attempts,status FROM work_items WHERE id='mail'").Scan(&attempts, &workStatus); err != nil {
		t.Fatal(err)
	}
	if err := st.DB.QueryRowContext(ctx, "SELECT status FROM deliveries WHERE id='delivery'").Scan(&deliveryStatus); err != nil {
		t.Fatal(err)
	}
	if attempts != 6 || workStatus != "failed" || deliveryStatus != "failed" {
		t.Fatalf("attempts=%d work=%s delivery=%s", attempts, workStatus, deliveryStatus)
	}
}

func TestDeliveryAttemptHoldsPausedAndCancelsIneligibleInvitations(t *testing.T) {
	for _, tc := range []struct {
		name       string
		state      string
		deadline   int64
		wantErr    error
		workStatus string
		mailStatus string
	}{
		{name: "paused", state: "paused", deadline: 500, wantErr: ErrDeliveryHeld, workStatus: "pending", mailStatus: "pending"},
		{name: "closed", state: "closed", deadline: 500, wantErr: ErrDeliveryCancelled, workStatus: "succeeded", mailStatus: "cancelled"},
		{name: "elapsed", state: "open", deadline: 100, wantErr: ErrDeliveryCancelled, workStatus: "succeeded", mailStatus: "cancelled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			st := workflowStore(t)
			defer st.Close()
			if _, err := st.DB.ExecContext(ctx, "UPDATE polls SET state=?,deadline=? WHERE id='poll-1'", tc.state, tc.deadline); err != nil {
				t.Fatal(err)
			}
			if _, err := st.DB.ExecContext(ctx, "INSERT INTO participants(id,poll_id) VALUES ('person','poll-1')"); err != nil {
				t.Fatal(err)
			}
			if _, err := st.DB.ExecContext(ctx, "INSERT INTO contacts(id,poll_id,participant_id,delivery_email,normalized_email) VALUES ('contact','poll-1','person','reader@example.test','reader@example.test')"); err != nil {
				t.Fatal(err)
			}
			if _, err := st.DB.ExecContext(ctx, "INSERT INTO work_items(id,poll_id,kind,logical_key,due_at,status,claim_token,claim_expires_at) VALUES ('mail','poll-1','delivery','invitation:poll-1:contact',10,'claimed','token',300)"); err != nil {
				t.Fatal(err)
			}
			if _, err := st.DB.ExecContext(ctx, "INSERT INTO deliveries(id,work_id,contact_id,recipient_email,message_kind,status,next_due) VALUES ('delivery','mail','contact','reader@example.test','invitation','pending',10)"); err != nil {
				t.Fatal(err)
			}
			if _, err := st.BeginDeliveryAttempt(ctx, "mail", "token", time.Unix(100, 0)); !errors.Is(err, tc.wantErr) {
				t.Fatalf("error=%v", err)
			}
			var workStatus, mailStatus string
			var attempts int
			if err := st.DB.QueryRowContext(ctx, "SELECT status,attempts FROM work_items WHERE id='mail'").Scan(&workStatus, &attempts); err != nil {
				t.Fatal(err)
			}
			if err := st.DB.QueryRowContext(ctx, "SELECT status FROM deliveries WHERE id='delivery'").Scan(&mailStatus); err != nil {
				t.Fatal(err)
			}
			if workStatus != tc.workStatus || mailStatus != tc.mailStatus || attempts != 0 {
				t.Fatalf("work=%s delivery=%s attempts=%d", workStatus, mailStatus, attempts)
			}
		})
	}
}

func TestPrepareDeliveryGrantReusesAndRotatesPersistedClaims(t *testing.T) {
	ctx := context.Background()
	st := workflowStore(t)
	defer st.Close()
	if _, err := st.DB.ExecContext(ctx, "UPDATE polls SET state='open',deadline=500 WHERE id='poll-1'"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.ExecContext(ctx, "INSERT INTO participants(id,poll_id) VALUES ('person','poll-1')"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.ExecContext(ctx, "INSERT INTO contacts(id,poll_id,participant_id,delivery_email,normalized_email) VALUES ('contact','poll-1','person','reader@example.test','reader@example.test')"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.ExecContext(ctx, "INSERT INTO work_items(id,poll_id,kind,logical_key,due_at,status,claim_token,claim_expires_at) VALUES ('mail','poll-1','delivery','invitation:poll-1:contact',10,'claimed','token',300)"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.ExecContext(ctx, "INSERT INTO deliveries(id,work_id,contact_id,recipient_email,message_kind,status,next_due) VALUES ('delivery','mail','contact','reader@example.test','invitation','pending',10)"); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(100, 0)
	first := GrantMaterial{ID: "grant-1", KeyID: "key-1", TokenHash: bytes.Repeat([]byte{1}, 32), ClaimsJSON: []byte(`{"nonce":"one"}`), IssuedAt: now, ExpiresAt: now.Add(time.Hour)}
	payload, err := st.PrepareDeliveryGrant(ctx, "mail", "token", "key-1", now, first)
	if err != nil || string(payload) != string(first.ClaimsJSON) {
		t.Fatalf("first payload=%s error=%v", payload, err)
	}
	unused := GrantMaterial{ID: "grant-unused", KeyID: "key-1", TokenHash: bytes.Repeat([]byte{2}, 32), ClaimsJSON: []byte(`{"nonce":"unused"}`), IssuedAt: now, ExpiresAt: now.Add(time.Hour)}
	payload, err = st.PrepareDeliveryGrant(ctx, "mail", "token", "key-1", now, unused)
	if err != nil || string(payload) != string(first.ClaimsJSON) {
		t.Fatalf("reused payload=%s error=%v", payload, err)
	}
	rotated := GrantMaterial{ID: "grant-2", KeyID: "key-2", TokenHash: bytes.Repeat([]byte{3}, 32), ClaimsJSON: []byte(`{"nonce":"two"}`), IssuedAt: now, ExpiresAt: now.Add(time.Hour)}
	payload, err = st.PrepareDeliveryGrant(ctx, "mail", "token", "key-2", now, rotated)
	if err != nil || string(payload) != string(rotated.ClaimsJSON) {
		t.Fatalf("rotated payload=%s error=%v", payload, err)
	}
	var grants, revoked int
	if err := st.DB.QueryRowContext(ctx, "SELECT count(*),sum(CASE WHEN revoked_at IS NOT NULL THEN 1 ELSE 0 END) FROM grants").Scan(&grants, &revoked); err != nil {
		t.Fatal(err)
	}
	if grants != 2 || revoked != 1 {
		t.Fatalf("grants=%d revoked=%d", grants, revoked)
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

func TestWorkOutcomePersistenceCoversAcceptedFailedAndCloseReads(t *testing.T) {
	ctx := context.Background()
	st := workflowStore(t)
	defer st.Close()
	for _, statement := range []string{
		"UPDATE polls SET state='open',deadline=500 WHERE id='poll-1'",
		"INSERT INTO options(poll_id,id,label,display_order) VALUES ('poll-1','a','A',1),('poll-1','b','B',2)",
		"INSERT INTO participants(id,poll_id) VALUES ('person','poll-1')",
		"INSERT INTO contacts(id,poll_id,participant_id,delivery_email,normalized_email) VALUES ('contact','poll-1','person','reader@example.test','reader@example.test')",
		"INSERT INTO work_items(id,poll_id,kind,logical_key,due_at,status,claim_token,claim_expires_at) VALUES ('accepted','poll-1','delivery','accepted',10,'claimed','accept-token',300)",
		"INSERT INTO deliveries(id,work_id,contact_id,recipient_email,message_kind,status,next_due) VALUES ('accepted-delivery','accepted','contact','reader@example.test','invitation','pending',10)",
		"INSERT INTO work_items(id,poll_id,kind,logical_key,due_at,status,claim_token,claim_expires_at) VALUES ('failed','poll-1','delivery','failed',10,'claimed','fail-token',300)",
		"INSERT INTO deliveries(id,work_id,contact_id,recipient_email,message_kind,status,next_due) VALUES ('failed-delivery','failed','contact','reader@example.test','invitation','pending',10)",
		"INSERT INTO work_items(id,poll_id,kind,logical_key,due_at,status,claim_token,claim_expires_at) VALUES ('close','poll-1','close','close-read',10,'claimed','close-token',300)",
		`INSERT INTO ballots(poll_id,participant_id,version,preferences_json,accepted_at) VALUES ('poll-1','person',1,'["a"]',90)`,
	} {
		if _, err := st.DB.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.BeginDeliveryAttempt(ctx, "accepted", "accept-token", time.Unix(100, 0)); err != nil {
		t.Fatal(err)
	}
	if err := st.AcceptDelivery(ctx, "accepted", "accept-token", time.Unix(100, 0)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.BeginDeliveryAttempt(ctx, "failed", "fail-token", time.Unix(100, 0)); err != nil {
		t.Fatal(err)
	}
	if err := st.FailDelivery(ctx, "failed", "fail-token", time.Unix(100, 0), "permanent rejection"); err != nil {
		t.Fatal(err)
	}
	closeWork, err := st.LoadCloseWork(ctx, "close", "close-token", time.Unix(100, 0))
	if err != nil || len(closeWork.Ballots) != 1 || len(closeWork.Options) != 2 {
		t.Fatalf("close work=%#v error=%v", closeWork, err)
	}
	participant, err := st.PollForParticipant(ctx, "person", "poll-1")
	if err != nil || participant.BallotVersion != 1 || len(participant.Preferences) != 1 {
		t.Fatalf("participant poll=%#v error=%v", participant, err)
	}
	if err := st.FailWork(ctx, "close", "close-token", time.Unix(100, 0), "close failed"); err != nil {
		t.Fatal(err)
	}
}

type failingRows struct {
	err    error
	closed bool
}

func (rows *failingRows) Err() error {
	return rows.err
}

func (rows *failingRows) Close() error {
	rows.closed = true
	return nil
}

func TestFinishRowsRejectsIterationFailureBeforeDurableConstruction(t *testing.T) {
	want := errors.New("persistence interrupted")
	rows := &failingRows{err: want}
	err := finishRows(rows, "close options")
	if !errors.Is(err, want) || !rows.closed {
		t.Fatalf("error=%v closed=%v", err, rows.closed)
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
