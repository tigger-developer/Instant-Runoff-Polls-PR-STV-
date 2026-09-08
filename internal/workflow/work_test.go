// ABOUTME: Verifies bounded work claims and durable SMTP retry decisions.
// ABOUTME: It protects progress from stale workers and unbounded redelivery.
package workflow

import (
	"errors"
	"testing"
	"time"
)

func TestDeliverySchedulesFiveBoundedRetriesWithPersistedJitter(t *testing.T) {
	now := time.Unix(1_789_000_000, 0).UTC()
	for attempt, base := range []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 16 * time.Minute} {
		delivery := Delivery{Status: DeliveryInFlight, Attempts: attempt + 1}
		if err := delivery.TemporaryFailure(now, 0.1); err != nil {
			t.Fatal(err)
		}
		want := now.Add(base + base/10)
		if delivery.Status != DeliveryRetrying || !delivery.NextDue.Equal(want) || delivery.Attempts != attempt+1 {
			t.Fatalf("attempt %d delivery = %#v, want due %s", attempt+1, delivery, want)
		}
		persisted := delivery.NextDue
		if err := delivery.TemporaryFailure(now.Add(time.Second), 0); err != nil || delivery.NextDue != persisted {
			t.Fatal("reprocessing changed persisted retry")
		}
		now = want
	}
	delivery := Delivery{Status: DeliveryInFlight, Attempts: 6}
	if err := delivery.TemporaryFailure(now, 0); !errors.Is(err, ErrDeliveryTerminal) || delivery.Status != DeliveryFailed || delivery.Attempts != 6 {
		t.Fatalf("sixth failure = %#v, %v", delivery, err)
	}
}

func TestDeliveryRejectsInvalidJitter(t *testing.T) {
	delivery := Delivery{Status: DeliveryInFlight, Attempts: 1}
	if err := delivery.TemporaryFailure(time.Now(), -0.1); err == nil {
		t.Fatal("negative jitter succeeded")
	}
	if err := delivery.TemporaryFailure(time.Now(), 0.10001); err == nil {
		t.Fatal("excess jitter succeeded")
	}
}
