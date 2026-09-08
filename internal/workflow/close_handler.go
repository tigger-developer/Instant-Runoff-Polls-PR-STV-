// ABOUTME: Converts claimed deadline work into one durable poll closure.
// ABOUTME: It freezes anonymized count input or records a zero-turnout outcome.
package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/tigger-developer/Instant-Runoff-Polls-PR-STV-/internal/store"
)

func NewCloseHandler(repository *store.Store, randomness io.Reader, now func() time.Time) WorkHandler {
	return func(ctx context.Context, item *store.ClaimedWork) (Summary, error) {
		if repository == nil || randomness == nil || now == nil || item == nil || item.Kind != "close" {
			return Summary{}, errors.New("close handler dependencies are invalid")
		}
		at := now()
		work, err := repository.LoadCloseWork(ctx, item.ID, item.ClaimToken, at)
		if err != nil {
			return Summary{}, fmt.Errorf("load close work: %w", err)
		}
		changed, noVotes, err := repository.ClosePoll(ctx, work.OwnerID, work.PollID, work.Version, func(current store.CloseWork) (store.CountSnapshot, error) {
			return BuildCloseSnapshot(current, randomness)
		}, "count:"+work.PollID, at)
		if err != nil {
			return Summary{}, fmt.Errorf("commit poll close: %w", err)
		}
		if changed {
			summary := Summary{Closed: 1}
			if noVotes {
				summary.NoVotes = 1
			}
			return summary, ErrWorkHandled
		}
		return Summary{}, ErrWorkHandled
	}
}

func BuildCloseSnapshot(work store.CloseWork, randomness io.Reader) (store.CountSnapshot, error) {
	poll := closeWorkflowPoll(work)
	input, err := BuildCountInput(poll, randomness)
	if err != nil {
		return store.CountSnapshot{}, err
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return store.CountSnapshot{}, fmt.Errorf("encode count snapshot: %w", err)
	}
	fingerprint := sha256.Sum256(encoded)
	return store.CountSnapshot{ID: "snapshot:" + work.PollID, SchemaVersion: input.SchemaVersion, Rule: input.Rule, InputFingerprint: hex.EncodeToString(fingerprint[:]), InputJSON: encoded}, nil
}

func closeWorkflowPoll(work store.CloseWork) Poll {
	poll := Poll{ID: work.PollID, OwnerID: work.OwnerID, Question: work.Question, Places: work.Places, Deadline: time.Unix(work.Deadline, 0).UTC(), State: Closed, Version: work.Version, Ballots: make(map[string]Ballot, len(work.Ballots))}
	for _, option := range work.Options {
		poll.Options = append(poll.Options, Option{ID: option.ID, Label: option.Label})
	}
	for _, participant := range work.Participants {
		poll.Participants = append(poll.Participants, Participant{ID: participant.ID})
	}
	for _, ballot := range work.Ballots {
		poll.Ballots[ballot.ParticipantID] = Ballot{ParticipantID: ballot.ParticipantID, Preferences: append([]string(nil), ballot.Preferences...), Version: ballot.Version, AcceptedAt: time.Unix(ballot.AcceptedAt, 0).UTC()}
	}
	return poll
}
