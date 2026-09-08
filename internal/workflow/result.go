// ABOUTME: Projects internal W002 results into aggregate-only moderator data.
// ABOUTME: It deliberately omits ballot, parcel, snapshot, and request identifiers.
package workflow

import "github.com/tigger-developer/Instant-Runoff-Polls-PR-STV-/internal/count"

type ResultView struct {
	Turnout int               `json:"turnout"`
	Quota   int               `json:"quota,omitempty"`
	Winners []string          `json:"winners"`
	Counts  []ResultCountView `json:"counts"`
	NoVotes bool              `json:"no_votes"`
}

type ResultCountView struct {
	Index             int                 `json:"index"`
	Totals            []count.OptionTotal `json:"totals"`
	Exhausted         int                 `json:"exhausted"`
	ElectedOptionIDs  []string            `json:"elected_option_ids"`
	ExcludedOptionIDs []string            `json:"excluded_option_ids"`
	SurplusOptionID   string              `json:"surplus_option_id,omitempty"`
	Transfers         []AggregateTransfer `json:"transfers"`
	SelectionOutcomes []SelectionOutcome  `json:"selection_outcomes"`
}

type AggregateTransfer struct {
	FromOptionID string `json:"from_option_id"`
	ToOptionID   string `json:"to_option_id,omitempty"`
	Ballots      int    `json:"ballots"`
}

type SelectionOutcome struct {
	Kind              string   `json:"kind"`
	Method            string   `json:"method"`
	SelectedOptionIDs []string `json:"selected_option_ids"`
}

func ProjectResult(turnout int, result count.Result) ResultView {
	view := ResultView{Turnout: turnout, Quota: result.Quota, Winners: append([]string(nil), result.Winners...), Counts: make([]ResultCountView, 0, len(result.Counts))}
	for _, record := range result.Counts {
		projected := ResultCountView{Index: record.Index, Totals: append([]count.OptionTotal(nil), record.Totals...), Exhausted: record.Exhausted, ElectedOptionIDs: append([]string(nil), record.ElectedOptionIDs...), ExcludedOptionIDs: append([]string(nil), record.ExcludedOptionIDs...), SurplusOptionID: record.SurplusOptionID}
		transferCounts := make(map[[2]string]int)
		transferOrder := make([][2]string, 0)
		for _, transfer := range record.Transfers {
			key := [2]string{transfer.FromOptionID, transfer.ToOptionID}
			if _, exists := transferCounts[key]; !exists {
				transferOrder = append(transferOrder, key)
			}
			transferCounts[key]++
		}
		for _, key := range transferOrder {
			projected.Transfers = append(projected.Transfers, AggregateTransfer{FromOptionID: key[0], ToOptionID: key[1], Ballots: transferCounts[key]})
		}
		for _, selection := range record.Selections {
			projected.SelectionOutcomes = append(projected.SelectionOutcomes, SelectionOutcome{Kind: selection.Kind, Method: selection.Method, SelectedOptionIDs: append([]string(nil), selection.SelectedOptionIDs...)})
		}
		view.Counts = append(view.Counts, projected)
	}
	return view
}

func ProjectZeroTurnout() ResultView {
	return ResultView{Winners: []string{}, Counts: []ResultCountView{}, NoVotes: true}
}
