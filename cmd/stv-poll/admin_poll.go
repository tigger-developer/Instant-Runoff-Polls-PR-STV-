// ABOUTME: Closes polls and exports frozen count evidence through the Exodan admin CLI.
// ABOUTME: It authorizes operations against configured poll owners and emits JSON results.
package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"time"

	"github.com/tigger-developer/Instant-Runoff-Polls-PR-STV-/internal/config"
	"github.com/tigger-developer/Instant-Runoff-Polls-PR-STV-/internal/store"
	"github.com/tigger-developer/Instant-Runoff-Polls-PR-STV-/internal/workflow"
)

type closedPoll struct {
	PollID         string `json:"poll_id"`
	State          string `json:"state"`
	CountingStatus string `json:"counting_status"`
}

func closePollCommand(pollID string, output io.Writer) error {
	return withAdminStore(func(ctx context.Context, st *store.Store, cfg config.Config) error {
		closed, err := closePoll(ctx, st, cfg, pollID, rand.Reader, time.Now())
		if err != nil {
			return err
		}
		return json.NewEncoder(output).Encode(closed)
	})
}

func countAuditCommand(pollID string, output io.Writer) error {
	return withAdminStore(func(ctx context.Context, st *store.Store, cfg config.Config) error {
		return exportCountAudit(ctx, st, cfg, pollID, output)
	})
}

func closePoll(ctx context.Context, st *store.Store, cfg config.Config, pollID string, randomness io.Reader, now time.Time) (closedPoll, error) {
	if st == nil || randomness == nil || !adminPollID.MatchString(pollID) {
		return closedPoll{}, errors.New("valid poll closure dependencies are required")
	}
	ownerID, err := authorizedPollOwner(ctx, st, cfg, pollID)
	if err != nil {
		return closedPoll{}, err
	}
	work, err := st.PollForClose(ctx, ownerID, pollID)
	if err != nil {
		return closedPoll{}, err
	}
	if work.State != "closed" {
		_, _, err = st.ClosePoll(ctx, ownerID, pollID, work.Version, func(current store.CloseWork) (store.CountSnapshot, error) {
			return workflow.BuildCloseSnapshot(current, randomness)
		}, "count:"+pollID, now)
		if err != nil {
			return closedPoll{}, err
		}
	}
	poll, err := st.OwnedPoll(ctx, ownerID, pollID)
	if err != nil {
		return closedPoll{}, err
	}
	return closedPoll{PollID: poll.ID, State: poll.State, CountingStatus: poll.CountingStatus}, nil
}

func exportCountAudit(ctx context.Context, st *store.Store, cfg config.Config, pollID string, output io.Writer) error {
	if st == nil || output == nil || !adminPollID.MatchString(pollID) {
		return errors.New("valid count audit dependencies are required")
	}
	ownerID, err := authorizedPollOwner(ctx, st, cfg, pollID)
	if err != nil {
		return err
	}
	audit, err := st.CountAuditForOwner(ctx, ownerID, pollID)
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(audit)
}

func authorizedPollOwner(ctx context.Context, st *store.Store, cfg config.Config, pollID string) (string, error) {
	ownerID, err := st.PollOwner(ctx, pollID)
	if err != nil {
		return "", err
	}
	for _, moderator := range cfg.Moderators {
		if moderator.ID == ownerID {
			return ownerID, nil
		}
	}
	return "", store.ErrConflict
}

func withAdminStore(operation func(context.Context, *store.Store, config.Config) error) error {
	defaults := os.Getenv("DEFAULT_CONFIG_PATH")
	if defaults == "" {
		return errors.New("DEFAULT_CONFIG_PATH is required")
	}
	state := os.Getenv("STATE_DIRECTORY")
	if state == "" {
		return errors.New("STATE_DIRECTORY is required")
	}
	cfg, err := config.Load(defaults, os.Getenv("CONFIG_PATH"), os.Getenv("SECRETS_PATH"))
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	st, err := store.Open(ctx, state)
	if err != nil {
		return err
	}
	defer st.Close()
	moderators := make([]store.ConfiguredModerator, 0, len(cfg.Moderators))
	for _, moderator := range cfg.Moderators {
		moderators = append(moderators, store.ConfiguredModerator{ID: moderator.ID, NormalizedEmail: strings.ToLower(moderator.Email)})
	}
	if err := st.SyncModerators(ctx, moderators); err != nil {
		return err
	}
	return operation(ctx, st, cfg)
}
