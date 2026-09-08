// ABOUTME: Implements the approved pure Irish-guided PR-STV count transition.
// ABOUTME: It accepts only opaque count input and returns reproducible outcomes.
package count

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
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
	SchemaVersion    int           `json:"schema_version"`
	Rule             string        `json:"rule"`
	InputFingerprint string        `json:"input_fingerprint"`
	Quota            int           `json:"quota"`
	Winners          []string      `json:"winners"`
	Counts           []CountRecord `json:"counts"`
	UsedDecisions    []Decision    `json:"used_decisions"`
}
type OptionTotal struct {
	OptionID string `json:"option_id"`
	Votes    int    `json:"votes"`
}
type CountRecord struct {
	Index             int           `json:"index"`
	Totals            []OptionTotal `json:"totals"`
	Exhausted         int           `json:"exhausted"`
	ElectedOptionIDs  []string      `json:"elected_option_ids"`
	ExcludedOptionIDs []string      `json:"excluded_option_ids"`
	SurplusOptionID   string        `json:"surplus_option_id,omitempty"`
	Transfers         []Transfer    `json:"transfers"`
}
type Transfer struct {
	BallotID     string `json:"ballot_id"`
	FromOptionID string `json:"from_option_id"`
	ToOptionID   string `json:"to_option_id,omitempty"`
}
type Decision struct {
	Sequence           int      `json:"sequence"`
	Kind               string   `json:"kind"`
	RequestFingerprint string   `json:"request_fingerprint"`
	SelectedOptionIDs  []string `json:"selected_option_ids"`
}
type DecisionRequest struct {
	Sequence           int      `json:"sequence"`
	Kind               string   `json:"kind"`
	RequestFingerprint string   `json:"request_fingerprint"`
	EligibleOptionIDs  []string `json:"eligible_option_ids"`
	RequiredSelections int      `json:"required_selections"`
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

func Run(ctx context.Context, input Input, decisions []Decision) (Outcome, error) {
	if err := ctx.Err(); err != nil {
		return Outcome{}, err
	}
	if err := validate(input); err != nil {
		return Outcome{}, err
	}
	quota := len(input.Ballots)/(input.Places+1) + 1
	inputFingerprint, err := fingerprintInput(input)
	if err != nil {
		return Outcome{}, fmt.Errorf("fingerprinting count input: %w", err)
	}
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
	countRecords := []CountRecord{}
	pendingExcluded := []string(nil)
	pendingSurplus := ""
	pendingTransfers := []Transfer(nil)
	for steps := 0; steps < 100; steps++ {
		if err := ctx.Err(); err != nil {
			return Outcome{}, err
		}
		tallies := tally(input.Options, allocations)
		record := recordTotals(operation+1, input.Options, tallies, allocations)
		record.ExcludedOptionIDs = pendingExcluded
		record.SurplusOptionID = pendingSurplus
		record.Transfers = pendingTransfers
		countRecords = append(countRecords, record)
		pendingExcluded = nil
		pendingSurplus = ""
		pendingTransfers = nil
		for _, option := range input.Options {
			if continuing[option] && tallies[option] >= quota {
				continuing[option] = false
				winners = append(winners, option)
				countRecords[len(countRecords)-1].ElectedOptionIDs = append(countRecords[len(countRecords)-1].ElectedOptionIDs, option)
				electionParcel[option] = operation
				if len(winners) == input.Places {
					if decisionIndex != len(decisions) {
						return Outcome{}, errors.New("unused count decision")
					}
					return Outcome{Result: &Result{SchemaVersion: 1, Rule: input.Rule, InputFingerprint: inputFingerprint, Quota: quota, Winners: orderByOptions(input.Options, winners), Counts: countRecords, UsedDecisions: append([]Decision(nil), decisions...)}}, nil
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
			return Outcome{Result: &Result{SchemaVersion: 1, Rule: input.Rule, InputFingerprint: inputFingerprint, Quota: quota, Winners: orderByOptions(input.Options, winners), Counts: countRecords, UsedDecisions: append([]Decision(nil), decisions...)}}, nil
		}
		selected := ""
		pendingSurpluses := []string{}
		largestSurplus := 0
		for _, winner := range winners {
			if !processed[winner] && tallies[winner] > quota {
				surplus := tallies[winner] - quota
				if surplus > largestSurplus {
					largestSurplus = surplus
					pendingSurpluses = []string{winner}
				} else if surplus == largestSurplus {
					pendingSurpluses = append(pendingSurpluses, winner)
				}
			}
		}
		if len(pendingSurpluses) == 1 {
			selected = pendingSurpluses[0]
		} else if len(pendingSurpluses) > 1 {
			pendingSurpluses = historicalCandidates(pendingSurpluses, countRecords[:len(countRecords)-1], true)
			if len(pendingSurpluses) == 1 {
				selected = pendingSurpluses[0]
			} else {
				request, err := newDecisionRequest(inputFingerprint, decisionIndex+1, "surplus_order_lot", operation+1, orderByOptions(input.Options, pendingSurpluses))
				if err != nil {
					return Outcome{}, fmt.Errorf("fingerprinting surplus decision request: %w", err)
				}
				if decisionIndex == len(decisions) {
					return Outcome{DecisionRequest: &request}, nil
				}
				decision := decisions[decisionIndex]
				if err := validateDecision(decision, request); err != nil {
					return Outcome{}, err
				}
				selected = decision.SelectedOptionIDs[0]
				decisionIndex++
			}
		}
		if selected != "" {
			eligible, choice := surplusRemainderTie(input.Options, selected, tallies[selected]-quota, allocations, continuing, electionParcel[selected], countRecords[:len(countRecords)-1])
			if len(eligible) > 0 {
				request, err := newDecisionRequest(inputFingerprint, decisionIndex+1, "remainder_lot", operation+1, eligible)
				if err != nil {
					return Outcome{}, fmt.Errorf("fingerprinting remainder decision request: %w", err)
				}
				if decisionIndex == len(decisions) {
					return Outcome{DecisionRequest: &request}, nil
				}
				decision := decisions[decisionIndex]
				if err := validateDecision(decision, request); err != nil {
					return Outcome{}, errors.New("ineligible count decision")
				}
				choice = decision.SelectedOptionIDs[0]
				decisionIndex++
			}
			operation++
			pendingSurplus = selected
			pendingTransfers = transferSurplus(selected, tallies[selected]-quota, allocations, continuing, electionParcel[selected], operation, choice)
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
			lowestOptions = historicalCandidates(lowestOptions, countRecords[:len(countRecords)-1], false)
			if len(lowestOptions) == 1 {
				lowest = lowestOptions[0]
			}
		}
		exclusionSet := bulkExclusionSet(input.Options, continuing, tallies, input.Places-len(winners))
		if len(exclusionSet) == 0 && len(lowestOptions) > 1 {
			request, err := newDecisionRequest(inputFingerprint, decisionIndex+1, "exclusion_lot", operation+1, lowestOptions)
			if err != nil {
				return Outcome{}, fmt.Errorf("fingerprinting exclusion decision request: %w", err)
			}
			if decisionIndex == len(decisions) {
				return Outcome{DecisionRequest: &request}, nil
			}
			decision := decisions[decisionIndex]
			if err := validateDecision(decision, request); err != nil {
				return Outcome{}, err
			}
			lowest = decision.SelectedOptionIDs[0]
			decisionIndex++
		}
		if len(exclusionSet) == 0 {
			exclusionSet = []string{lowest}
		}
		for _, option := range exclusionSet {
			continuing[option] = false
		}
		operation++
		pendingExcluded = append([]string(nil), exclusionSet...)
		for index := range allocations {
			if contains(exclusionSet, allocations[index].option) {
				from := allocations[index].option
				to := nextPreference(allocations[index].ballot, continuing)
				allocations[index].option = to
				allocations[index].parcel = operation
				pendingTransfers = append(pendingTransfers, Transfer{BallotID: allocations[index].ballot.ID, FromOptionID: from, ToOptionID: to})
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
func transferSurplus(winner string, surplus int, allocations []allocation, continuing map[string]bool, sourceParcel, destinationParcel int, remainderChoice string) []Transfer {
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
		return nil
	}
	transfers := []Transfer{}
	moveCounts := map[string]int{}
	if total <= surplus {
		for destination, indexes := range groups {
			moveCounts[destination] = len(indexes)
		}
	} else {
		remaining := surplus
		for _, destination := range order {
			moveCounts[destination] = surplus * len(groups[destination]) / total
			remaining -= moveCounts[destination]
		}
		remainderOrder := append([]string(nil), order...)
		sort.SliceStable(remainderOrder, func(i, j int) bool {
			left := surplus * len(groups[remainderOrder[i]]) % total
			right := surplus * len(groups[remainderOrder[j]]) % total
			if left == right && remainderChoice != "" {
				return remainderOrder[i] == remainderChoice
			}
			return left > right
		})
		for _, destination := range remainderOrder[:remaining] {
			moveCounts[destination]++
		}
	}
	for _, destination := range order {
		for _, index := range groups[destination][:moveCounts[destination]] {
			allocations[index].option = destination
			allocations[index].parcel = destinationParcel
			transfers = append(transfers, Transfer{BallotID: allocations[index].ballot.ID, FromOptionID: winner, ToOptionID: destination})
		}
	}
	return transfers
}

func surplusRemainderTie(options []string, winner string, surplus int, allocations []allocation, continuing map[string]bool, sourceParcel int, history []CountRecord) ([]string, string) {
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
		return nil, ""
	}
	allocated := 0
	for _, count := range counts {
		allocated += surplus * count / total
	}
	if surplus-allocated != 1 {
		return nil, ""
	}
	maxRemainder := -1
	eligible := []string{}
	for _, option := range options {
		count, ok := counts[option]
		if !ok {
			continue
		}
		remainder := surplus * count % total
		if remainder > maxRemainder {
			maxRemainder = remainder
			eligible = []string{option}
		} else if remainder == maxRemainder {
			eligible = append(eligible, option)
		}
	}
	if len(eligible) < 2 {
		return nil, ""
	}
	eligible = historicalCandidates(eligible, history, true)
	if len(eligible) == 1 {
		return nil, eligible[0]
	}
	return eligible, ""
}

var validID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func historicalCandidates(candidates []string, history []CountRecord, high bool) []string {
	remaining := append([]string(nil), candidates...)
	for _, record := range history {
		valueByOption := map[string]int{}
		for _, total := range record.Totals {
			valueByOption[total.OptionID] = total.Votes
		}
		best := valueByOption[remaining[0]]
		for _, option := range remaining[1:] {
			value := valueByOption[option]
			if (high && value > best) || (!high && value < best) {
				best = value
			}
		}
		filtered := remaining[:0]
		for _, option := range remaining {
			if valueByOption[option] == best {
				filtered = append(filtered, option)
			}
		}
		remaining = filtered
		if len(remaining) == 1 {
			return remaining
		}
	}
	return remaining
}

func orderByOptions(options, candidates []string) []string {
	ordered := make([]string, 0, len(candidates))
	for _, option := range options {
		if contains(candidates, option) {
			ordered = append(ordered, option)
		}
	}
	return ordered
}

func bulkExclusionSet(options []string, continuing map[string]bool, tallies map[string]int, remainingPlaces int) []string {
	ordered := make([]string, 0, len(options))
	for _, option := range options {
		if continuing[option] {
			ordered = append(ordered, option)
		}
	}
	sort.SliceStable(ordered, func(i, j int) bool { return tallies[ordered[i]] < tallies[ordered[j]] })
	best := []string(nil)
	sum := 0
	for index := 0; index < len(ordered)-1; index++ {
		sum += tallies[ordered[index]]
		count := index + 1
		if count < 2 || len(ordered)-count < remainingPlaces {
			continue
		}
		if tallies[ordered[index]] == tallies[ordered[index+1]] {
			continue
		}
		if sum < tallies[ordered[index+1]] {
			best = append([]string(nil), ordered[:count]...)
		}
	}
	return best
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
		if !validID.MatchString(o) || options[o] {
			return errors.New("duplicate or empty option identifier")
		}
		options[o] = true
	}
	ballots := map[string]bool{}
	for _, b := range input.Ballots {
		if !validID.MatchString(b.ID) || ballots[b.ID] || len(b.Preferences) == 0 || len(b.Preferences) > len(input.Options) {
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

func fingerprintInput(input Input) (string, error) {
	ballots := make([]any, 0, len(input.Ballots))
	for _, ballot := range input.Ballots {
		ballots = append(ballots, []any{ballot.ID, ballot.Preferences})
	}
	return fingerprint([]any{input.SchemaVersion, input.Rule, input.Options, input.Places, ballots})
}

func fingerprint(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func newDecisionRequest(inputFingerprint string, sequence int, kind string, countIndex int, eligible []string) (DecisionRequest, error) {
	requestFingerprint, err := fingerprint([]any{inputFingerprint, sequence, kind, countIndex, eligible, 1})
	if err != nil {
		return DecisionRequest{}, err
	}
	return DecisionRequest{Sequence: sequence, Kind: kind, RequestFingerprint: requestFingerprint, EligibleOptionIDs: append([]string(nil), eligible...), RequiredSelections: 1}, nil
}

func validateDecision(decision Decision, request DecisionRequest) error {
	if decision.Sequence != request.Sequence || decision.Kind != request.Kind || decision.RequestFingerprint != request.RequestFingerprint || len(decision.SelectedOptionIDs) != 1 || !contains(request.EligibleOptionIDs, decision.SelectedOptionIDs[0]) {
		return errors.New("count decision does not match request")
	}
	return nil
}

func recordTotals(index int, options []string, tallies map[string]int, allocations []allocation) CountRecord {
	record := CountRecord{Index: index, Totals: make([]OptionTotal, 0, len(options))}
	for _, option := range options {
		record.Totals = append(record.Totals, OptionTotal{OptionID: option, Votes: tallies[option]})
	}
	for _, allocation := range allocations {
		if allocation.option == "" {
			record.Exhausted++
		}
	}
	return record
}
