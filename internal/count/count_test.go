package count

import (
	"context"
	"testing"
)

func TestRunCalculatesQuotaAndStopsAfterSingleWinner(t *testing.T) {
	input := Input{SchemaVersion: 1, Rule: RuleIrishGuidedSTV, Options: []string{"A", "B", "C"}, Places: 1, Ballots: ballots("A", "A", "A", "A", "A", "A", "A", "A", "B", "B")}
	outcome, err := Run(context.Background(), input, nil)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Result == nil {
		t.Fatalf("outcome = %#v, want result", outcome)
	}
	if outcome.Result.Quota != 6 {
		t.Fatalf("quota = %d, want 6", outcome.Result.Quota)
	}
	if len(outcome.Result.Winners) != 1 || outcome.Result.Winners[0] != "A" {
		t.Fatalf("winners = %#v", outcome.Result.Winners)
	}
}

func TestRunRejectsInvalidInput(t *testing.T) {
	input := Input{SchemaVersion: 1, Rule: RuleIrishGuidedSTV, Options: []string{"A", "A"}, Places: 1, Ballots: ballots("A")}
	outcome, err := Run(context.Background(), input, nil)
	if err == nil || outcome.Result != nil || outcome.DecisionRequest != nil {
		t.Fatalf("outcome = %#v, error = %v", outcome, err)
	}
}

func TestRunTransfersOriginalSurplusUsingWholeBallots(t *testing.T) {
	input := Input{SchemaVersion: 1, Rule: RuleIrishGuidedSTV, Options: []string{"A", "B", "C", "D"}, Places: 2, Ballots: []Ballot{
		{ID: "b1", Preferences: []string{"A", "B"}}, {ID: "b2", Preferences: []string{"A", "B"}}, {ID: "b3", Preferences: []string{"A", "B"}}, {ID: "b4", Preferences: []string{"A", "B"}},
		{ID: "b5", Preferences: []string{"A", "C"}}, {ID: "b6", Preferences: []string{"A", "C"}},
		{ID: "b7", Preferences: []string{"B"}}, {ID: "b8", Preferences: []string{"B"}}, {ID: "b9", Preferences: []string{"C"}}, {ID: "b10", Preferences: []string{"D", "B"}},
	}}
	outcome, err := Run(context.Background(), input, nil)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Result == nil || len(outcome.Result.Winners) != 2 || outcome.Result.Winners[0] != "A" || outcome.Result.Winners[1] != "B" {
		t.Fatalf("outcome = %#v", outcome)
	}
}

func TestRunRequestsDecisionForUnresolvedExclusionTie(t *testing.T) {
	input := Input{SchemaVersion: 1, Rule: RuleIrishGuidedSTV, Options: []string{"A", "B"}, Places: 1, Ballots: []Ballot{
		{ID: "a1", Preferences: []string{"A", "B"}},
		{ID: "a2", Preferences: []string{"A", "B"}},
		{ID: "b1", Preferences: []string{"B", "A"}},
		{ID: "b2", Preferences: []string{"B", "A"}},
	}}

	outcome, err := Run(context.Background(), input, nil)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Result != nil || outcome.DecisionRequest == nil {
		t.Fatalf("outcome = %#v, want exclusion decision request without a result", outcome)
	}
}

func ballots(preferences ...string) []Ballot {
	result := make([]Ballot, 0, len(preferences))
	for index, preference := range preferences {
		result = append(result, Ballot{ID: string(rune('a' + index)), Preferences: []string{preference}})
	}
	return result
}
