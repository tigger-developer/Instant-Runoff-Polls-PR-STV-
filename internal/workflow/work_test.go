// ABOUTME: Verifies bounded work claims and durable SMTP retry decisions.
// ABOUTME: It protects progress from stale workers and unbounded redelivery.
package workflow

import (
	"errors"
	"testing"
	"time"
)

func TestWorkClaimExpiresAndRejectsStaleAcknowledgement(t *testing.T) {
	now := time.Unix(1_789_000_000, 0).UTC()
	item := WorkItem{ID: "work-1", Status: WorkPending}
	if err := item.Claim("claim-one", now); err != nil {
		t.Fatal(err)
	}
	if item.ClaimExpiresAt != now.Add(120*time.Second) || !item.CanCommit("claim-one", now.Add(119*time.Second)) {
		t.Fatalf("claim = %#v", item)
	}
	if err := item.Claim("claim-two", now.Add(121*time.Second)); err != nil {
		t.Fatal(err)
	}
	if item.CanCommit("claim-one", now.Add(121*time.Second)) || !item.CanCommit("claim-two", now.Add(121*time.Second)) {
		t.Fatal("stale claim retained commit authority")
	}
}

func TestDeliverySchedulesFiveBoundedRetriesWithPersistedJitter(t *testing.T) {
	now := time.Unix(1_789_000_000, 0).UTC()
	delivery := Delivery{Status: DeliveryPending}
	for attempt, base := range []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 16 * time.Minute} {
		if err := delivery.StartAttempt(); err != nil {
			t.Fatal(err)
		}
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
		delivery.Status = DeliveryPending
		now = want
	}
	if err := delivery.StartAttempt(); err != nil {
		t.Fatal(err)
	}
	if err := delivery.TemporaryFailure(now, 0); !errors.Is(err, ErrDeliveryTerminal) || delivery.Status != DeliveryFailed || delivery.Attempts != 6 {
		t.Fatalf("sixth failure = %#v, %v", delivery, err)
	}
}

func TestDeliveryPermanentFailureStopsImmediately(t *testing.T) {
	delivery := Delivery{Status: DeliveryPending}
	if err := delivery.StartAttempt(); err != nil {
		t.Fatal(err)
	}
	delivery.PermanentFailure()
	if delivery.Status != DeliveryFailed || delivery.Attempts != 1 || !delivery.NextDue.IsZero() {
		t.Fatalf("permanent failure = %#v", delivery)
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
