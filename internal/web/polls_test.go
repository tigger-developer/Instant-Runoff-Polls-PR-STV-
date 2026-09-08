// ABOUTME: Verifies rank decoding for server-rendered ballot forms.
// ABOUTME: It protects consecutive partial rankings before persistence.
package web

import (
	"reflect"
	"testing"

	"github.com/tigger-developer/Instant-Runoff-Polls-PR-STV-/internal/store"
)

func TestRankedPreferencesAcceptsConsecutiveSubsetInRankOrder(t *testing.T) {
	options := []store.PollOption{{ID: "a"}, {ID: "b"}, {ID: "c"}}
	preferences, err := rankedPreferences(options, []string{"2", "", "1"})
	if err != nil || !reflect.DeepEqual(preferences, []string{"c", "a"}) {
		t.Fatalf("preferences=%v error=%v", preferences, err)
	}
}

func TestRankedPreferencesRejectsDuplicatesGapsAndEmptyBallot(t *testing.T) {
	options := []store.PollOption{{ID: "a"}, {ID: "b"}, {ID: "c"}}
	for _, ranks := range [][]string{{"1", "1", ""}, {"1", "3", ""}, {"", "", ""}, {"1", ""}} {
		if _, err := rankedPreferences(options, ranks); err == nil {
			t.Fatalf("ranks=%v unexpectedly accepted", ranks)
		}
	}
}
