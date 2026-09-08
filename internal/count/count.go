// ABOUTME: Implements the approved pure Irish-guided PR-STV count transition.
// ABOUTME: It accepts only opaque count input and returns reproducible outcomes.
package count

import (
	"context"
	"errors"
	"fmt"
)

const RuleIrishGuidedSTV = "irish-guided-stv-v1"

type Ballot struct {
	ID          string   `json:"id"`
	Preferences []string `json:"preferences"`
}

type Input struct {
	SchemaVersion int      `json:"schema_version"`
	Rule          string   `json:"rule"`
	Options       []string `json:"options"`
	Places        int      `json:"places"`
	Ballots       []Ballot `json:"ballots"`
}

type Result struct {
	Quota   int      `json:"quota"`
	Winners []string `json:"winners"`
}

type DecisionRequest struct{}
type Outcome struct {
	Result          *Result
	DecisionRequest *DecisionRequest
}

func Run(ctx context.Context, input Input, decisions []string) (Outcome, error) {
	if err := ctx.Err(); err != nil {
		return Outcome{}, err
	}
	if err := validate(input); err != nil {
		return Outcome{}, err
	}
	quota := len(input.Ballots)/(input.Places+1) + 1
	tallies := make(map[string]int, len(input.Options))
	for _, ballot := range input.Ballots {
		tallies[ballot.Preferences[0]]++
	}
	winners := make([]string, 0, input.Places)
	for _, option := range input.Options {
		if tallies[option] >= quota {
			winners = append(winners, option)
			if len(winners) == input.Places {
				return Outcome{Result: &Result{Quota: quota, Winners: winners}}, nil
			}
		}
	}
	return Outcome{}, errors.New("count transitions beyond first allocation are not implemented")
}

func validate(input Input) error {
	if input.SchemaVersion != 1 || input.Rule != RuleIrishGuidedSTV {
		return errors.New("unsupported count schema or rule")
	}
	if len(input.Options) < 1 || len(input.Options) > 50 || input.Places < 1 || input.Places > len(input.Options) {
		return errors.New("invalid options or places")
	}
	if len(input.Ballots) < 1 || len(input.Ballots) > 1000 {
		return errors.New("invalid ballot count")
	}
	options := map[string]bool{}
	for _, option := range input.Options {
		if option == "" || options[option] {
			return errors.New("duplicate or empty option identifier")
		}
		options[option] = true
	}
	ballots := map[string]bool{}
	for _, ballot := range input.Ballots {
		if ballot.ID == "" || ballots[ballot.ID] || len(ballot.Preferences) == 0 {
			return errors.New("invalid ballot identifier or preferences")
		}
		ballots[ballot.ID] = true
		seen := map[string]bool{}
		for _, preference := range ballot.Preferences {
			if !options[preference] || seen[preference] {
				return fmt.Errorf("invalid ballot preference")
			}
			seen[preference] = true
		}
	}
	return nil
}
