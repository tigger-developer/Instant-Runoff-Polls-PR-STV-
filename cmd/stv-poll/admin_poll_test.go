// ABOUTME: Verifies manual poll closure and count-audit export through admin services.
// ABOUTME: It protects frozen count evidence while excluding voter contact data.
package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/tigger-developer/Instant-Runoff-Polls-PR-STV-/internal/config"
	"github.com/tigger-developer/Instant-Runoff-Polls-PR-STV-/internal/store"
	"github.com/tigger-developer/Instant-Runoff-Polls-PR-STV-/internal/workflow"
)

func TestManualCloseFreezesBallotsAndQueuesCount(t *testing.T) {
	ctx := context.Background()
	st := adminPollStore(t)
	defer st.Close()

	closed, err := closePoll(ctx, st, adminConfig(), "poll", bytes.NewReader(bytes.Repeat([]byte{7}, 64)), time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	if closed.PollID != "poll" || closed.State != "closed" || closed.CountingStatus != "pending" {
		t.Fatalf("closed=%#v", closed)
	}
	var snapshots, countWork int
	if err := st.DB.QueryRowContext(ctx, "SELECT count(*) FROM count_snapshots WHERE poll_id='poll'").Scan(&snapshots); err != nil {
		t.Fatal(err)
	}
	if err := st.DB.QueryRowContext(ctx, "SELECT count(*) FROM work_items WHERE poll_id='poll' AND kind='count' AND status='pending'").Scan(&countWork); err != nil {
		t.Fatal(err)
	}
	if snapshots != 1 || countWork != 1 {
		t.Fatalf("snapshots=%d count work=%d", snapshots, countWork)
	}
}

func TestCountAuditExportsFrozenInputAndResultWithoutVoterIdentity(t *testing.T) {
	ctx := context.Background()
	st := adminPollStore(t)
	defer st.Close()
	if _, err := closePoll(ctx, st, adminConfig(), "poll", bytes.NewReader(bytes.Repeat([]byte{7}, 64)), time.Unix(100, 0)); err != nil {
		t.Fatal(err)
	}
	summary, err := workflow.ProcessDueWork(ctx, st, map[string]workflow.WorkHandler{
		"count": workflow.NewCountHandler(st, bytes.NewReader(bytes.Repeat([]byte{8}, 256)), func() time.Time { return time.Unix(101, 0) }),
	}, func() string { return "claim" }, func() time.Time { return time.Unix(101, 0) })
	if err != nil || summary.Counted != 1 {
		t.Fatalf("summary=%#v error=%v", summary, err)
	}
	var output bytes.Buffer
	if err := exportCountAudit(ctx, st, adminConfig(), "poll", &output); err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{`"poll_id":"poll"`, `"rule":"irish-guided-stv-v1"`, `"input_fingerprint"`, `"result"`, `"winners":["a"]`} {
		if !strings.Contains(output.String(), required) {
			t.Fatalf("audit missing %s: %s", required, output.String())
		}
	}
	for _, forbidden := range []string{"Alex", "one@example.test"} {
		if strings.Contains(output.String(), forbidden) {
			t.Fatalf("audit exposed %q: %s", forbidden, output.String())
		}
	}
}

func adminConfig() config.Config {
	return config.Config{Moderators: []config.Moderator{{ID: "owner", Email: "owner@example.test"}}}
}

func adminPollStore(t *testing.T) *store.Store {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SyncModerators(ctx, []store.ConfiguredModerator{{ID: "owner", NormalizedEmail: "owner@example.test"}}); err != nil {
		t.Fatal(err)
	}
	draft := store.DraftPoll{Question: "Question", Deadline: time.Unix(500, 0), Places: 1, Options: []store.PollOption{{ID: "a", Label: "Alpha"}, {ID: "b", Label: "Beta"}}}
	if err := st.CreateDraftPoll(ctx, "owner", "poll", draft, time.Unix(50, 0)); err != nil {
		t.Fatal(err)
	}
	participant := store.ElectorateParticipant{ID: "person", DisplayName: "Alex", Contacts: []store.Contact{{ID: "contact", DeliveryEmail: "one@example.test", NormalizedEmail: "one@example.test"}}}
	if err := st.ReplaceElectorate(ctx, "owner", "poll", 1, []store.ElectorateParticipant{participant}); err != nil {
		t.Fatal(err)
	}
	invitation := store.InvitationWork{WorkID: "invite", DeliveryID: "delivery", ParticipantID: "person", RecipientEmails: []string{"one@example.test"}}
	if _, err := st.OpenPoll(ctx, "owner", "poll", 2, "scheduled-close", []store.InvitationWork{invitation}, time.Unix(60, 0)); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceBallot(ctx, "poll", "person", 0, []byte(`["a","b"]`), func() time.Time { return time.Unix(90, 0) }); err != nil {
		t.Fatal(err)
	}
	return st
}
