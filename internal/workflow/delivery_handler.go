// ABOUTME: Sends one claimed message and persists its bounded SMTP outcome.
// ABOUTME: It keeps network I/O outside transactions and preserves retry state.
package workflow

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/textproto"
	"time"

	"github.com/tigger-developer/Instant-Runoff-Polls-PR-STV-/internal/store"
)

type MessageSender interface {
	Send(context.Context, Message) error
}

type DeliveryMessageBuilder func(context.Context, *store.ClaimedWork, store.DeliveryAttempt) (Message, error)

func NewDeliveryHandler(repository *store.Store, sender MessageSender, build DeliveryMessageBuilder, jitter func() (float64, error), now func() time.Time) WorkHandler {
	return func(ctx context.Context, item *store.ClaimedWork) (Summary, error) {
		if repository == nil || sender == nil || build == nil || jitter == nil || now == nil || item == nil || item.Kind != "delivery" {
			return Summary{}, errors.New("delivery handler dependencies are invalid")
		}
		at := now()
		attempt, err := repository.BeginDeliveryAttempt(ctx, item.ID, item.ClaimToken, at)
		if errors.Is(err, store.ErrDeliveryHeld) {
			return Summary{}, ErrWorkHandled
		}
		if errors.Is(err, store.ErrDeliveryCancelled) {
			return Summary{Cancelled: 1}, ErrWorkHandled
		}
		if errors.Is(err, store.ErrDeliveryExhausted) {
			return Summary{}, fmt.Errorf("%w: SMTP attempt interrupted", ErrWorkFinalized)
		}
		if err != nil {
			return Summary{}, fmt.Errorf("begin delivery attempt: %w", err)
		}
		message, err := build(ctx, item, attempt)
		if err == nil {
			err = sender.Send(ctx, message)
		}
		if err == nil {
			if err := repository.AcceptDelivery(ctx, item.ID, item.ClaimToken, now()); err != nil {
				return finalizeDeliveryError(ctx, repository, item, now(), "SMTP acceptance persistence failure", fmt.Errorf("record SMTP acceptance: %w", err))
			}
			return Summary{SMTPAccepted: 1}, ErrWorkHandled
		}
		failureClass, temporary := classifyDeliveryFailure(err)
		if !temporary || attempt.Attempts >= 6 {
			if persistErr := repository.FailDelivery(ctx, item.ID, item.ClaimToken, now(), failureClass); persistErr != nil {
				return Summary{}, fmt.Errorf("record terminal delivery failure: %w", persistErr)
			}
			return Summary{}, fmt.Errorf("%w: %s", ErrWorkFinalized, failureClass)
		}
		jitterFraction, jitterErr := jitter()
		if jitterErr != nil {
			return finalizeDeliveryError(ctx, repository, item, now(), "delivery retry scheduling failure", fmt.Errorf("generate delivery jitter: %w", jitterErr))
		}
		delivery := Delivery{Status: DeliveryInFlight, Attempts: attempt.Attempts}
		if transitionErr := delivery.TemporaryFailure(at, jitterFraction); transitionErr != nil {
			return finalizeDeliveryError(ctx, repository, item, now(), "delivery retry scheduling failure", fmt.Errorf("schedule delivery retry: %w", transitionErr))
		}
		if persistErr := repository.RetryDelivery(ctx, item.ID, item.ClaimToken, now(), delivery.NextDue, failureClass); persistErr != nil {
			return finalizeDeliveryError(ctx, repository, item, now(), "delivery retry persistence failure", fmt.Errorf("record delivery retry: %w", persistErr))
		}
		return Summary{Retrying: 1}, fmt.Errorf("%w: %s", ErrWorkRescheduled, failureClass)
	}
}

func finalizeDeliveryError(ctx context.Context, repository *store.Store, item *store.ClaimedWork, now time.Time, failureClass string, cause error) (Summary, error) {
	if err := repository.FailDelivery(ctx, item.ID, item.ClaimToken, now, failureClass); err != nil {
		return Summary{}, fmt.Errorf("%v; reconcile delivery failure: %w", cause, err)
	}
	return Summary{}, fmt.Errorf("%w: %s", ErrWorkFinalized, failureClass)
}

func classifyDeliveryFailure(err error) (string, bool) {
	if errors.Is(err, ErrInvalidMessage) {
		return "invalid message", false
	}
	var protocolError *textproto.Error
	if errors.As(err, &protocolError) {
		if protocolError.Code >= 400 && protocolError.Code < 500 {
			return "temporary SMTP rejection", true
		}
		return "permanent SMTP rejection", false
	}
	var networkError net.Error
	if errors.As(err, &networkError) {
		return "SMTP connection failure", true
	}
	return "SMTP policy or authentication failure", false
}
