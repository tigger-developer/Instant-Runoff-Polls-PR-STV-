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

var (
	ErrInvalidInput    = errors.New("invalid count input")
	ErrInvalidDecision = errors.New("invalid count decision")
)

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
	Index                 int                   `json:"index"`
	Totals                []OptionTotal         `json:"totals"`
	Exhausted             int                   `json:"exhausted"`
	NonTransferableChange int                   `json:"non_transferable_change"`
	ElectedOptionIDs      []string              `json:"elected_option_ids"`
	ExcludedOptionIDs     []string              `json:"excluded_option_ids"`
	SurplusOptionID       string                `json:"surplus_option_id,omitempty"`
	Transfers             []Transfer            `json:"transfers"`
	Apportionments        []Apportionment       `json:"apportionments"`
	Selections            []SelectionProvenance `json:"selections"`
}
type Transfer struct {
	BallotID     string `json:"ballot_id"`
	FromOptionID string `json:"from_option_id"`
	ToOptionID   string `json:"to_option_id,omitempty"`
}
type Apportionment struct {
	DestinationOptionID string   `json:"destination_option_id"`
	ParcelBallots       int      `json:"parcel_ballots"`
	Quotient            int      `json:"quotient"`
	Remainder           int      `json:"remainder"`
	SelectedBallotIDs   []string `json:"selected_ballot_ids"`
}
type SelectionProvenance struct {
	Kind              string   `json:"kind"`
	Method            string   `json:"method"`
	SelectedOptionIDs []string `json:"selected_option_ids"`
	DecisionSequence  int      `json:"decision_sequence,omitempty"`
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

type surplusTransferResult struct {
	transfers      []Transfer
	apportionments []Apportionment
}

type remainderPlan struct {
	selected []string
	tied     []string
	slots    int
}

func Run(ctx context.Context, input Input, decisions []Decision) (Outcome, error) {
	if err := ctx.Err(); err != nil {
		return Outcome{}, err
	}
	if err := validate(ctx, input); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return Outcome{}, err
		}
		return Outcome{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	inputFingerprint, err := fingerprintInput(input)
	if err != nil {
		return Outcome{}, fmt.Errorf("fingerprinting count input: %w", err)
	}
	engine := &countEngine{
		input: input, decisions: decisions, quota: len(input.Ballots)/(input.Places+1) + 1,
		inputFingerprint: inputFingerprint, allocations: make([]allocation, len(input.Ballots)),
		continuing: make(map[string]bool, len(input.Options)), processed: map[string]bool{},
		electionParcel: map[string]int{},
	}
	for index, ballot := range input.Ballots {
		engine.allocations[index] = allocation{ballot: ballot, option: ballot.Preferences[0]}
	}
	for _, option := range input.Options {
		engine.continuing[option] = true
	}
	return engine.run(ctx)
}

type countEngine struct {
	input                 Input
	decisions             []Decision
	quota                 int
	inputFingerprint      string
	allocations           []allocation
	continuing            map[string]bool
	winners               []string
	processed             map[string]bool
	electionParcel        map[string]int
	decisionIndex         int
	operation             int
	countRecords          []CountRecord
	pendingExcluded       []string
	pendingSurplus        string
	pendingTransfers      []Transfer
	pendingApportionments []Apportionment
	pendingSelections     []SelectionProvenance
	previousExhausted     int
}

func (engine *countEngine) run(ctx context.Context) (Outcome, error) {
	for steps := 0; steps < 100; steps++ {
		if err := ctx.Err(); err != nil {
			return Outcome{}, err
		}
		tallies := tally(engine.input.Options, engine.allocations)
		if err := engine.recordCurrent(tallies); err != nil {
			return Outcome{}, err
		}
		if result, err := engine.recognizeWinners(tallies); result != nil || err != nil {
			return Outcome{Result: result}, err
		}
		processed, outcome, err := engine.processSurplus(ctx, tallies)
		if err != nil || outcome.DecisionRequest != nil {
			return outcome, err
		}
		if processed {
			continue
		}
		outcome, err = engine.processExclusion(ctx, tallies)
		if err != nil || outcome.DecisionRequest != nil {
			return outcome, err
		}
	}
	return Outcome{}, errors.New("count exceeded progress bound")
}

func (engine *countEngine) recordCurrent(tallies map[string]int) error {
	if !conservesBallots(tallies, engine.allocations) {
		return errors.New("count ballot conservation failed")
	}
	record := recordTotals(engine.operation+1, engine.input.Options, tallies, engine.allocations)
	record.ExcludedOptionIDs = engine.pendingExcluded
	record.SurplusOptionID = engine.pendingSurplus
	record.Transfers = engine.pendingTransfers
	record.Apportionments = engine.pendingApportionments
	record.NonTransferableChange = record.Exhausted - engine.previousExhausted
	record.Selections = engine.pendingSelections
	engine.previousExhausted = record.Exhausted
	engine.countRecords = append(engine.countRecords, record)
	engine.pendingExcluded = nil
	engine.pendingSurplus = ""
	engine.pendingTransfers = nil
	engine.pendingApportionments = nil
	engine.pendingSelections = nil
	return nil
}

func (engine *countEngine) recognizeWinners(tallies map[string]int) (*Result, error) {
	for _, option := range engine.input.Options {
		if engine.continuing[option] && tallies[option] >= engine.quota {
			engine.elect(option)
			if len(engine.winners) == engine.input.Places {
				return engine.result()
			}
		}
	}
	if continuingCount(engine.continuing) != engine.input.Places-len(engine.winners) {
		return nil, nil
	}
	for _, option := range engine.input.Options {
		if engine.continuing[option] {
			engine.elect(option)
		}
	}
	return engine.result()
}

func (engine *countEngine) elect(option string) {
	engine.continuing[option] = false
	engine.winners = append(engine.winners, option)
	last := len(engine.countRecords) - 1
	engine.countRecords[last].ElectedOptionIDs = append(engine.countRecords[last].ElectedOptionIDs, option)
	engine.electionParcel[option] = engine.operation
}

func (engine *countEngine) result() (*Result, error) {
	if engine.decisionIndex != len(engine.decisions) {
		return nil, fmt.Errorf("%w: unused decision", ErrInvalidDecision)
	}
	return &Result{SchemaVersion: 1, Rule: engine.input.Rule, InputFingerprint: engine.inputFingerprint, Quota: engine.quota, Winners: orderByOptions(engine.input.Options, engine.winners), Counts: engine.countRecords, UsedDecisions: append([]Decision(nil), engine.decisions...)}, nil
}

func (engine *countEngine) processSurplus(ctx context.Context, tallies map[string]int) (bool, Outcome, error) {
	selected, outcome, err := engine.selectSurplus(tallies)
	if err != nil || outcome.DecisionRequest != nil || selected == "" {
		return false, outcome, err
	}
	plan, err := buildRemainderPlan(ctx, engine.input.Options, selected, tallies[selected]-engine.quota, engine.allocations, engine.continuing, engine.electionParcel[selected])
	if err != nil {
		return false, Outcome{}, err
	}
	for plan.slots > 0 {
		if plan.slots == len(plan.tied) {
			plan.selected = append(plan.selected, plan.tied...)
			break
		}
		eligible := historicalCandidates(plan.tied, engine.countRecords[:len(engine.countRecords)-1], true)
		choice := ""
		if len(eligible) == 1 {
			choice = eligible[0]
			engine.pendingSelections = append(engine.pendingSelections, SelectionProvenance{Kind: "remainder_lot", Method: "historical_high", SelectedOptionIDs: []string{choice}})
		} else {
			request, err := newDecisionRequest(engine.inputFingerprint, engine.decisionIndex+1, "remainder_lot", engine.operation+1, orderByOptions(engine.input.Options, eligible))
			if err != nil {
				return false, Outcome{}, fmt.Errorf("fingerprinting remainder decision request: %w", err)
			}
			decision, available, err := engine.consumeDecision(request)
			if err != nil {
				return false, Outcome{}, err
			}
			if !available {
				return false, Outcome{DecisionRequest: &request}, nil
			}
			choice = decision.SelectedOptionIDs[0]
			engine.pendingSelections = append(engine.pendingSelections, decisionProvenance(decision))
		}
		plan.selected = append(plan.selected, choice)
		plan.tied = withoutOption(plan.tied, choice)
		plan.slots--
	}
	engine.operation++
	engine.pendingSurplus = selected
	transferResult, err := transferSurplus(ctx, selected, tallies[selected]-engine.quota, engine.allocations, engine.continuing, engine.electionParcel[selected], engine.operation, plan.selected)
	if err != nil {
		return false, Outcome{}, err
	}
	engine.pendingTransfers = transferResult.transfers
	engine.pendingApportionments = transferResult.apportionments
	engine.processed[selected] = true
	return true, Outcome{}, nil
}

func (engine *countEngine) selectSurplus(tallies map[string]int) (string, Outcome, error) {
	pending := largestPendingSurpluses(engine.winners, engine.processed, tallies, engine.quota)
	if len(pending) == 0 {
		return "", Outcome{}, nil
	}
	if len(pending) == 1 {
		return pending[0], Outcome{}, nil
	}
	pending = historicalCandidates(pending, engine.countRecords[:len(engine.countRecords)-1], true)
	if len(pending) == 1 {
		engine.pendingSelections = append(engine.pendingSelections, SelectionProvenance{Kind: "surplus_order", Method: "historical_high", SelectedOptionIDs: []string{pending[0]}})
		return pending[0], Outcome{}, nil
	}
	request, err := newDecisionRequest(engine.inputFingerprint, engine.decisionIndex+1, "surplus_order_lot", engine.operation+1, orderByOptions(engine.input.Options, pending))
	if err != nil {
		return "", Outcome{}, fmt.Errorf("fingerprinting surplus decision request: %w", err)
	}
	decision, available, err := engine.consumeDecision(request)
	if err != nil {
		return "", Outcome{}, err
	}
	if !available {
		return "", Outcome{DecisionRequest: &request}, nil
	}
	engine.pendingSelections = append(engine.pendingSelections, decisionProvenance(decision))
	return decision.SelectedOptionIDs[0], Outcome{}, nil
}

func (engine *countEngine) processExclusion(ctx context.Context, tallies map[string]int) (Outcome, error) {
	lowest, candidates := lowestCandidates(engine.input.Options, engine.continuing, tallies)
	if lowest == "" {
		return Outcome{}, errors.New("count made no progress")
	}
	if len(candidates) > 1 {
		candidates = historicalCandidates(candidates, engine.countRecords[:len(engine.countRecords)-1], false)
		if len(candidates) == 1 {
			lowest = candidates[0]
			engine.pendingSelections = append(engine.pendingSelections, SelectionProvenance{Kind: "exclusion", Method: "historical_low", SelectedOptionIDs: []string{lowest}})
		}
	}
	exclusionSet := bulkExclusionSet(engine.input.Options, engine.continuing, tallies, engine.input.Places-len(engine.winners))
	if len(exclusionSet) == 0 && len(candidates) > 1 {
		request, err := newDecisionRequest(engine.inputFingerprint, engine.decisionIndex+1, "exclusion_lot", engine.operation+1, candidates)
		if err != nil {
			return Outcome{}, fmt.Errorf("fingerprinting exclusion decision request: %w", err)
		}
		decision, available, err := engine.consumeDecision(request)
		if err != nil {
			return Outcome{}, err
		}
		if !available {
			return Outcome{DecisionRequest: &request}, nil
		}
		lowest = decision.SelectedOptionIDs[0]
		engine.pendingSelections = append(engine.pendingSelections, decisionProvenance(decision))
	}
	if len(exclusionSet) == 0 {
		exclusionSet = []string{lowest}
	} else {
		engine.pendingSelections = append(engine.pendingSelections, SelectionProvenance{Kind: "exclusion", Method: "bulk_rule", SelectedOptionIDs: append([]string(nil), exclusionSet...)})
	}
	return Outcome{}, engine.applyExclusion(ctx, exclusionSet)
}

func (engine *countEngine) consumeDecision(request DecisionRequest) (Decision, bool, error) {
	if engine.decisionIndex == len(engine.decisions) {
		return Decision{}, false, nil
	}
	decision := engine.decisions[engine.decisionIndex]
	if err := validateDecision(decision, request); err != nil {
		return Decision{}, false, fmt.Errorf("%w: %v", ErrInvalidDecision, err)
	}
	engine.decisionIndex++
	return decision, true, nil
}

func (engine *countEngine) applyExclusion(ctx context.Context, exclusionSet []string) error {
	for _, option := range exclusionSet {
		engine.continuing[option] = false
	}
	engine.operation++
	engine.pendingExcluded = append([]string(nil), exclusionSet...)
	for index := range engine.allocations {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !contains(exclusionSet, engine.allocations[index].option) {
			continue
		}
		from := engine.allocations[index].option
		to, err := nextPreference(ctx, engine.allocations[index].ballot, engine.continuing)
		if err != nil {
			return err
		}
		engine.allocations[index].option = to
		engine.allocations[index].parcel = engine.operation
		engine.pendingTransfers = append(engine.pendingTransfers, Transfer{BallotID: engine.allocations[index].ballot.ID, FromOptionID: from, ToOptionID: to})
	}
	return nil
}

func largestPendingSurpluses(winners []string, processed map[string]bool, tallies map[string]int, quota int) []string {
	pending := []string{}
	largest := 0
	for _, winner := range winners {
		if processed[winner] || tallies[winner] <= quota {
			continue
		}
		surplus := tallies[winner] - quota
		if surplus > largest {
			largest = surplus
			pending = []string{winner}
		} else if surplus == largest {
			pending = append(pending, winner)
		}
	}
	return pending
}

func lowestCandidates(options []string, continuing map[string]bool, tallies map[string]int) (string, []string) {
	lowest := ""
	candidates := []string{}
	for _, option := range options {
		if !continuing[option] {
			continue
		}
		if lowest == "" || tallies[option] < tallies[lowest] {
			lowest = option
			candidates = []string{option}
		} else if tallies[option] == tallies[lowest] {
			candidates = append(candidates, option)
		}
	}
	return lowest, candidates
}

func decisionProvenance(decision Decision) SelectionProvenance {
	return SelectionProvenance{Kind: decision.Kind, Method: "external_lot", SelectedOptionIDs: append([]string(nil), decision.SelectedOptionIDs...), DecisionSequence: decision.Sequence}
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
func nextPreference(ctx context.Context, ballot Ballot, continuing map[string]bool) (string, error) {
	for _, p := range ballot.Preferences {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if continuing[p] {
			return p, nil
		}
	}
	return "", nil
}
func transferSurplus(ctx context.Context, winner string, surplus int, allocations []allocation, continuing map[string]bool, sourceParcel, destinationParcel int, remainderChoices []string) (surplusTransferResult, error) {
	groups := map[string][]int{}
	order := []string{}
	for i, a := range allocations {
		if err := ctx.Err(); err != nil {
			return surplusTransferResult{}, err
		}
		if a.option == winner && a.parcel == sourceParcel {
			d, err := nextPreference(ctx, a.ballot, continuing)
			if err != nil {
				return surplusTransferResult{}, err
			}
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
		return surplusTransferResult{}, nil
	}
	result := surplusTransferResult{}
	moveCounts := map[string]int{}
	if total <= surplus {
		for destination, indexes := range groups {
			moveCounts[destination] = len(indexes)
		}
	} else {
		for _, destination := range order {
			moveCounts[destination] = surplus * len(groups[destination]) / total
		}
		for _, destination := range remainderChoices {
			moveCounts[destination]++
		}
	}
	for _, destination := range order {
		apportionment := Apportionment{
			DestinationOptionID: destination,
			ParcelBallots:       len(groups[destination]),
			Quotient:            surplus * len(groups[destination]) / total,
			Remainder:           surplus * len(groups[destination]) % total,
		}
		for _, index := range groups[destination][:moveCounts[destination]] {
			if err := ctx.Err(); err != nil {
				return surplusTransferResult{}, err
			}
			allocations[index].option = destination
			allocations[index].parcel = destinationParcel
			result.transfers = append(result.transfers, Transfer{BallotID: allocations[index].ballot.ID, FromOptionID: winner, ToOptionID: destination})
			apportionment.SelectedBallotIDs = append(apportionment.SelectedBallotIDs, allocations[index].ballot.ID)
		}
		result.apportionments = append(result.apportionments, apportionment)
	}
	return result, nil
}

func buildRemainderPlan(ctx context.Context, options []string, winner string, surplus int, allocations []allocation, continuing map[string]bool, sourceParcel int) (remainderPlan, error) {
	counts := map[string]int{}
	total := 0
	for _, allocation := range allocations {
		if err := ctx.Err(); err != nil {
			return remainderPlan{}, err
		}
		if allocation.option != winner || allocation.parcel != sourceParcel {
			continue
		}
		destination, err := nextPreference(ctx, allocation.ballot, continuing)
		if err != nil {
			return remainderPlan{}, err
		}
		if destination != "" {
			counts[destination]++
			total++
		}
	}
	if total == 0 || total <= surplus {
		return remainderPlan{}, nil
	}
	allocated := 0
	for _, count := range counts {
		allocated += surplus * count / total
	}
	remaining := surplus - allocated
	if remaining == 0 {
		return remainderPlan{}, nil
	}
	ordered := make([]string, 0, len(counts))
	for _, option := range options {
		if _, ok := counts[option]; ok {
			ordered = append(ordered, option)
		}
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		return surplus*counts[ordered[i]]%total > surplus*counts[ordered[j]]%total
	})
	cutoff := surplus * counts[ordered[remaining-1]] % total
	plan := remainderPlan{}
	for _, option := range ordered {
		remainder := surplus * counts[option] % total
		if remainder > cutoff {
			plan.selected = append(plan.selected, option)
		} else if remainder == cutoff {
			plan.tied = append(plan.tied, option)
		}
	}
	plan.slots = remaining - len(plan.selected)
	return plan, nil
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

func withoutOption(options []string, removed string) []string {
	result := make([]string, 0, len(options)-1)
	for _, option := range options {
		if option != removed {
			result = append(result, option)
		}
	}
	return result
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
func validate(ctx context.Context, input Input) error {
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
		if err := ctx.Err(); err != nil {
			return err
		}
		if !validID.MatchString(o) || options[o] {
			return errors.New("duplicate or empty option identifier")
		}
		options[o] = true
	}
	ballots := map[string]bool{}
	for _, b := range input.Ballots {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !validID.MatchString(b.ID) || ballots[b.ID] || len(b.Preferences) == 0 || len(b.Preferences) > len(input.Options) {
			return errors.New("invalid ballot identifier or preferences")
		}
		ballots[b.ID] = true
		seen := map[string]bool{}
		for _, p := range b.Preferences {
			if err := ctx.Err(); err != nil {
				return err
			}
			if !options[p] || seen[p] {
				return fmt.Errorf("invalid ballot preference")
			}
			seen[p] = true
		}
	}
	return nil
}

func conservesBallots(tallies map[string]int, allocations []allocation) bool {
	total := 0
	for _, votes := range tallies {
		total += votes
	}
	for _, allocation := range allocations {
		if allocation.option == "" {
			total++
		}
	}
	return total == len(allocations)
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
