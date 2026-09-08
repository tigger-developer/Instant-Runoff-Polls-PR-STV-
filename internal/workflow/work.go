// ABOUTME: Defines bounded work claims and persisted delivery retry transitions.
// ABOUTME: It prevents stale acknowledgements and unlimited SMTP attempts.
package workflow

import (
	"errors"
	"time"
)

var (
	ErrInvalidDelivery  = errors.New("invalid delivery transition")
	ErrDeliveryTerminal = errors.New("delivery retry limit reached")
)

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
