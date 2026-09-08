// ABOUTME: Defines bounded work claims and persisted delivery retry transitions.
// ABOUTME: It prevents stale acknowledgements and unlimited SMTP attempts.
package workflow

import (
	"errors"
	"time"
)

var (
	ErrWorkClaimed      = errors.New("work item is already claimed")
	ErrInvalidDelivery  = errors.New("invalid delivery transition")
	ErrDeliveryTerminal = errors.New("delivery retry limit reached")
)

type WorkStatus string

const (
	WorkPending WorkStatus = "pending"
	WorkClaimed WorkStatus = "claimed"
)

type WorkItem struct {
	ID             string
	Status         WorkStatus
	ClaimToken     string
	ClaimExpiresAt time.Time
}

func (item *WorkItem) Claim(token string, now time.Time) error {
	if token == "" {
		return ErrWorkClaimed
	}
	if item.Status == WorkClaimed && now.Before(item.ClaimExpiresAt) {
		return ErrWorkClaimed
	}
	item.Status = WorkClaimed
	item.ClaimToken = token
	item.ClaimExpiresAt = now.Add(120 * time.Second)
	return nil
}

func (item WorkItem) CanCommit(token string, now time.Time) bool {
	return item.Status == WorkClaimed && token != "" && token == item.ClaimToken && now.Before(item.ClaimExpiresAt)
}

type DeliveryStatus string

const (
	DeliveryPending   DeliveryStatus = "pending"
	DeliveryInFlight  DeliveryStatus = "in_flight"
	DeliveryRetrying  DeliveryStatus = "retrying"
	DeliveryAccepted  DeliveryStatus = "smtp_accepted"
	DeliveryFailed    DeliveryStatus = "failed"
	DeliveryCancelled DeliveryStatus = "cancelled"
)

type Delivery struct {
	Status   DeliveryStatus
	Attempts int
	NextDue  time.Time
}

func (delivery *Delivery) StartAttempt() error {
	if delivery.Status != DeliveryPending && delivery.Status != DeliveryRetrying {
		return ErrInvalidDelivery
	}
	if delivery.Attempts >= 6 {
		return ErrDeliveryTerminal
	}
	delivery.Attempts++
	delivery.Status = DeliveryInFlight
	delivery.NextDue = time.Time{}
	return nil
}

func (delivery *Delivery) TemporaryFailure(now time.Time, jitterFraction float64) error {
	if delivery.Status == DeliveryRetrying && !delivery.NextDue.IsZero() {
		return nil
	}
	if delivery.Status != DeliveryInFlight || jitterFraction < 0 || jitterFraction > 0.1 {
		return ErrInvalidDelivery
	}
	if delivery.Attempts >= 6 {
		delivery.Status = DeliveryFailed
		delivery.NextDue = time.Time{}
		return ErrDeliveryTerminal
	}
	base := time.Minute * time.Duration(1<<(delivery.Attempts-1))
	delivery.NextDue = now.Add(base + time.Duration(float64(base)*jitterFraction))
	delivery.Status = DeliveryRetrying
	return nil
}

func (delivery *Delivery) PermanentFailure() {
	delivery.Status = DeliveryFailed
	delivery.NextDue = time.Time{}
}
