// ABOUTME: Verifies bounded close-count-delivery processing and summary output.
// ABOUTME: It protects work ordering, progress retention, and command observability.
package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/tigger-developer/Instant-Runoff-Polls-PR-STV-/internal/store"
)

func TestProcessDueWorkOrdersKindsAndEmitsExactSummary(t *testing.T) {
	repository := &fakeWorkRepository{items: map[string][]*store.ClaimedWork{
		"close":    {{ID: "close-1", Kind: "close"}},
		"count":    {{ID: "count-1", Kind: "count"}},
		"delivery": {{ID: "mail-1", Kind: "delivery"}, {ID: "mail-2", Kind: "delivery"}},
	}}
	handlers := map[string]WorkHandler{
		"close":    func(context.Context, *store.ClaimedWork) (Summary, error) { return Summary{Closed: 1}, nil },
		"count":    func(context.Context, *store.ClaimedWork) (Summary, error) { return Summary{Counted: 1}, nil },
		"delivery": func(context.Context, *store.ClaimedWork) (Summary, error) { return Summary{SMTPAccepted: 1}, nil },
	}
	summary, err := ProcessDueWork(context.Background(), repository, handlers, func() string { return "token" }, func() time.Time { return time.Unix(100, 0) })
	if err != nil {
		t.Fatal(err)
	}
	wantOrder := []string{"close", "count", "delivery", "delivery"}
	if !reflect.DeepEqual(repository.claimOrder, wantOrder) {
		t.Fatalf("claim order = %v", repository.claimOrder)
	}
	if summary != (Summary{Version: 1, Closed: 1, Counted: 1, SMTPAccepted: 2}) {
		t.Fatalf("summary = %#v", summary)
	}
	encoded, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `{"version":1,"closed":1,"counted":1,"no_votes":0,"smtp_accepted":2,"retrying":0,"failed":0,"cancelled":0}` {
		t.Fatalf("JSON = %s", encoded)
	}
}

func TestProcessDueWorkStopsAtHundredAndPreservesFailureProgress(t *testing.T) {
	items := make([]*store.ClaimedWork, 101)
	for index := range items {
		items[index] = &store.ClaimedWork{ID: string(rune(index + 1)), Kind: "close"}
	}
	repository := &fakeWorkRepository{items: map[string][]*store.ClaimedWork{"close": items}}
	summary, err := ProcessDueWork(context.Background(), repository, map[string]WorkHandler{"close": func(context.Context, *store.ClaimedWork) (Summary, error) { return Summary{Closed: 1}, nil }}, func() string { return "token" }, time.Now)
	if err != nil || summary.Closed != 100 || len(repository.items["close"]) != 1 {
		t.Fatalf("bounded summary=%#v remaining=%d error=%v", summary, len(repository.items["close"]), err)
	}

	repository = &fakeWorkRepository{items: map[string][]*store.ClaimedWork{"close": {{ID: "one", Kind: "close"}}, "count": {{ID: "two", Kind: "count"}}}}
	summary, err = ProcessDueWork(context.Background(), repository, map[string]WorkHandler{
		"close": func(context.Context, *store.ClaimedWork) (Summary, error) { return Summary{Closed: 1}, nil },
		"count": func(context.Context, *store.ClaimedWork) (Summary, error) {
			return Summary{}, errors.New("count failed")
		},
	}, func() string { return "token" }, time.Now)
	if err == nil || summary.Closed != 1 || summary.Failed != 1 || len(repository.failed) != 1 {
		t.Fatalf("failure summary=%#v failed=%v error=%v", summary, repository.failed, err)
	}
}

func TestProcessDueWorkDoesNotOverwriteDurableRetry(t *testing.T) {
	repository := &fakeWorkRepository{items: map[string][]*store.ClaimedWork{
		"delivery": {{ID: "mail", Kind: "delivery"}},
	}}
	summary, err := ProcessDueWork(context.Background(), repository, map[string]WorkHandler{
		"delivery": func(context.Context, *store.ClaimedWork) (Summary, error) {
			return Summary{Retrying: 1}, ErrWorkRescheduled
		},
	}, func() string { return "token" }, func() time.Time { return time.Unix(100, 0) })
	if !errors.Is(err, ErrWorkRescheduled) || summary.Retrying != 1 || summary.Failed != 1 {
		t.Fatalf("summary=%#v error=%v", summary, err)
	}
	if len(repository.failed) != 0 || len(repository.completed) != 0 {
		t.Fatalf("retry was overwritten: failed=%v completed=%v", repository.failed, repository.completed)
	}
}

func TestProcessDueWorkContinuesAfterDurablyHandledItem(t *testing.T) {
	repository := &fakeWorkRepository{items: map[string][]*store.ClaimedWork{
		"delivery": {{ID: "held", Kind: "delivery"}, {ID: "sent", Kind: "delivery"}},
	}}
	handled := 0
	summary, err := ProcessDueWork(context.Background(), repository, map[string]WorkHandler{
		"delivery": func(context.Context, *store.ClaimedWork) (Summary, error) {
			handled++
			if handled == 1 {
				return Summary{}, ErrWorkHandled
			}
			return Summary{SMTPAccepted: 1}, nil
		},
	}, func() string { return "token" }, func() time.Time { return time.Unix(100, 0) })
	if err != nil || summary.Failed != 0 || summary.SMTPAccepted != 1 || len(repository.completed) != 1 {
		t.Fatalf("summary=%#v completed=%v error=%v", summary, repository.completed, err)
	}
}

func TestProcessDueWorkReportsMissingHandlerPersistenceFailure(t *testing.T) {
	persistErr := errors.New("database unavailable")
	repository := &fakeWorkRepository{items: map[string][]*store.ClaimedWork{"close": {{ID: "close", Kind: "close"}}}, failErr: persistErr}
	summary, err := ProcessDueWork(context.Background(), repository, map[string]WorkHandler{}, func() string { return "token" }, time.Now)
	if !errors.Is(err, persistErr) || summary.Failed != 1 {
		t.Fatalf("summary=%#v error=%v", summary, err)
	}
}

type fakeWorkRepository struct {
	items      map[string][]*store.ClaimedWork
	claimOrder []string
	completed  []string
	failed     []string
	failErr    error
}

func (repo *fakeWorkRepository) ClaimDueWork(_ context.Context, kind, token string, _ time.Time) (*store.ClaimedWork, error) {
	items := repo.items[kind]
	if len(items) == 0 {
		return nil, nil
	}
	item := items[0]
	repo.items[kind] = items[1:]
	item.ClaimToken = token
	repo.claimOrder = append(repo.claimOrder, kind)
	return item, nil
}
func (repo *fakeWorkRepository) CompleteWork(_ context.Context, id, _ string, _ time.Time) error {
	repo.completed = append(repo.completed, id)
	return nil
}
func (repo *fakeWorkRepository) FailWork(_ context.Context, id, _ string, _ time.Time, _ string) error {
	repo.failed = append(repo.failed, id)
	return repo.failErr
}
