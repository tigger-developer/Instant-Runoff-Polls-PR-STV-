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

type allocation struct {
	ballot Ballot
	option string
}

func Run(ctx context.Context, input Input, decisions []string) (Outcome, error) {
	if err := ctx.Err(); err != nil {
		return Outcome{}, err
	}
	if err := validate(input); err != nil {
		return Outcome{}, err
	}
	quota := len(input.Ballots)/(input.Places+1) + 1
	allocations := make([]allocation, len(input.Ballots))
	for index, ballot := range input.Ballots {
		allocations[index] = allocation{ballot: ballot, option: ballot.Preferences[0]}
	}
	continuing := make(map[string]bool, len(input.Options))
	for _, option := range input.Options {
		continuing[option] = true
	}
	winners := []string{}
	processed := map[string]bool{}
	for steps := 0; steps < 100; steps++ {
		if err := ctx.Err(); err != nil {
			return Outcome{}, err
		}
		tallies := tally(input.Options, allocations)
		for _, option := range input.Options {
			if continuing[option] && tallies[option] >= quota {
				continuing[option] = false
				winners = append(winners, option)
				if len(winners) == input.Places {
					return Outcome{Result: &Result{Quota: quota, Winners: winners}}, nil
				}
			}
		}
		if len(continuing) == input.Places-len(winners) {
			for _, option := range input.Options {
				if continuing[option] {
					winners = append(winners, option)
				}
			}
			return Outcome{Result: &Result{Quota: quota, Winners: winners}}, nil
		}
		selected := ""
		for _, winner := range winners {
			if !processed[winner] && tallies[winner] > quota {
				selected = winner
				break
			}
		}
		if selected != "" {
			transferSurplus(selected, tallies[selected]-quota, allocations, continuing)
			processed[selected] = true
			continue
		}
		lowest := ""
		for _, option := range input.Options {
			if continuing[option] && (lowest == "" || tallies[option] < tallies[lowest]) {
				lowest = option
			}
		}
		if lowest == "" {
			return Outcome{}, errors.New("count made no progress")
		}
		continuing[lowest] = false
		for index := range allocations {
			if allocations[index].option == lowest {
				allocations[index].option = nextPreference(allocations[index].ballot, continuing)
			}
		}
	}
	return Outcome{}, errors.New("count exceeded progress bound")
}
func tally(options []string, allocations []allocation) map[string]int {
	totals := map[string]int{}
	for _, o := range options {
		totals[o] = 0
	}
	for _, a := range allocations {
		if a.option != "" {
			totals[a.option]++
		}
	}
	return totals
}
func nextPreference(ballot Ballot, continuing map[string]bool) string {
	for _, p := range ballot.Preferences {
		if continuing[p] {
			return p
		}
	}
	return ""
}
func transferSurplus(winner string, surplus int, allocations []allocation, continuing map[string]bool) {
	groups := map[string][]int{}
	order := []string{}
	for i, a := range allocations {
		if a.option == winner {
			d := nextPreference(a.ballot, continuing)
			if d != "" {
				if _, ok := groups[d]; !ok {
					order = append(order, d)
				}
				groups[d] = append(groups[d], i)
			}
		}
	}
	total := 0
	for _, ids := range groups {
		total += len(ids)
	}
	if total == 0 {
		return
	}
	remaining := surplus
	for _, d := range order {
		count := surplus * len(groups[d]) / total
		if count > remaining {
			count = remaining
		}
		for _, i := range groups[d][:count] {
			allocations[i].option = d
		}
		remaining -= count
	}
	for _, d := range order {
		for _, i := range groups[d] {
			if remaining == 0 {
				return
			}
			if allocations[i].option == winner {
				allocations[i].option = d
				remaining--
			}
		}
	}
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
	for _, o := range input.Options {
		if o == "" || options[o] {
			return errors.New("duplicate or empty option identifier")
		}
		options[o] = true
	}
	ballots := map[string]bool{}
	for _, b := range input.Ballots {
		if b.ID == "" || ballots[b.ID] || len(b.Preferences) == 0 {
			return errors.New("invalid ballot identifier or preferences")
		}
		ballots[b.ID] = true
		seen := map[string]bool{}
		for _, p := range b.Preferences {
			if !options[p] || seen[p] {
				return fmt.Errorf("invalid ballot preference")
			}
			seen[p] = true
		}
	}
	return nil
}
