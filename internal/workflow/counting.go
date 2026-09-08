// ABOUTME: Builds anonymized frozen W002 count inputs from closed workflow polls.
// ABOUTME: It applies one cryptographic shuffle before assigning count-only IDs.
package workflow

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"math/big"

	"github.com/tigger-developer/Instant-Runoff-Polls-PR-STV-/internal/count"
)

var ErrInvalidSnapshot = errors.New("invalid count snapshot")

func BuildCountInput(poll Poll, randomness io.Reader) (count.Input, error) {
	if poll.State != Closed || len(poll.Ballots) == 0 || randomness == nil {
		return count.Input{}, ErrInvalidSnapshot
	}
	ballots := make([]count.Ballot, 0, len(poll.Ballots))
	for _, participant := range poll.Participants {
		ballot, exists := poll.Ballots[participant.ID]
		if !exists {
			continue
		}
		ballots = append(ballots, count.Ballot{Preferences: append([]string(nil), ballot.Preferences...)})
	}
	if len(ballots) != len(poll.Ballots) {
		return count.Input{}, fmt.Errorf("%w: ballot participant mismatch", ErrInvalidSnapshot)
	}
	for index := len(ballots) - 1; index > 0; index-- {
		selected, err := rand.Int(randomness, big.NewInt(int64(index+1)))
		if err != nil {
			return count.Input{}, fmt.Errorf("%w: shuffle: %v", ErrInvalidSnapshot, err)
		}
		other := int(selected.Int64())
		ballots[index], ballots[other] = ballots[other], ballots[index]
	}
	for index := range ballots {
		ballots[index].ID = fmt.Sprintf("b%d", index+1)
	}
	options := make([]string, len(poll.Options))
	for index, option := range poll.Options {
		options[index] = option.ID
	}
	return count.Input{SchemaVersion: 1, Rule: count.RuleIrishGuidedSTV, Options: options, Places: poll.Places, Ballots: ballots}, nil
}

func ChooseCountDecision(request count.DecisionRequest, randomness io.Reader) (count.Decision, error) {
	if request.Sequence < 1 || request.Kind == "" || request.RequestFingerprint == "" || request.RequiredSelections != 1 || len(request.EligibleOptionIDs) == 0 || randomness == nil {
		return count.Decision{}, ErrInvalidSnapshot
	}
	selected, err := rand.Int(randomness, big.NewInt(int64(len(request.EligibleOptionIDs))))
	if err != nil {
		return count.Decision{}, fmt.Errorf("%w: lot selection: %v", ErrInvalidSnapshot, err)
	}
	return count.Decision{Sequence: request.Sequence, Kind: request.Kind, RequestFingerprint: request.RequestFingerprint, SelectedOptionIDs: []string{request.EligibleOptionIDs[int(selected.Int64())]}}, nil
}
