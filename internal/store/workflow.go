// ABOUTME: Persists invited-poll workflow mutations under SQLite transactions.
// ABOUTME: It enforces ownership and optimistic versions inside the write boundary.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

var ErrConflict = errors.New("workflow conflict")

type Contact struct {
	ID              string
	DeliveryEmail   string
	NormalizedEmail string
}

type ElectorateParticipant struct {
	ID          string
	DisplayName string
	Contacts    []Contact
}

func (s *Store) ReplaceElectorate(ctx context.Context, ownerID, pollID string, expectedVersion int, participants []ElectorateParticipant) error {
	if s == nil || s.DB == nil || ownerID == "" || pollID == "" || expectedVersion < 1 {
		return ErrConflict
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin electorate replacement: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var state string
	var version int
	if err := tx.QueryRowContext(ctx, "SELECT state, version FROM polls WHERE id = ? AND owner_id = ?", pollID, ownerID).Scan(&state, &version); err != nil || state != "draft" || version != expectedVersion {
		return ErrConflict
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM contacts WHERE poll_id = ?", pollID); err != nil {
		return fmt.Errorf("delete poll contacts: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM participants WHERE poll_id = ?", pollID); err != nil {
		return fmt.Errorf("delete poll participants: %w", err)
	}
	for _, participant := range participants {
		if participant.ID == "" || len(participant.Contacts) == 0 {
			return fmt.Errorf("participant and contact IDs are required")
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO participants(id, poll_id, display_name) VALUES (?, ?, ?)", participant.ID, pollID, participant.DisplayName); err != nil {
			return fmt.Errorf("insert poll participant: %w", err)
		}
		for _, contact := range participant.Contacts {
			if contact.ID == "" || contact.DeliveryEmail == "" || contact.NormalizedEmail == "" {
				return fmt.Errorf("contact fields are required")
			}
			if _, err := tx.ExecContext(ctx, "INSERT INTO contacts(id, poll_id, participant_id, delivery_email, normalized_email) VALUES (?, ?, ?, ?, ?)", contact.ID, pollID, participant.ID, contact.DeliveryEmail, contact.NormalizedEmail); err != nil {
				return fmt.Errorf("insert poll contact: %w", err)
			}
		}
	}
	result, err := tx.ExecContext(ctx, "UPDATE polls SET version = version + 1 WHERE id = ? AND owner_id = ? AND state = 'draft' AND version = ?", pollID, ownerID, expectedVersion)
	if err != nil {
		return fmt.Errorf("update poll version: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return ErrConflict
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit electorate replacement: %w", err)
	}
	return nil
}

func (s *Store) ReplaceBallot(ctx context.Context, pollID, participantID string, expectedVersion int, preferencesJSON []byte, acceptedAt time.Time) error {
	if s == nil || s.DB == nil || pollID == "" || participantID == "" || expectedVersion < 0 || !json.Valid(preferencesJSON) {
		return ErrConflict
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin ballot replacement: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var state string
	var deadline int64
	if err := tx.QueryRowContext(ctx, "SELECT state, deadline FROM polls WHERE id = ?", pollID).Scan(&state, &deadline); err != nil || state != "open" || acceptedAt.Unix() >= deadline {
		return ErrConflict
	}
	var participant string
	if err := tx.QueryRowContext(ctx, "SELECT id FROM participants WHERE id = ? AND poll_id = ?", participantID, pollID).Scan(&participant); err != nil {
		return ErrConflict
	}
	var currentVersion int
	err = tx.QueryRowContext(ctx, "SELECT version FROM ballots WHERE poll_id = ? AND participant_id = ?", pollID, participantID).Scan(&currentVersion)
	if errors.Is(err, sql.ErrNoRows) {
		currentVersion = 0
	} else if err != nil {
		return fmt.Errorf("read ballot version: %w", err)
	}
	if currentVersion != expectedVersion {
		return ErrConflict
	}
	if currentVersion == 0 {
		_, err = tx.ExecContext(ctx, "INSERT INTO ballots(poll_id, participant_id, version, preferences_json, accepted_at) VALUES (?, ?, 1, ?, ?)", pollID, participantID, preferencesJSON, acceptedAt.Unix())
	} else {
		_, err = tx.ExecContext(ctx, "UPDATE ballots SET version = version + 1, preferences_json = ?, accepted_at = ? WHERE poll_id = ? AND participant_id = ? AND version = ?", preferencesJSON, acceptedAt.Unix(), pollID, participantID, expectedVersion)
	}
	if err != nil {
		return fmt.Errorf("replace ballot: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit ballot replacement: %w", err)
	}
	return nil
}
