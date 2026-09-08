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

var (
	ErrConflict = errors.New("workflow conflict")
	ErrCapacity = errors.New("workflow capacity reached")
)

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

type InvitationWork struct {
	WorkID     string
	DeliveryID string
	ContactID  string
}

type CountSnapshot struct {
	ID               string
	SchemaVersion    int
	Rule             string
	InputFingerprint string
	InputJSON        []byte
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

func (s *Store) OpenPoll(ctx context.Context, ownerID, pollID string, expectedVersion int, invitations []InvitationWork, now time.Time) (bool, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin poll opening: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var state string
	var version int
	var deadline int64
	var places int
	if err := tx.QueryRowContext(ctx, "SELECT state,version,deadline,places FROM polls WHERE id=? AND owner_id=?", pollID, ownerID).Scan(&state, &version, &deadline, &places); err != nil {
		return false, ErrConflict
	}
	if state == "open" && version == expectedVersion {
		return false, nil
	}
	if state != "draft" || version != expectedVersion || now.Unix() >= deadline {
		return false, ErrConflict
	}
	var optionCount, participantCount, contactCount int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM options WHERE poll_id=?", pollID).Scan(&optionCount); err != nil {
		return false, err
	}
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM participants WHERE poll_id=?", pollID).Scan(&participantCount); err != nil {
		return false, err
	}
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM contacts WHERE poll_id=?", pollID).Scan(&contactCount); err != nil {
		return false, err
	}
	if optionCount < 2 || places < 1 || places > optionCount || participantCount < 1 || contactCount != len(invitations) {
		return false, ErrConflict
	}
	for _, invitation := range invitations {
		var recipient string
		if invitation.WorkID == "" || invitation.DeliveryID == "" || tx.QueryRowContext(ctx, "SELECT delivery_email FROM contacts WHERE id=? AND poll_id=?", invitation.ContactID, pollID).Scan(&recipient) != nil {
			return false, ErrConflict
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO work_items(id,poll_id,kind,logical_key,due_at,status) VALUES (?,?,?,?,?,'pending')", invitation.WorkID, pollID, "invitation", "invitation:"+pollID+":"+invitation.ContactID, now.Unix()); err != nil {
			return false, fmt.Errorf("insert invitation work: %w", err)
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO deliveries(id,work_id,contact_id,recipient_email,message_kind,status,next_due) VALUES (?,?,?,?,?,'pending',?)", invitation.DeliveryID, invitation.WorkID, invitation.ContactID, recipient, "invitation", now.Unix()); err != nil {
			return false, fmt.Errorf("insert invitation delivery: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, "UPDATE polls SET state='open',version=version+1 WHERE id=? AND version=?", pollID, expectedVersion); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit poll opening: %w", err)
	}
	return true, nil
}

func (s *Store) ClosePoll(ctx context.Context, ownerID, pollID string, expectedVersion int, snapshot CountSnapshot, workID string, now time.Time) (bool, error) {
	if snapshot.ID == "" || snapshot.SchemaVersion < 1 || snapshot.Rule == "" || snapshot.InputFingerprint == "" || !json.Valid(snapshot.InputJSON) || workID == "" {
		return false, ErrConflict
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin poll close: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var state string
	var version int
	if err := tx.QueryRowContext(ctx, "SELECT state,version FROM polls WHERE id=? AND owner_id=?", pollID, ownerID).Scan(&state, &version); err != nil {
		return false, ErrConflict
	}
	if state == "closed" && version == expectedVersion {
		return false, nil
	}
	if (state != "open" && state != "paused") || version != expectedVersion {
		return false, ErrConflict
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO count_snapshots(id,poll_id,schema_version,rule,input_fingerprint,input_json,created_at) VALUES (?,?,?,?,?,?,?)", snapshot.ID, pollID, snapshot.SchemaVersion, snapshot.Rule, snapshot.InputFingerprint, snapshot.InputJSON, now.Unix()); err != nil {
		return false, fmt.Errorf("insert count snapshot: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO work_items(id,poll_id,kind,logical_key,due_at,status) VALUES (?,?, 'count', ?,?,'pending')", workID, pollID, "count:"+pollID, now.Unix()); err != nil {
		return false, fmt.Errorf("insert count work: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE deliveries SET status='cancelled' WHERE status IN ('pending','retrying') AND work_id IN (SELECT id FROM work_items WHERE poll_id=? AND kind='invitation')", pollID); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE polls SET state='closed',closed_at=?,version=version+1 WHERE id=? AND version=?", now.Unix(), pollID, expectedVersion); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit poll close: %w", err)
	}
	return true, nil
}

func (s *Store) ConsumeModeratorGrant(ctx context.Context, grantHash []byte, moderatorID string, sessionHash, csrfHash []byte, sessionExpiresAt, now time.Time) error {
	if len(grantHash) != 32 || len(sessionHash) != 32 || len(csrfHash) != 32 || moderatorID == "" || !sessionExpiresAt.After(now) {
		return ErrConflict
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin moderator grant consumption: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var configured string
	if err := tx.QueryRowContext(ctx, "SELECT id FROM moderators WHERE id=?", moderatorID).Scan(&configured); err != nil {
		return ErrConflict
	}
	result, err := tx.ExecContext(ctx, "UPDATE grants SET consumed_at=? WHERE token_hash=? AND purpose='moderator' AND principal_id=? AND consumed_at IS NULL AND revoked_at IS NULL AND expires_at>?", now.Unix(), grantHash, moderatorID, now.Unix())
	if err != nil {
		return fmt.Errorf("consume moderator grant: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return ErrConflict
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO sessions(token_hash,purpose,principal_id,csrf_hash,expires_at) VALUES (?,'moderator',?,?,?)", sessionHash, moderatorID, csrfHash, sessionExpiresAt.Unix()); err != nil {
		return fmt.Errorf("create moderator session: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit moderator grant consumption: %w", err)
	}
	return nil
}

func (s *Store) CreatePreAuthSession(ctx context.Context, sessionHash, csrfHash []byte, expiresAt, now time.Time) error {
	if len(sessionHash) != 32 || len(csrfHash) != 32 || !expiresAt.After(now) {
		return ErrConflict
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin pre-authentication session: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, "DELETE FROM sessions WHERE expires_at<=?", now.Unix()); err != nil {
		return fmt.Errorf("remove expired sessions: %w", err)
	}
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM sessions WHERE purpose='preauth' AND revoked_at IS NULL AND expires_at>?", now.Unix()).Scan(&count); err != nil {
		return fmt.Errorf("count pre-authentication sessions: %w", err)
	}
	if count >= 1000 {
		return ErrCapacity
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO sessions(token_hash,purpose,principal_id,csrf_hash,expires_at) VALUES (?,'preauth','',?,?)", sessionHash, csrfHash, expiresAt.Unix()); err != nil {
		return fmt.Errorf("create pre-authentication session: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit pre-authentication session: %w", err)
	}
	return nil
}
