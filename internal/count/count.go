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
type DecisionRequest struct {
	Sequence           int
	Kind               string
	EligibleOptionIDs  []string
	RequiredSelections int
}
type Outcome struct {
	Result          *Result
	DecisionRequest *DecisionRequest
}

type allocation struct {
	ballot Ballot
	option string
	parcel int
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
	electionParcel := map[string]int{}
	decisionIndex := 0
	operation := 0
	for steps := 0; steps < 100; steps++ {
		if err := ctx.Err(); err != nil {
			return Outcome{}, err
		}
		tallies := tally(input.Options, allocations)
		for _, option := range input.Options {
			if continuing[option] && tallies[option] >= quota {
				continuing[option] = false
				winners = append(winners, option)
				electionParcel[option] = operation
				if len(winners) == input.Places {
					if decisionIndex != len(decisions) {
						return Outcome{}, errors.New("unused count decision")
					}
					return Outcome{Result: &Result{Quota: quota, Winners: winners}}, nil
				}
			}
		}
		if continuingCount(continuing) == input.Places-len(winners) {
			for _, option := range input.Options {
				if continuing[option] {
					winners = append(winners, option)
				}
			}
			if decisionIndex != len(decisions) {
				return Outcome{}, errors.New("unused count decision")
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
			eligible := surplusRemainderTie(selected, tallies[selected]-quota, allocations, continuing, electionParcel[selected])
			choice := ""
			if len(eligible) > 0 {
				if decisionIndex == len(decisions) {
					return Outcome{DecisionRequest: &DecisionRequest{Sequence: decisionIndex + 1, Kind: "remainder_lot", EligibleOptionIDs: eligible, RequiredSelections: 1}}, nil
				}
				choice = decisions[decisionIndex]
				if !contains(eligible, choice) {
					return Outcome{}, errors.New("ineligible count decision")
				}
				decisionIndex++
			}
			operation++
			transferSurplus(selected, tallies[selected]-quota, allocations, continuing, electionParcel[selected], operation, choice)
			processed[selected] = true
			continue
		}
		lowest := ""
		lowestOptions := []string{}
		for _, option := range input.Options {
			if !continuing[option] {
				continue
			}
			if lowest == "" || tallies[option] < tallies[lowest] {
				lowest = option
				lowestOptions = []string{option}
				continue
			}
			if tallies[option] == tallies[lowest] {
				lowestOptions = append(lowestOptions, option)
			}
		}
		if lowest == "" {
			return Outcome{}, errors.New("count made no progress")
		}
		if len(lowestOptions) > 1 {
			if decisionIndex == len(decisions) {
				return Outcome{DecisionRequest: &DecisionRequest{
					Sequence: decisionIndex + 1, Kind: "exclusion_lot", EligibleOptionIDs: lowestOptions, RequiredSelections: 1,
				}}, nil
			}
			lowest = decisions[decisionIndex]
			if !contains(lowestOptions, lowest) {
				return Outcome{}, errors.New("ineligible count decision")
			}
			decisionIndex++
		}
		continuing[lowest] = false
		operation++
		for index := range allocations {
			if allocations[index].option == lowest {
				allocations[index].option = nextPreference(allocations[index].ballot, continuing)
				allocations[index].parcel = operation
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

func continuingCount(continuing map[string]bool) int {
	count := 0
	for _, active := range continuing {
		if active {
			count++
		}
	}
	return count
}
func nextPreference(ballot Ballot, continuing map[string]bool) string {
	for _, p := range ballot.Preferences {
		if continuing[p] {
			return p
		}
	}
	return ""
}
func transferSurplus(winner string, surplus int, allocations []allocation, continuing map[string]bool, sourceParcel, destinationParcel int, remainderChoice string) {
	groups := map[string][]int{}
	order := []string{}
	for i, a := range allocations {
		if a.option == winner && a.parcel == sourceParcel {
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
			allocations[i].parcel = destinationParcel
		}
		remaining -= count
	}
	if remainderChoice != "" && remaining > 0 {
		for _, i := range groups[remainderChoice] {
			if allocations[i].option == winner {
				allocations[i].option = remainderChoice
				allocations[i].parcel = destinationParcel
				remaining--
				break
			}
		}
	}
	for _, d := range order {
		for _, i := range groups[d] {
			if remaining == 0 {
				return
			}
			if allocations[i].option == winner {
				allocations[i].option = d
				allocations[i].parcel = destinationParcel
				remaining--
			}
		}
	}
}

func surplusRemainderTie(winner string, surplus int, allocations []allocation, continuing map[string]bool, sourceParcel int) []string {
	counts := map[string]int{}
	total := 0
	for _, allocation := range allocations {
		if allocation.option != winner || allocation.parcel != sourceParcel {
			continue
		}
		destination := nextPreference(allocation.ballot, continuing)
		if destination != "" {
			counts[destination]++
			total++
		}
	}
	if total == 0 || total <= surplus {
		return nil
	}
	allocated := 0
	for _, count := range counts {
		allocated += surplus * count / total
	}
	if surplus-allocated != 1 {
		return nil
	}
	maxRemainder := -1
	eligible := []string{}
	for option, count := range counts {
		remainder := surplus * count % total
		if remainder > maxRemainder {
			maxRemainder = remainder
			eligible = []string{option}
		} else if remainder == maxRemainder {
			eligible = append(eligible, option)
		}
	}
	if len(eligible) < 2 {
		return nil
	}
	return eligible
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
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
