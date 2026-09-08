// ABOUTME: Verifies rank decoding for server-rendered ballot forms.
// ABOUTME: It protects consecutive partial rankings before persistence.
package web

import (
	"reflect"
	"testing"

	"github.com/tigger-developer/Instant-Runoff-Polls-PR-STV-/internal/store"
)

func TestSelectedPreferencesAcceptsOrderedUniqueSubset(t *testing.T) {
	options := []store.PollOption{{ID: "a"}, {ID: "b"}, {ID: "c"}}
	preferences, err := selectedPreferences(options, []string{"c", "a"})
	if err != nil || !reflect.DeepEqual(preferences, []string{"c", "a"}) {
		t.Fatalf("preferences=%v error=%v", preferences, err)
	}
}

func TestSelectedPreferencesRejectsDuplicateUnknownAndEmptyBallot(t *testing.T) {
	options := []store.PollOption{{ID: "a"}, {ID: "b"}, {ID: "c"}}
	for _, preferences := range [][]string{{"a", "a"}, {"a", "missing"}, {}} {
		if _, err := selectedPreferences(options, preferences); err == nil {
			t.Fatalf("preferences=%v unexpectedly accepted", preferences)
		}
	}
}

func TestBuildBallotPageMovesSelectedOptionsIntoNumberedTracker(t *testing.T) {
	view := store.ParticipantPoll{ParticipantName: "Alex", Poll: store.PollRecord{ID: "poll", Options: []store.PollOption{{ID: "a", Label: "Alpha"}, {ID: "b", Label: "Beta"}, {ID: "c", Label: "Gamma"}}}}
	page, err := buildBallotPage(view, []string{"b", "a"})
	if err != nil {
		t.Fatal(err)
	}
	if page.ParticipantName != "Alex" || page.NextPreference != "3rd" || len(page.Available) != 1 || page.Available[0].Label != "Gamma" || len(page.Selected) != 2 || page.Selected[0].Rank != 1 || page.Selected[0].Label != "Beta" || page.Selected[1].Rank != 2 || page.Selected[1].Label != "Alpha" {
		t.Fatalf("page=%#v", page)
	}
	if page.Available[0].SelectURL != "/polls/poll?preference=b&preference=a&preference=c" {
		t.Fatalf("select URL=%s", page.Available[0].SelectURL)
	}
}

func TestBuildBallotPageShowsRecordedStateUntilVoterChoosesToChange(t *testing.T) {
	view := store.ParticipantPoll{
		ParticipantName: "Alex",
		Preferences:     []string{"a", "b"},
		Poll: store.PollRecord{
			ID:      "poll",
			Options: []store.PollOption{{ID: "a", Label: "Alpha"}, {ID: "b", Label: "Beta"}},
		},
	}
	page, err := buildBallotPage(view, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !page.Recorded || len(page.Available) != 0 || len(page.Selected) != 2 {
		t.Fatalf("recorded page=%#v", page)
	}

	restarted, err := buildBallotPage(view, []string{})
	if err != nil {
		t.Fatal(err)
	}
	if restarted.Recorded || restarted.NextPreference != "first" || len(restarted.Available) != 2 || len(restarted.Selected) != 0 {
		t.Fatalf("restarted page=%#v", restarted)
	}
}
