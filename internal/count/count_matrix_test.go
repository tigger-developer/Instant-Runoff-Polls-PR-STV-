// ABOUTME: Exercises the complete signed-off PR-STV fixture and boundary matrix.
// ABOUTME: It supplements focused transition tests with deterministic replay evidence.
package count

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"testing"
)

func TestRunCalculatesFixedQuotaForEveryPlaceCount(t *testing.T) {
	input := Input{SchemaVersion: 1, Rule: RuleIrishGuidedSTV, Options: []string{"A", "B", "C"}, Ballots: expandBallots([]ballotGroup{{8, []string{"A", "B", "C"}}, {1, []string{"B"}}, {1, []string{"C"}}})}
	for _, testCase := range []struct{ places, quota int }{{1, 6}, {2, 4}, {3, 3}} {
		candidate := cloneInput(input)
		candidate.Places = testCase.places
		outcome := runChoosing(t, candidate, "B")
		if outcome.Result.Quota != testCase.quota {
			t.Fatalf("places %d quota = %d, want %d", testCase.places, outcome.Result.Quota, testCase.quota)
		}
	}
}

func TestRunSelectsLargestPendingSurplus(t *testing.T) {
	input := Input{SchemaVersion: 1, Rule: RuleIrishGuidedSTV, Options: []string{"A", "B", "C", "D"}, Places: 3, Ballots: expandBallots([]ballotGroup{{5, []string{"A", "C"}}, {4, []string{"B", "D"}}, {2, []string{"C"}}, {1, []string{"D"}}})}
	outcome, err := Run(context.Background(), input, nil)
	if err != nil || outcome.Result == nil || !reflect.DeepEqual(outcome.Result.Winners, []string{"A", "B", "C"}) {
		t.Fatalf("outcome = %#v, error = %v", outcome, err)
	}
	if !recordContains(outcome.Result.Counts, func(record CountRecord) bool { return record.SurplusOptionID == "A" }) {
		t.Fatalf("counts = %#v, want A surplus processed", outcome.Result.Counts)
	}
}

func TestRunReplaysBothLastParcelRemainderOutcomes(t *testing.T) {
	input := Input{SchemaVersion: 1, Rule: RuleIrishGuidedSTV, Options: []string{"A", "B", "C", "D"}, Places: 2, Ballots: expandBallots([]ballotGroup{{4, []string{"A", "B"}}, {3, []string{"B"}}, {3, []string{"C"}}, {1, []string{"D", "A", "B"}}, {1, []string{"D", "A", "C"}}})}
	for _, testCase := range []struct {
		choice  string
		winners []string
	}{{"B", []string{"A", "B"}}, {"C", []string{"A", "C"}}} {
		outcome := runChoosing(t, input, testCase.choice)
		if !reflect.DeepEqual(outcome.Result.Winners, testCase.winners) {
			t.Fatalf("choice %s winners = %#v, want %#v", testCase.choice, outcome.Result.Winners, testCase.winners)
		}
	}
}

func TestRunSkipsExcludedAndElectedPreferences(t *testing.T) {
	tests := []struct {
		name    string
		options []string
		places  int
		ballots []Ballot
		want    []string
	}{
		{name: "excluded", options: []string{"A", "B", "C", "D"}, places: 1, ballots: expandBallots([]ballotGroup{{4, []string{"A"}}, {3, []string{"B"}}, {1, []string{"C"}}, {2, []string{"D", "C", "B"}}}), want: []string{"B"}},
		{name: "elected", options: []string{"A", "B", "C", "D"}, places: 2, ballots: expandBallots([]ballotGroup{{4, []string{"A"}}, {3, []string{"B"}}, {2, []string{"C"}}, {1, []string{"D", "A", "B"}}}), want: []string{"A", "B"}},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			outcome := runChoosing(t, Input{SchemaVersion: 1, Rule: RuleIrishGuidedSTV, Options: testCase.options, Places: testCase.places, Ballots: testCase.ballots}, "D")
			if !reflect.DeepEqual(outcome.Result.Winners, testCase.want) {
				t.Fatalf("winners = %#v, want %#v", outcome.Result.Winners, testCase.want)
			}
		})
	}
}

func TestRunRejectsEveryMalformedDecisionField(t *testing.T) {
	input := Input{SchemaVersion: 1, Rule: RuleIrishGuidedSTV, Options: []string{"A", "B"}, Places: 1, Ballots: expandBallots([]ballotGroup{{2, []string{"A", "B"}}, {2, []string{"B", "A"}}})}
	requestOutcome, err := Run(context.Background(), input, nil)
	if err != nil || requestOutcome.DecisionRequest == nil {
		t.Fatalf("request = %#v, error = %v", requestOutcome, err)
	}
	request := requestOutcome.DecisionRequest
	valid := Decision{Sequence: request.Sequence, Kind: request.Kind, RequestFingerprint: request.RequestFingerprint, SelectedOptionIDs: []string{"B"}}
	cases := []struct {
		name   string
		mutate func(*Decision)
	}{
		{"sequence", func(value *Decision) { value.Sequence++ }},
		{"kind", func(value *Decision) { value.Kind = "remainder_lot" }},
		{"fingerprint", func(value *Decision) { value.RequestFingerprint = "wrong" }},
		{"empty selection", func(value *Decision) { value.SelectedOptionIDs = nil }},
		{"multiple selections", func(value *Decision) { value.SelectedOptionIDs = []string{"A", "B"} }},
		{"ineligible selection", func(value *Decision) { value.SelectedOptionIDs = []string{"C"} }},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			candidate := valid
			candidate.SelectedOptionIDs = append([]string(nil), valid.SelectedOptionIDs...)
			testCase.mutate(&candidate)
			outcome, err := Run(context.Background(), input, []Decision{candidate})
			if !errors.Is(err, ErrInvalidDecision) || outcome.Result != nil || outcome.DecisionRequest != nil {
				t.Fatalf("outcome = %#v, error = %v", outcome, err)
			}
		})
	}
}

func TestRunGeneratedConservationAndReplaySeed20260908(t *testing.T) {
	random := rand.New(rand.NewSource(20260908))
	for caseIndex := range 100 {
		optionCount := 2 + random.Intn(7)
		options := make([]string, optionCount)
		for index := range options {
			options[index] = fmt.Sprintf("o%d", index+1)
		}
		ballotCount := 1 + random.Intn(50)
		generated := make([]Ballot, ballotCount)
		for index := range generated {
			order := random.Perm(optionCount)
			preferenceCount := 1 + random.Intn(optionCount)
			preferences := make([]string, preferenceCount)
			for preferenceIndex := range preferences {
				preferences[preferenceIndex] = options[order[preferenceIndex]]
			}
			generated[index] = Ballot{ID: fmt.Sprintf("b%d", index+1), Preferences: preferences}
		}
		input := Input{SchemaVersion: 1, Rule: RuleIrishGuidedSTV, Options: options, Places: 1 + random.Intn(optionCount), Ballots: generated}
		outcome := runChoosing(t, input, "")
		for _, record := range outcome.Result.Counts {
			total := record.Exhausted
			for _, option := range record.Totals {
				total += option.Votes
			}
			if total != ballotCount {
				t.Fatalf("case %d count %d total = %d, want %d", caseIndex, record.Index, total, ballotCount)
			}
		}
		replayed, err := Run(context.Background(), input, outcome.Result.UsedDecisions)
		if err != nil || !reflect.DeepEqual(outcome.Result, replayed.Result) {
			t.Fatalf("case %d replay mismatch: %v", caseIndex, err)
		}
	}
}

func TestRunRejectsOptionCountAboveLimit(t *testing.T) {
	options := make([]string, 51)
	for index := range options {
		options[index] = fmt.Sprintf("o%d", index+1)
	}
	input := Input{SchemaVersion: 1, Rule: RuleIrishGuidedSTV, Options: options, Places: 1, Ballots: []Ballot{{ID: "b1", Preferences: []string{"o1"}}}}
	if _, err := Run(context.Background(), input, nil); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("error = %v", err)
	}
}
