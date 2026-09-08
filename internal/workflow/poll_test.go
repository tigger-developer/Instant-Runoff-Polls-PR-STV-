// ABOUTME: Verifies poll lifecycle, deadline, version, and ballot replacement rules.
// ABOUTME: It exercises the transactional decisions independently of HTTP and SQLite.
package workflow

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestPollValidatesOpeningAndFreezesDefinition(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	poll := Poll{State: Draft, Version: 1, Question: "Choose", Options: []Option{{ID: "a", Label: "A"}, {ID: "b", Label: "B"}}, Places: 1, Deadline: now.Add(time.Hour), Participants: []Participant{{Addresses: []Address{{Delivery: "p@example.test", Normalized: "p@example.test"}}}}}
	if err := poll.Open(now, 1); err != nil {
		t.Fatal(err)
	}
	if poll.State != Open || poll.Version != 2 {
		t.Fatalf("opened poll = %#v", poll)
	}
	if err := poll.UpdateDefinition("Changed", poll.Options, 1, poll.Deadline, 2); !errors.Is(err, ErrPollFrozen) {
		t.Fatalf("update error = %v", err)
	}
}

func TestPollRejectsInvalidOpeningWithoutMutation(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	poll := Poll{State: Draft, Version: 3, Question: "Choose", Options: []Option{{ID: "a", Label: "A"}}, Places: 1, Deadline: now.Add(time.Hour)}
	want := poll
	if err := poll.Open(now, 3); err == nil {
		t.Fatal("expected invalid opening to fail")
	}
	if !reflect.DeepEqual(poll, want) {
		t.Fatalf("failed open mutated poll: %#v", poll)
	}
}

func TestDraftDefinitionUpdatesAtCurrentVersion(t *testing.T) {
	deadline := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	poll := Poll{State: Draft, Version: 1}
	options := []Option{{ID: "a", Label: "Alice"}, {ID: "b", Label: "Bob"}}
	if err := poll.UpdateDefinition("Choose", options, 1, deadline, 1); err != nil {
		t.Fatal(err)
	}
	if poll.Question != "Choose" || poll.Version != 2 || !poll.Deadline.Equal(deadline) || len(poll.Options) != 2 {
		t.Fatalf("poll=%#v", poll)
	}
	if err := poll.UpdateDefinition("Stale", options, 1, deadline, 1); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("stale update error=%v", err)
	}
}

func TestPollReplacesBallotOnlyBeforeDeadlineAtCurrentVersion(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	poll := validOpenPoll(now.Add(time.Hour))
	if err := poll.ReplaceBallot("p1", []string{"b"}, 0, now); err != nil {
		t.Fatal(err)
	}
	if err := poll.ReplaceBallot("p1", []string{"a"}, 0, now); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("stale update error = %v", err)
	}
	if err := poll.ReplaceBallot("p1", []string{"a"}, 1, poll.Deadline); !errors.Is(err, ErrVotingClosed) {
		t.Fatalf("deadline update error = %v", err)
	}
	if got := poll.Ballots["p1"].Preferences; !reflect.DeepEqual(got, []string{"b"}) {
		t.Fatalf("ballot = %v", got)
	}
}

func TestPollValidatesConsecutivePartialRanking(t *testing.T) {
	poll := validOpenPoll(time.Now().Add(time.Hour))
	for _, ranking := range [][]string{{}, {"a", "a"}, {"unknown"}} {
		if err := poll.ReplaceBallot("p1", ranking, 0, time.Now()); !errors.Is(err, ErrInvalidBallot) {
			t.Fatalf("ranking %v error = %v", ranking, err)
		}
	}
	if err := poll.ReplaceBallot("p1", []string{"b"}, 0, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestPollPauseResumeAndCloseAreIdempotent(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	poll := validOpenPoll(now.Add(time.Hour))
	if err := poll.Pause(1); err != nil {
		t.Fatal(err)
	}
	if err := poll.ReplaceBallot("p1", []string{"a"}, 0, now); !errors.Is(err, ErrVotingClosed) {
		t.Fatalf("paused vote error = %v", err)
	}
	if err := poll.Resume(now, 2); err != nil {
		t.Fatal(err)
	}
	if changed, err := poll.Close(now, 3); err != nil || !changed {
		t.Fatalf("close changed=%v error=%v", changed, err)
	}
	if changed, err := poll.Close(now, 4); err != nil || changed {
		t.Fatalf("repeated close changed=%v error=%v", changed, err)
	}
}

func validOpenPoll(deadline time.Time) Poll {
	return Poll{State: Open, Version: 1, Question: "Choose", Options: []Option{{ID: "a", Label: "A"}, {ID: "b", Label: "B"}}, Places: 1, Deadline: deadline, Participants: []Participant{{ID: "p1", Addresses: []Address{{Delivery: "p@example.test", Normalized: "p@example.test"}}}}, Ballots: map[string]Ballot{}}
}
