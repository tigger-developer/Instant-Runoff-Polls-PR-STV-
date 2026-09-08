// ABOUTME: Runs one bounded close-count-delivery work pass.
// ABOUTME: It emits the stable worker summary while preserving committed progress.
package workflow

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/tigger-developer/Instant-Runoff-Polls-PR-STV-/internal/store"
)

type Summary struct {
	Version      int `json:"version"`
	Closed       int `json:"closed"`
	Counted      int `json:"counted"`
	NoVotes      int `json:"no_votes"`
	SMTPAccepted int `json:"smtp_accepted"`
	Retrying     int `json:"retrying"`
	Failed       int `json:"failed"`
	Cancelled    int `json:"cancelled"`
}

type WorkRepository interface {
	ClaimDueWork(context.Context, string, string, time.Time) (*store.ClaimedWork, error)
	CompleteWork(context.Context, string, string, time.Time) error
	FailWork(context.Context, string, string, time.Time, string) error
}

type WorkHandler func(context.Context, *store.ClaimedWork) (Summary, error)

var (
	ErrWorkRescheduled = errors.New("work item was durably rescheduled")
	ErrWorkFinalized   = errors.New("work item was durably finalized")
	ErrWorkHandled     = errors.New("work item was durably handled")
)

func ProcessDueWork(ctx context.Context, repository WorkRepository, handlers map[string]WorkHandler, newToken func() string, now func() time.Time) (Summary, error) {
	summary := Summary{Version: 1}
	if repository == nil || newToken == nil || now == nil {
		summary.Failed = 1
		return summary, errors.New("worker dependencies are required")
	}
	processed := 0
	for _, kind := range []string{"close", "count", "delivery"} {
		for processed < 100 {
			if err := ctx.Err(); err != nil {
				summary.Failed++
				return summary, fmt.Errorf("worker deadline: %w", err)
			}
			token := newToken()
			if token == "" {
				summary.Failed++
				return summary, errors.New("worker claim token is empty")
			}
			item, err := repository.ClaimDueWork(ctx, kind, token, now())
			if err != nil {
				summary.Failed++
				return summary, fmt.Errorf("claim %s work: %w", kind, err)
			}
			if item == nil {
				break
			}
			handler := handlers[kind]
			if handler == nil {
				summary.Failed++
				_ = repository.FailWork(ctx, item.ID, token, now(), "handler unavailable")
				return summary, fmt.Errorf("%s handler is unavailable", kind)
			}
			delta, handleErr := handler(ctx, item)
			summary.add(delta)
			processed++
			if errors.Is(handleErr, ErrWorkHandled) {
				continue
			}
			if handleErr != nil {
				summary.Failed++
				if errors.Is(handleErr, ErrWorkRescheduled) || errors.Is(handleErr, ErrWorkFinalized) {
					return summary, fmt.Errorf("process %s work: %w", kind, handleErr)
				}
				if err := repository.FailWork(ctx, item.ID, token, now(), "operation failed"); err != nil {
					return summary, fmt.Errorf("record %s failure: %w", kind, err)
				}
				return summary, fmt.Errorf("process %s work: %w", kind, handleErr)
			}
			if err := repository.CompleteWork(ctx, item.ID, token, now()); err != nil {
				summary.Failed++
				return summary, fmt.Errorf("complete %s work: %w", kind, err)
			}
		}
	}
	return summary, nil
}

func (summary *Summary) add(delta Summary) {
	summary.Closed += delta.Closed
	summary.Counted += delta.Counted
	summary.NoVotes += delta.NoVotes
	summary.SMTPAccepted += delta.SMTPAccepted
	summary.Retrying += delta.Retrying
	summary.Failed += delta.Failed
	summary.Cancelled += delta.Cancelled
}
