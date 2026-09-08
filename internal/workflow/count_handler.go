// ABOUTME: Replays persisted W002 decisions and commits one authoritative count result.
// ABOUTME: It keeps random lot selection durable before continuing the count.
package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/tigger-developer/Instant-Runoff-Polls-PR-STV-/internal/count"
	"github.com/tigger-developer/Instant-Runoff-Polls-PR-STV-/internal/store"
)

func NewCountHandler(repository *store.Store, randomness io.Reader, now func() time.Time) WorkHandler {
	return func(ctx context.Context, item *store.ClaimedWork) (summary Summary, returnErr error) {
		if repository == nil || randomness == nil || now == nil || item == nil || item.Kind != "count" {
			return Summary{}, errors.New("count handler dependencies are invalid")
		}
		work, err := repository.LoadCountWork(ctx, item.ID, item.ClaimToken, now())
		if err != nil {
			return Summary{}, fmt.Errorf("load count evidence: %w", err)
		}
		if len(work.ExistingResult) != 0 {
			return Summary{}, nil
		}
		defer func() {
			if returnErr != nil {
				_ = repository.MarkCountFailed(ctx, item.ID, item.ClaimToken, now())
			}
		}()
		var input count.Input
		if err := json.Unmarshal(work.InputJSON, &input); err != nil {
			return Summary{}, fmt.Errorf("decode count input: %w", err)
		}
		decisions := make([]count.Decision, 0, len(work.DecisionsJSON))
		for _, encoded := range work.DecisionsJSON {
			var decision count.Decision
			if err := json.Unmarshal(encoded, &decision); err != nil {
				return Summary{}, fmt.Errorf("decode count decision: %w", err)
			}
			decisions = append(decisions, decision)
		}
		for step := 0; step < 50; step++ {
			outcome, err := count.Run(ctx, input, decisions)
			if err != nil {
				return Summary{}, fmt.Errorf("run count: %w", err)
			}
			if outcome.Result != nil {
				encoded, err := json.Marshal(outcome.Result)
				if err != nil {
					return Summary{}, fmt.Errorf("encode count result: %w", err)
				}
				created, err := repository.CommitCountResult(ctx, item.ID, item.ClaimToken, now(), encoded)
				if err != nil {
					return Summary{}, fmt.Errorf("commit count result: %w", err)
				}
				if created {
					return Summary{Counted: 1}, ErrWorkHandled
				}
				return Summary{}, ErrWorkHandled
			}
			if outcome.DecisionRequest == nil {
				return Summary{}, errors.New("count returned no result or decision request")
			}
			decision, err := ChooseCountDecision(*outcome.DecisionRequest, randomness)
			if err != nil {
				return Summary{}, err
			}
			encoded, err := json.Marshal(decision)
			if err != nil {
				return Summary{}, fmt.Errorf("encode count decision: %w", err)
			}
			if _, err := repository.CommitCountDecision(ctx, item.ID, item.ClaimToken, now(), decision.Sequence, decision.RequestFingerprint, encoded); err != nil {
				return Summary{}, fmt.Errorf("commit count decision: %w", err)
			}
			decisions = append(decisions, decision)
		}
		return Summary{}, errors.New("count decision limit reached")
	}
}
