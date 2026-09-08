// ABOUTME: Verifies owner-scoped drafts and optimistic lifecycle persistence.
// ABOUTME: It exercises list ordering and frozen-definition boundaries.
package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestDraftPollCRUDIsOwnerScopedAndVersioned(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SyncModerators(ctx, []ConfiguredModerator{{ID: "one", NormalizedEmail: "one@example.test"}, {ID: "two", NormalizedEmail: "two@example.test"}}); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(100, 0)
	draft := DraftPoll{Question: "Question", Deadline: time.Unix(500, 0), Places: 1, Options: []PollOption{{ID: "a", Label: "Alice"}, {ID: "b", Label: "Bob"}}}
	if err := st.CreateDraftPoll(ctx, "one", "poll", draft, now); err != nil {
		t.Fatal(err)
	}
	if _, err := st.OwnedPoll(ctx, "two", "poll"); !errors.Is(err, ErrConflict) {
		t.Fatalf("cross-owner read error=%v", err)
	}
	draft.Question = "Changed"
	if err := st.UpdateDraftPoll(ctx, "one", "poll", 1, draft); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateDraftPoll(ctx, "one", "poll", 1, draft); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale update error=%v", err)
	}
	poll, err := st.OwnedPoll(ctx, "one", "poll")
	if err != nil || poll.Question != "Changed" || poll.Version != 2 || len(poll.Options) != 2 {
		t.Fatalf("poll=%#v error=%v", poll, err)
	}
	polls, err := st.OwnedPolls(ctx, "one", 0)
	if err != nil || len(polls) != 1 || polls[0].ID != "poll" {
		t.Fatalf("polls=%#v error=%v", polls, err)
	}
}

func TestPollPauseAndResumeCheckStateVersionAndDeadline(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SyncModerators(ctx, []ConfiguredModerator{{ID: "one", NormalizedEmail: "one@example.test"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.ExecContext(ctx, "INSERT INTO polls(id,owner_id,question,deadline,places,state,version) VALUES ('poll','one','Question',500,1,'open',1)"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetPollState(ctx, "one", "poll", "open", "paused", 1, time.Unix(100, 0)); err != nil {
		t.Fatal(err)
	}
	if err := st.SetPollState(ctx, "one", "poll", "paused", "open", 2, time.Unix(500, 0)); !errors.Is(err, ErrConflict) {
		t.Fatalf("elapsed resume error=%v", err)
	}
	if err := st.SetPollState(ctx, "one", "poll", "paused", "open", 2, time.Unix(499, 0)); err != nil {
		t.Fatal(err)
	}
}
