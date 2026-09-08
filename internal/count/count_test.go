package count

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"
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
	if !recordContains(outcome.Result.Counts, func(record CountRecord) bool {
		return len(record.ElectedOptionIDs) > 0 && record.ElectedOptionIDs[0] == "A"
	}) {
		t.Fatalf("counts = %#v, want A election event", outcome.Result.Counts)
	}
	if !recordContains(outcome.Result.Counts, func(record CountRecord) bool {
		return record.SurplusOptionID == "A" && len(record.Transfers) > 0
	}) {
		t.Fatalf("counts = %#v, want A surplus movements", outcome.Result.Counts)
	}
	if !recordContains(outcome.Result.Counts, func(record CountRecord) bool {
		return record.SurplusOptionID == "A" && reflect.DeepEqual(record.Apportionments, []Apportionment{
			{DestinationOptionID: "B", ParcelBallots: 4, Quotient: 1, Remainder: 2, SelectedBallotIDs: []string{"b1"}},
			{DestinationOptionID: "C", ParcelBallots: 2, Quotient: 0, Remainder: 4, SelectedBallotIDs: []string{"b5"}},
		})
	}) {
		t.Fatalf("counts = %#v, want exact surplus apportionment", outcome.Result.Counts)
	}
	if !recordContains(outcome.Result.Counts, func(record CountRecord) bool {
		return len(record.ExcludedOptionIDs) > 0 && record.ExcludedOptionIDs[0] == "D"
	}) {
		t.Fatalf("counts = %#v, want D exclusion event", outcome.Result.Counts)
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
	if outcome.DecisionRequest.RequestFingerprint != "8ed98c9fad2b9998df68ff94221d2cda234beee5d2df69f2429808e4e251513e" {
		t.Fatalf("request fingerprint = %q", outcome.DecisionRequest.RequestFingerprint)
	}
}

func TestRunAppliesExclusionDecisionAndReplaysWinner(t *testing.T) {
	input := Input{SchemaVersion: 1, Rule: RuleIrishGuidedSTV, Options: []string{"A", "B"}, Places: 1, Ballots: []Ballot{
		{ID: "a1", Preferences: []string{"A", "B"}},
		{ID: "a2", Preferences: []string{"A", "B"}},
		{ID: "b1", Preferences: []string{"B", "A"}},
		{ID: "b2", Preferences: []string{"B", "A"}},
	}}

	request, err := Run(context.Background(), input, nil)
	if err != nil || request.DecisionRequest == nil {
		t.Fatalf("request outcome = %#v, error = %v", request, err)
	}
	decision := Decision{Sequence: request.DecisionRequest.Sequence, Kind: request.DecisionRequest.Kind, RequestFingerprint: request.DecisionRequest.RequestFingerprint, SelectedOptionIDs: []string{"B"}}
	outcome, err := Run(context.Background(), input, []Decision{decision})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Result == nil || len(outcome.Result.Winners) != 1 || outcome.Result.Winners[0] != "A" {
		t.Fatalf("outcome = %#v, want A to win after excluding B", outcome)
	}
}

func TestRunUsesOnlyElectionParcelForTransferredVoteSurplus(t *testing.T) {
	input := Input{SchemaVersion: 1, Rule: RuleIrishGuidedSTV, Options: []string{"A", "B", "C", "D"}, Places: 2, Ballots: []Ballot{
		{ID: "b1", Preferences: []string{"A", "B"}}, {ID: "b2", Preferences: []string{"A", "B"}},
		{ID: "b3", Preferences: []string{"A", "B"}}, {ID: "b4", Preferences: []string{"A", "B"}},
		{ID: "b5", Preferences: []string{"B"}}, {ID: "b6", Preferences: []string{"B"}}, {ID: "b7", Preferences: []string{"B"}},
		{ID: "b8", Preferences: []string{"C"}}, {ID: "b9", Preferences: []string{"C"}}, {ID: "b10", Preferences: []string{"C"}},
		{ID: "b11", Preferences: []string{"D", "A", "B"}}, {ID: "b12", Preferences: []string{"D", "A", "C"}},
	}}

	request, err := Run(context.Background(), input, nil)
	if err != nil || request.DecisionRequest == nil {
		t.Fatalf("request outcome = %#v, error = %v", request, err)
	}
	decision := Decision{Sequence: request.DecisionRequest.Sequence, Kind: request.DecisionRequest.Kind, RequestFingerprint: request.DecisionRequest.RequestFingerprint, SelectedOptionIDs: []string{"B"}}
	outcome, err := Run(context.Background(), input, []Decision{decision})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Result == nil || len(outcome.Result.Winners) != 2 || outcome.Result.Winners[0] != "A" || outcome.Result.Winners[1] != "B" {
		t.Fatalf("outcome = %#v, want A and B from the recorded remainder decision", outcome)
	}
}

func TestRunReturnsVersionedReproducibleRecord(t *testing.T) {
	input := Input{SchemaVersion: 1, Rule: RuleIrishGuidedSTV, Options: []string{"A", "B", "C", "D"}, Places: 2, Ballots: []Ballot{
		{ID: "b1", Preferences: []string{"A", "B"}}, {ID: "b2", Preferences: []string{"A", "B"}},
		{ID: "b3", Preferences: []string{"A", "B"}}, {ID: "b4", Preferences: []string{"A", "B"}},
		{ID: "b5", Preferences: []string{"A", "C"}}, {ID: "b6", Preferences: []string{"A", "C"}},
		{ID: "b7", Preferences: []string{"B"}}, {ID: "b8", Preferences: []string{"B"}},
		{ID: "b9", Preferences: []string{"C"}}, {ID: "b10", Preferences: []string{"D", "B"}},
	}}

	first, err := Run(context.Background(), input, nil)
	if err != nil || first.Result == nil {
		t.Fatalf("first outcome = %#v, error = %v", first, err)
	}
	second, err := Run(context.Background(), input, nil)
	if err != nil || second.Result == nil {
		t.Fatalf("second outcome = %#v, error = %v", second, err)
	}
	if first.Result.SchemaVersion != 1 || first.Result.Rule != RuleIrishGuidedSTV || len(first.Result.InputFingerprint) != 64 {
		t.Fatalf("result provenance = %#v", first.Result)
	}
	if len(first.Result.Counts) < 2 || len(first.Result.Counts[0].Totals) != len(input.Options) {
		t.Fatalf("count record = %#v", first.Result.Counts)
	}
	if first.Result.InputFingerprint != second.Result.InputFingerprint || len(first.Result.Counts) != len(second.Result.Counts) {
		t.Fatalf("replay differs: first=%#v second=%#v", first.Result, second.Result)
	}
}

func TestRunRejectsIdentifierOutsideContract(t *testing.T) {
	input := Input{SchemaVersion: 1, Rule: RuleIrishGuidedSTV, Options: []string{"not allowed"}, Places: 1, Ballots: []Ballot{{ID: "b1", Preferences: []string{"not allowed"}}}}
	if _, err := Run(context.Background(), input, nil); err == nil {
		t.Fatal("Run accepted an identifier containing spaces")
	}
}

func TestRunUsesHistoricalLowBeforeRequestingExclusionLot(t *testing.T) {
	input := Input{SchemaVersion: 1, Rule: RuleIrishGuidedSTV, Options: []string{"A", "B", "C"}, Places: 1, Ballots: []Ballot{
		{ID: "b1", Preferences: []string{"A"}}, {ID: "b2", Preferences: []string{"A"}}, {ID: "b3", Preferences: []string{"A"}},
		{ID: "b4", Preferences: []string{"B", "A"}}, {ID: "b5", Preferences: []string{"B", "A"}},
		{ID: "b6", Preferences: []string{"C", "B"}},
	}}
	outcome, err := Run(context.Background(), input, nil)
	if err != nil || outcome.Result == nil || outcome.Result.Winners[0] != "A" {
		t.Fatalf("outcome = %#v, error = %v, want historical comparison to elect A", outcome, err)
	}
}

func TestRunBulkExcludesStrictlyLowerCombinedSet(t *testing.T) {
	preferences := make([]string, 0, 60)
	for range 20 {
		preferences = append(preferences, "A")
	}
	for range 20 {
		preferences = append(preferences, "B")
	}
	for range 18 {
		preferences = append(preferences, "C")
	}
	preferences = append(preferences, "D", "E")
	inputBallots := make([]Ballot, 0, len(preferences))
	for index, preference := range preferences {
		inputBallots = append(inputBallots, Ballot{ID: fmt.Sprintf("b%d", index+1), Preferences: []string{preference}})
	}
	input := Input{SchemaVersion: 1, Rule: RuleIrishGuidedSTV, Options: []string{"A", "B", "C", "D", "E"}, Places: 2, Ballots: inputBallots}
	input.Ballots[58].Preferences = []string{"D", "A"}
	input.Ballots[59].Preferences = []string{"E", "B"}
	outcome, err := Run(context.Background(), input, nil)
	if err != nil || outcome.Result == nil || len(outcome.Result.Winners) != 2 || outcome.Result.Winners[0] != "A" || outcome.Result.Winners[1] != "B" {
		t.Fatalf("outcome = %#v, error = %v, want A and B after bulk exclusion", outcome, err)
	}
}

func TestRunRequestsLotForEqualOutcomeSensitiveSurpluses(t *testing.T) {
	input := equalSurplusInput()
	outcome, err := Run(context.Background(), input, nil)
	if err != nil || outcome.DecisionRequest == nil || outcome.DecisionRequest.Kind != "surplus_order_lot" {
		t.Fatalf("outcome = %#v, error = %v, want surplus-order decision", outcome, err)
	}
}

func TestRunReplaysBothEqualSurplusLotOutcomes(t *testing.T) {
	for _, testCase := range []struct {
		firstChoice string
		thirdWinner string
	}{
		{firstChoice: "A", thirdWinner: "C"},
		{firstChoice: "B", thirdWinner: "D"},
	} {
		t.Run(testCase.firstChoice, func(t *testing.T) {
			outcome := runChoosing(t, equalSurplusInput(), testCase.firstChoice)
			if outcome.Result == nil || !contains(outcome.Result.Winners, testCase.thirdWinner) {
				t.Fatalf("outcome = %#v, want third winner %s", outcome, testCase.thirdWinner)
			}
		})
	}
}

func equalSurplusInput() Input {
	return Input{SchemaVersion: 1, Rule: RuleIrishGuidedSTV, Options: []string{"A", "B", "C", "D"}, Places: 3, Ballots: []Ballot{
		{ID: "a1", Preferences: []string{"A", "C"}}, {ID: "a2", Preferences: []string{"A", "C"}}, {ID: "a3", Preferences: []string{"A", "C"}}, {ID: "a4", Preferences: []string{"A", "C"}}, {ID: "a5", Preferences: []string{"A", "C"}},
		{ID: "b1", Preferences: []string{"B", "D"}}, {ID: "b2", Preferences: []string{"B", "D"}}, {ID: "b3", Preferences: []string{"B", "D"}}, {ID: "b4", Preferences: []string{"B", "D"}}, {ID: "b5", Preferences: []string{"B", "D"}},
		{ID: "c1", Preferences: []string{"C"}}, {ID: "d1", Preferences: []string{"D"}},
	}}
}

func TestRunReturnsWinnersInDeclaredOptionOrder(t *testing.T) {
	input := Input{SchemaVersion: 1, Rule: RuleIrishGuidedSTV, Options: []string{"A", "B", "C"}, Places: 2, Ballots: []Ballot{
		{ID: "b1", Preferences: []string{"B"}}, {ID: "b2", Preferences: []string{"B"}}, {ID: "b3", Preferences: []string{"B"}}, {ID: "b4", Preferences: []string{"B"}}, {ID: "b5", Preferences: []string{"B"}},
		{ID: "a1", Preferences: []string{"A"}}, {ID: "a2", Preferences: []string{"A"}}, {ID: "a3", Preferences: []string{"A"}},
		{ID: "c1", Preferences: []string{"C", "A"}}, {ID: "c2", Preferences: []string{"C", "A"}},
	}}
	outcome, err := Run(context.Background(), input, nil)
	if err != nil || outcome.Result == nil {
		t.Fatalf("outcome = %#v, error = %v", outcome, err)
	}
	if len(outcome.Result.Winners) != 2 || outcome.Result.Winners[0] != "A" || outcome.Result.Winners[1] != "B" {
		t.Fatalf("winners = %#v, want declared order [A B]", outcome.Result.Winners)
	}
}

func TestRunUsesHistoricalHighForRemainderTie(t *testing.T) {
	input := Input{SchemaVersion: 1, Rule: RuleIrishGuidedSTV, Options: []string{"A", "B", "C", "D"}, Places: 2, Ballots: []Ballot{
		{ID: "a1", Preferences: []string{"A"}}, {ID: "a2", Preferences: []string{"A"}}, {ID: "a3", Preferences: []string{"A"}}, {ID: "a4", Preferences: []string{"A"}},
		{ID: "b1", Preferences: []string{"B"}}, {ID: "b2", Preferences: []string{"B"}}, {ID: "b3", Preferences: []string{"B"}}, {ID: "b4", Preferences: []string{"B"}},
		{ID: "c1", Preferences: []string{"C"}}, {ID: "c2", Preferences: []string{"C"}}, {ID: "c3", Preferences: []string{"C"}},
		{ID: "d1", Preferences: []string{"D", "A", "B"}}, {ID: "d2", Preferences: []string{"D", "A", "C"}},
	}}
	outcome, err := Run(context.Background(), input, nil)
	if err != nil || outcome.Result == nil {
		t.Fatalf("outcome = %#v, error = %v, want history to resolve remainder", outcome, err)
	}
	if len(outcome.Result.Winners) != 2 || outcome.Result.Winners[0] != "A" || outcome.Result.Winners[1] != "B" {
		t.Fatalf("winners = %#v, want [A B]", outcome.Result.Winners)
	}
}

func TestRunRequestsSequentialLotsAcrossRemainderBoundary(t *testing.T) {
	input := Input{SchemaVersion: 1, Rule: RuleIrishGuidedSTV, Options: []string{"A", "B", "C", "D"}, Places: 3, Ballots: expandBallots([]ballotGroup{
		{2, []string{"A", "B"}}, {2, []string{"A", "C"}}, {2, []string{"A", "D"}}, {2, []string{"A"}},
		{4, []string{"B"}}, {4, []string{"C"}}, {4, []string{"D"}},
	})}
	first, err := Run(context.Background(), input, nil)
	if err != nil || first.DecisionRequest == nil || first.DecisionRequest.Kind != "remainder_lot" || !reflect.DeepEqual(first.DecisionRequest.EligibleOptionIDs, []string{"B", "C", "D"}) {
		t.Fatalf("first outcome = %#v, error = %v", first, err)
	}
	decision := Decision{Sequence: first.DecisionRequest.Sequence, Kind: first.DecisionRequest.Kind, RequestFingerprint: first.DecisionRequest.RequestFingerprint, SelectedOptionIDs: []string{"B"}}
	second, err := Run(context.Background(), input, []Decision{decision})
	if err != nil || second.DecisionRequest == nil || second.DecisionRequest.Sequence != 2 || !reflect.DeepEqual(second.DecisionRequest.EligibleOptionIDs, []string{"C", "D"}) {
		t.Fatalf("second outcome = %#v, error = %v", second, err)
	}
}

func TestRunSurplusTransferBoundaries(t *testing.T) {
	tests := []struct {
		name    string
		ballots []Ballot
		want    []string
	}{
		{name: "no usable surplus", ballots: expandBallots([]ballotGroup{{6, []string{"A"}}, {3, []string{"B"}}, {1, []string{"C", "B"}}}), want: []string{"A", "B"}},
		{name: "transferable parcel below surplus", ballots: expandBallots([]ballotGroup{{5, []string{"A"}}, {1, []string{"A", "B"}}, {3, []string{"B"}}, {1, []string{"C"}}}), want: []string{"A", "B"}},
		{name: "transferable parcel equals surplus", ballots: expandBallots([]ballotGroup{{4, []string{"A"}}, {2, []string{"A", "B"}}, {3, []string{"B"}}, {1, []string{"C"}}}), want: []string{"A", "B"}},
		{name: "equal remainders need no lot when no increment remains", ballots: expandBallots([]ballotGroup{{3, []string{"A", "B"}}, {3, []string{"A", "C"}}, {3, []string{"B"}}, {1, []string{"C"}}}), want: []string{"A", "B"}},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			input := Input{SchemaVersion: 1, Rule: RuleIrishGuidedSTV, Options: []string{"A", "B", "C"}, Places: 2, Ballots: testCase.ballots}
			outcome, err := Run(context.Background(), input, nil)
			if err != nil || outcome.Result == nil || !reflect.DeepEqual(outcome.Result.Winners, testCase.want) {
				t.Fatalf("outcome = %#v, error = %v, want winners %v", outcome, err, testCase.want)
			}
		})
	}
}

func TestRunFillsLastVacanciesIncludingZeroTallies(t *testing.T) {
	input := Input{SchemaVersion: 1, Rule: RuleIrishGuidedSTV, Options: []string{"A", "B", "C"}, Places: 3, Ballots: []Ballot{{ID: "b1", Preferences: []string{"A"}}}}
	outcome, err := Run(context.Background(), input, nil)
	if err != nil || outcome.Result == nil || !reflect.DeepEqual(outcome.Result.Winners, []string{"A", "B", "C"}) {
		t.Fatalf("outcome = %#v, error = %v", outcome, err)
	}
	if !reflect.DeepEqual(outcome.Result.Counts[len(outcome.Result.Counts)-1].ElectedOptionIDs, []string{"A", "B", "C"}) {
		t.Fatalf("final election record = %#v", outcome.Result.Counts[len(outcome.Result.Counts)-1])
	}
}

func TestRunDoesNotBulkExcludeAtEqualBoundary(t *testing.T) {
	input := Input{SchemaVersion: 1, Rule: RuleIrishGuidedSTV, Options: []string{"A", "B", "C", "D", "E"}, Places: 2, Ballots: expandBallots([]ballotGroup{{3, []string{"A"}}, {3, []string{"B"}}, {2, []string{"C"}}, {1, []string{"D", "A"}}, {1, []string{"E", "B"}}})}
	outcome, err := Run(context.Background(), input, nil)
	if err != nil || outcome.DecisionRequest == nil || outcome.DecisionRequest.Kind != "exclusion_lot" || !reflect.DeepEqual(outcome.DecisionRequest.EligibleOptionIDs, []string{"D", "E"}) {
		t.Fatalf("outcome = %#v, error = %v, want D/E exclusion lot", outcome, err)
	}
}

func TestRunRejectsInvalidBoundariesWithoutMutatingInput(t *testing.T) {
	valid := Input{SchemaVersion: 1, Rule: RuleIrishGuidedSTV, Options: []string{"A", "B"}, Places: 1, Ballots: []Ballot{{ID: "b1", Preferences: []string{"A", "B"}}}}
	cases := []struct {
		name  string
		input Input
	}{
		{name: "unsupported schema", input: func() Input { value := valid; value.SchemaVersion = 2; return value }()},
		{name: "unsupported rule", input: func() Input { value := valid; value.Rule = "other"; return value }()},
		{name: "zero places", input: func() Input { value := valid; value.Places = 0; return value }()},
		{name: "too many places", input: func() Input { value := valid; value.Places = 3; return value }()},
		{name: "zero ballots", input: func() Input { value := valid; value.Ballots = nil; return value }()},
		{name: "invalid option identifier", input: func() Input { value := valid; value.Options = []string{"A space", "B"}; return value }()},
		{name: "invalid ballot identifier", input: func() Input {
			value := valid
			value.Ballots = []Ballot{{ID: "", Preferences: []string{"A"}}}
			return value
		}()},
		{name: "duplicate ballot identifier", input: func() Input {
			value := valid
			value.Ballots = []Ballot{{ID: "b1", Preferences: []string{"A"}}, {ID: "b1", Preferences: []string{"B"}}}
			return value
		}()},
		{name: "empty ballot", input: func() Input {
			value := valid
			value.Ballots = []Ballot{{ID: "b1"}}
			return value
		}()},
		{name: "unknown preference", input: func() Input {
			value := valid
			value.Ballots = []Ballot{{ID: "b1", Preferences: []string{"C"}}}
			return value
		}()},
		{name: "repeated preference", input: func() Input {
			value := valid
			value.Ballots = []Ballot{{ID: "b1", Preferences: []string{"A", "A"}}}
			return value
		}()},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			before := cloneInput(testCase.input)
			outcome, err := Run(context.Background(), testCase.input, nil)
			if !errors.Is(err, ErrInvalidInput) || outcome.Result != nil || outcome.DecisionRequest != nil {
				t.Fatalf("outcome = %#v, error = %v, want ErrInvalidInput", outcome, err)
			}
			if !reflect.DeepEqual(testCase.input, before) {
				t.Fatalf("input mutated: got %#v want %#v", testCase.input, before)
			}
		})
	}
}

func TestRunRejectsCancellationAndUnusedDecision(t *testing.T) {
	input := Input{SchemaVersion: 1, Rule: RuleIrishGuidedSTV, Options: []string{"A", "B"}, Places: 1, Ballots: []Ballot{{ID: "b1", Preferences: []string{"A"}}}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if outcome, err := Run(ctx, input, nil); !errors.Is(err, context.Canceled) || outcome.Result != nil {
		t.Fatalf("cancelled outcome = %#v, error = %v", outcome, err)
	}
	unused := Decision{Sequence: 1, Kind: "exclusion_lot", RequestFingerprint: "unused", SelectedOptionIDs: []string{"B"}}
	if outcome, err := Run(context.Background(), input, []Decision{unused}); !errors.Is(err, ErrInvalidDecision) || outcome.Result != nil {
		t.Fatalf("unused-decision outcome = %#v, error = %v", outcome, err)
	}
}

func TestRunConservesBallotsAndSurvivesJSONReplay(t *testing.T) {
	input := equalSurplusInput()
	first := runChoosing(t, input, "A")
	encodedInput, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	encodedDecisions, err := json.Marshal(first.Result.UsedDecisions)
	if err != nil {
		t.Fatal(err)
	}
	var replayInput Input
	var replayDecisions []Decision
	if err := json.Unmarshal(encodedInput, &replayInput); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encodedDecisions, &replayDecisions); err != nil {
		t.Fatal(err)
	}
	replayed, err := Run(context.Background(), replayInput, replayDecisions)
	if err != nil || replayed.Result == nil || !reflect.DeepEqual(first.Result, replayed.Result) {
		t.Fatalf("replayed = %#v, error = %v, want %#v", replayed.Result, err, first.Result)
	}
	for _, record := range replayed.Result.Counts {
		total := record.Exhausted
		for _, option := range record.Totals {
			total += option.Votes
		}
		if total != len(input.Ballots) {
			t.Fatalf("count %d accounts for %d ballots, want %d", record.Index, total, len(input.Ballots))
		}
	}
}

func TestRunSupportsBoundsAndCancellationDuringCount(t *testing.T) {
	options := make([]string, 50)
	for index := range options {
		options[index] = fmt.Sprintf("o%d", index+1)
	}
	maxBallots := make([]Ballot, 1000)
	for index := range maxBallots {
		maxBallots[index] = Ballot{ID: fmt.Sprintf("b%d", index+1), Preferences: []string{options[index%len(options)]}}
	}
	input := Input{SchemaVersion: 1, Rule: RuleIrishGuidedSTV, Options: options, Places: 25, Ballots: maxBallots}
	if outcome, err := Run(context.Background(), input, nil); err != nil || (outcome.Result == nil && outcome.DecisionRequest == nil) {
		t.Fatalf("maximum input outcome = %#v, error = %v", outcome, err)
	}
	overLimit := cloneInput(input)
	overLimit.Ballots = append(overLimit.Ballots, Ballot{ID: "b1001", Preferences: []string{"o1"}})
	if _, err := Run(context.Background(), overLimit, nil); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("over-limit error = %v", err)
	}
	cancelContext := &cancelAfterChecksContext{remaining: 2}
	if outcome, err := Run(cancelContext, Input{SchemaVersion: 1, Rule: RuleIrishGuidedSTV, Options: []string{"A", "B", "C"}, Places: 1, Ballots: expandBallots([]ballotGroup{{3, []string{"A"}}, {2, []string{"B", "A"}}, {1, []string{"C", "B"}}})}, nil); !errors.Is(err, context.Canceled) || outcome.Result != nil {
		t.Fatalf("mid-count cancellation outcome = %#v, error = %v", outcome, err)
	}
}

func ballots(preferences ...string) []Ballot {
	result := make([]Ballot, 0, len(preferences))
	for index, preference := range preferences {
		result = append(result, Ballot{ID: string(rune('a' + index)), Preferences: []string{preference}})
	}
	return result
}

func recordContains(records []CountRecord, predicate func(CountRecord) bool) bool {
	for _, record := range records {
		if predicate(record) {
			return true
		}
	}
	return false
}

func cloneInput(input Input) Input {
	cloned := input
	if input.Options != nil {
		cloned.Options = append([]string(nil), input.Options...)
	}
	if input.Ballots != nil {
		cloned.Ballots = make([]Ballot, len(input.Ballots))
	}
	for index, ballot := range input.Ballots {
		cloned.Ballots[index] = Ballot{ID: ballot.ID, Preferences: append([]string(nil), ballot.Preferences...)}
	}
	return cloned
}

type ballotGroup struct {
	count       int
	preferences []string
}

func expandBallots(groups []ballotGroup) []Ballot {
	result := []Ballot{}
	for _, group := range groups {
		for range group.count {
			result = append(result, Ballot{ID: fmt.Sprintf("b%d", len(result)+1), Preferences: append([]string(nil), group.preferences...)})
		}
	}
	return result
}

func runChoosing(t *testing.T, input Input, firstChoice string) Outcome {
	t.Helper()
	decisions := []Decision{}
	for {
		outcome, err := Run(context.Background(), input, decisions)
		if err != nil {
			t.Fatal(err)
		}
		if outcome.Result != nil {
			return outcome
		}
		request := outcome.DecisionRequest
		if request == nil {
			t.Fatalf("outcome = %#v", outcome)
		}
		choice := request.EligibleOptionIDs[0]
		if len(decisions) == 0 && contains(request.EligibleOptionIDs, firstChoice) {
			choice = firstChoice
		}
		decisions = append(decisions, Decision{Sequence: request.Sequence, Kind: request.Kind, RequestFingerprint: request.RequestFingerprint, SelectedOptionIDs: []string{choice}})
	}
}

type cancelAfterChecksContext struct {
	remaining int
}

func (ctx *cancelAfterChecksContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (ctx *cancelAfterChecksContext) Done() <-chan struct{}       { return nil }
func (ctx *cancelAfterChecksContext) Value(any) any               { return nil }
func (ctx *cancelAfterChecksContext) Err() error {
	ctx.remaining--
	if ctx.remaining < 0 {
		return context.Canceled
	}
	return nil
}
