// ABOUTME: Verifies aggregate-only moderator result projection.
// ABOUTME: It prevents count snapshots and ballot-level provenance reaching views.
package workflow

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/tigger-developer/Instant-Runoff-Polls-PR-STV-/internal/count"
)

func TestProjectResultKeepsAggregatesAndDropsBallotEvidence(t *testing.T) {
	result := count.Result{
		SchemaVersion:    1,
		Rule:             count.RuleIrishGuidedSTV,
		InputFingerprint: "secret-fingerprint",
		Quota:            3,
		Winners:          []string{"a"},
		Counts:           []count.CountRecord{{Index: 1, Totals: []count.OptionTotal{{OptionID: "a", Votes: 3}, {OptionID: "b", Votes: 1}}, Exhausted: 1, ElectedOptionIDs: []string{"a"}, Transfers: []count.Transfer{{BallotID: "secret-ballot", FromOptionID: "b", ToOptionID: "a"}}, Selections: []count.SelectionProvenance{{Kind: "remainder_lot", Method: "decision", SelectedOptionIDs: []string{"a"}, DecisionSequence: 1}}}},
		UsedDecisions:    []count.Decision{{Sequence: 1, RequestFingerprint: "secret-request", SelectedOptionIDs: []string{"a"}}},
	}
	view := ProjectResult(5, result)
	if view.Turnout != 5 || view.Quota != 3 || len(view.Counts) != 1 || view.Counts[0].Transfers[0].FromOptionID != "b" {
		t.Fatalf("view = %#v", view)
	}
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"secret-fingerprint", "secret-ballot", "secret-request", "DecisionSequence", "request_fingerprint"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("projection leaked %q: %s", secret, encoded)
		}
	}
}

func TestProjectZeroTurnoutHasNoWinnerOrCount(t *testing.T) {
	view := ProjectZeroTurnout()
	if !view.NoVotes || view.Turnout != 0 || len(view.Winners) != 0 || len(view.Counts) != 0 {
		t.Fatalf("zero-turnout view = %#v", view)
	}
}
