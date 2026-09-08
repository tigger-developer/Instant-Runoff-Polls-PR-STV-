// ABOUTME: Verifies anonymized frozen count inputs and deterministic replay.
// ABOUTME: It protects snapshot ordering and the W002 integration boundary.
package workflow

import (
	"bytes"
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/tigger-developer/Instant-Runoff-Polls-PR-STV-/internal/count"
)

func TestBuildCountInputAnonymizesAndFreezesEffectiveBallots(t *testing.T) {
	poll := validOpenPoll(time.Now().Add(time.Hour))
	poll.State = Closed
	poll.Participants = append(poll.Participants, Participant{ID: "person-secret", Addresses: []Address{{Delivery: "secret@example.test", Normalized: "secret@example.test"}}})
	poll.Ballots["p1"] = Ballot{ParticipantID: "p1", Preferences: []string{"a", "b"}, Version: 2}
	poll.Ballots["person-secret"] = Ballot{ParticipantID: "person-secret", Preferences: []string{"a"}, Version: 1}

	first, err := BuildCountInput(poll, bytes.NewReader(bytes.Repeat([]byte{0}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	second, err := BuildCountInput(poll, bytes.NewReader(bytes.Repeat([]byte{0}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("same frozen input and randomness did not reproduce order")
	}
	if len(first.Ballots) != 2 || first.Ballots[0].ID != "b1" || first.Ballots[1].ID != "b2" {
		t.Fatalf("opaque ballots = %#v", first.Ballots)
	}
	for _, ballot := range first.Ballots {
		if ballot.ID == "p1" || ballot.ID == "person-secret" {
			t.Fatal("snapshot retained participant identity")
		}
	}
	outcome, err := count.Run(context.Background(), first, nil)
	if err != nil || outcome.Result == nil {
		t.Fatalf("count outcome = %#v, error = %v", outcome, err)
	}
}

func TestBuildCountInputRejectsNonClosedAndZeroTurnoutPolls(t *testing.T) {
	poll := validOpenPoll(time.Now().Add(time.Hour))
	if _, err := BuildCountInput(poll, bytes.NewReader(make([]byte, 32))); err == nil {
		t.Fatal("open poll snapshot succeeded")
	}
	poll.State = Closed
	if _, err := BuildCountInput(poll, bytes.NewReader(make([]byte, 32))); err == nil {
		t.Fatal("zero-turnout snapshot succeeded")
	}
}

func TestChooseCountDecisionRecordsExactRequestForReplay(t *testing.T) {
	request := count.DecisionRequest{Sequence: 2, Kind: "remainder_lot", RequestFingerprint: "fingerprint", EligibleOptionIDs: []string{"a", "b"}, RequiredSelections: 1}
	decision, err := ChooseCountDecision(request, bytes.NewReader(bytes.Repeat([]byte{0}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	if decision.Sequence != request.Sequence || decision.Kind != request.Kind || decision.RequestFingerprint != request.RequestFingerprint || !reflect.DeepEqual(decision.SelectedOptionIDs, []string{"a"}) {
		t.Fatalf("decision = %#v", decision)
	}
	if _, err := ChooseCountDecision(count.DecisionRequest{RequiredSelections: 2, EligibleOptionIDs: []string{"a", "b"}}, bytes.NewReader(make([]byte, 32))); err == nil {
		t.Fatal("unsupported decision cardinality succeeded")
	}
}
