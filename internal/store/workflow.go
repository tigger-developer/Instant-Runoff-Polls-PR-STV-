// ABOUTME: Persists invited-poll workflow mutations under SQLite transactions.
// ABOUTME: It enforces ownership and optimistic versions inside the write boundary.
package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

var (
	ErrConflict          = errors.New("workflow conflict")
	ErrCapacity          = errors.New("workflow capacity reached")
	ErrDeliveryHeld      = errors.New("delivery held")
	ErrDeliveryCancelled = errors.New("delivery cancelled")
	ErrDeliveryExhausted = errors.New("delivery attempts exhausted")
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

type SnapshotBuilder func(CloseWork) (CountSnapshot, error)

type ClaimedWork struct {
	ID             string
	PollID         string
	Kind           string
	Attempts       int
	ClaimToken     string
	ClaimExpiresAt int64
}

type CountWork struct {
	SnapshotID     string
	InputJSON      []byte
	DecisionsJSON  [][]byte
	ExistingResult []byte
}

type DeliveryAttempt struct {
	DeliveryID     string
	PollID         string
	ContactID      string
	ParticipantID  string
	PrincipalID    string
	GrantPayload   []byte
	RecipientEmail string
	MessageKind    string
	Question       string
	Winners        []string
	NoVotes        bool
	Attempts       int
}

type GrantMaterial struct {
	ID         string
	KeyID      string
	TokenHash  []byte
	ClaimsJSON []byte
	IssuedAt   time.Time
	ExpiresAt  time.Time
}

type CloseOption struct {
	ID    string
	Label string
}

type CloseParticipant struct {
	ID string
}

type CloseBallot struct {
	ParticipantID string
	Preferences   []string
	Version       int
	AcceptedAt    int64
}

type CloseWork struct {
	PollID       string
	OwnerID      string
	Question     string
	Deadline     int64
	Places       int
	State        string
	Version      int
	Options      []CloseOption
	Participants []CloseParticipant
	Ballots      []CloseBallot
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

func (s *Store) ReplaceBallot(ctx context.Context, pollID, participantID string, expectedVersion int, preferencesJSON []byte, now func() time.Time) error {
	if s == nil || s.DB == nil || pollID == "" || participantID == "" || expectedVersion < 0 || !json.Valid(preferencesJSON) || now == nil {
		return ErrConflict
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin ballot replacement: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	locked, err := tx.ExecContext(ctx, "UPDATE polls SET version=version WHERE id=?", pollID)
	if err != nil {
		return fmt.Errorf("lock poll for ballot replacement: %w", err)
	}
	changed, err := locked.RowsAffected()
	if err != nil || changed != 1 {
		return ErrConflict
	}
	acceptedAt := now()
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

func (s *Store) ClearBallot(ctx context.Context, pollID, participantID string, expectedVersion int, now func() time.Time) error {
	if s == nil || s.DB == nil || pollID == "" || participantID == "" || expectedVersion < 1 || now == nil {
		return ErrConflict
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin ballot clearing: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, "UPDATE polls SET version=version WHERE id=?", pollID); err != nil {
		return fmt.Errorf("lock poll for ballot clearing: %w", err)
	}
	clearedAt := now()
	var state string
	var deadline int64
	if err := tx.QueryRowContext(ctx, "SELECT state,deadline FROM polls WHERE id=?", pollID).Scan(&state, &deadline); err != nil || state != "open" || clearedAt.Unix() >= deadline {
		return ErrConflict
	}
	result, err := tx.ExecContext(ctx, "DELETE FROM ballots WHERE poll_id=? AND participant_id=? AND version=?", pollID, participantID, expectedVersion)
	if err != nil {
		return fmt.Errorf("clear ballot: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return ErrConflict
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit ballot clearing: %w", err)
	}
	return nil
}

func (s *Store) OpenPoll(ctx context.Context, ownerID, pollID string, expectedVersion int, closeWorkID string, invitations []InvitationWork, now time.Time) (bool, error) {
	if closeWorkID == "" {
		return false, ErrConflict
	}
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
		if _, err := tx.ExecContext(ctx, "INSERT INTO work_items(id,poll_id,kind,logical_key,due_at,status) VALUES (?,?,?,?,?,'pending')", invitation.WorkID, pollID, "delivery", "invitation:"+pollID+":"+invitation.ContactID, now.Unix()); err != nil {
			return false, fmt.Errorf("insert invitation work: %w", err)
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO deliveries(id,work_id,contact_id,recipient_email,message_kind,status,next_due) VALUES (?,?,?,?,?,'pending',?)", invitation.DeliveryID, invitation.WorkID, invitation.ContactID, recipient, "invitation", now.Unix()); err != nil {
			return false, fmt.Errorf("insert invitation delivery: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO work_items(id,poll_id,kind,logical_key,due_at,status) VALUES (?,?, 'close', ?,?,'pending')", closeWorkID, pollID, "close:"+pollID, deadline); err != nil {
		return false, fmt.Errorf("insert close work: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE polls SET state='open',version=version+1 WHERE id=? AND version=?", pollID, expectedVersion); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit poll opening: %w", err)
	}
	return true, nil
}

func (s *Store) ClosePoll(ctx context.Context, ownerID, pollID string, expectedVersion int, build SnapshotBuilder, workID string, now time.Time) (bool, bool, error) {
	if ownerID == "" || pollID == "" || expectedVersion < 1 || build == nil || workID == "" {
		return false, false, ErrConflict
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return false, false, fmt.Errorf("begin poll close: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	locked, err := tx.ExecContext(ctx, "UPDATE polls SET version=version WHERE id=? AND owner_id=?", pollID, ownerID)
	if err != nil {
		return false, false, fmt.Errorf("lock poll close: %w", err)
	}
	changed, err := locked.RowsAffected()
	if err != nil || changed != 1 {
		return false, false, ErrConflict
	}
	current, err := loadCloseWork(ctx, tx, ownerID, pollID)
	if err != nil {
		return false, false, err
	}
	if current.State == "closed" && current.Version == expectedVersion {
		return false, false, tx.Commit()
	}
	if (current.State != "open" && current.State != "paused") || current.Version != expectedVersion {
		return false, false, ErrConflict
	}
	noVotes := len(current.Ballots) == 0
	if noVotes {
		if err := insertAnnouncementWork(ctx, tx, pollID, "no-votes", now); err != nil {
			return false, false, err
		}
	} else {
		snapshot, err := build(current)
		if err != nil {
			return false, false, err
		}
		if snapshot.ID == "" || snapshot.SchemaVersion < 1 || snapshot.Rule == "" || snapshot.InputFingerprint == "" || !json.Valid(snapshot.InputJSON) {
			return false, false, ErrConflict
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO count_snapshots(id,poll_id,schema_version,rule,input_fingerprint,input_json,created_at) VALUES (?,?,?,?,?,?,?)", snapshot.ID, pollID, snapshot.SchemaVersion, snapshot.Rule, snapshot.InputFingerprint, snapshot.InputJSON, now.Unix()); err != nil {
			return false, false, fmt.Errorf("insert count snapshot: %w", err)
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO work_items(id,poll_id,kind,logical_key,due_at,status) VALUES (?,?, 'count', ?,?,'pending')", workID, pollID, "count:"+pollID, now.Unix()); err != nil {
			return false, false, fmt.Errorf("insert count work: %w", err)
		}
	}
	if err := cancelOpenInvitations(ctx, tx, pollID); err != nil {
		return false, false, err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE work_items SET status='succeeded',claim_token=NULL,claim_expires_at=NULL WHERE poll_id=? AND kind='close' AND status IN ('pending','claimed')", pollID); err != nil {
		return false, false, fmt.Errorf("finalize close work: %w", err)
	}
	countingStatus := "pending"
	if noVotes {
		countingStatus = "no_votes"
	}
	if _, err := tx.ExecContext(ctx, "UPDATE polls SET state='closed',counting_status=?,closed_at=?,version=version+1 WHERE id=? AND version=?", countingStatus, now.Unix(), pollID, expectedVersion); err != nil {
		return false, false, err
	}
	if err := tx.Commit(); err != nil {
		return false, false, fmt.Errorf("commit poll close: %w", err)
	}
	return true, noVotes, nil
}

func (s *Store) ConsumeModeratorGrant(ctx context.Context, grantHash []byte, moderatorID string, sessionHash, csrfHash, preAuthHash, replaceHash []byte, sessionExpiresAt, now time.Time) error {
	if len(grantHash) != 32 || len(sessionHash) != 32 || len(csrfHash) != 32 || len(preAuthHash) != 0 && len(preAuthHash) != 32 || len(replaceHash) != 0 && len(replaceHash) != 32 || moderatorID == "" || !sessionExpiresAt.After(now) {
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
	if err := rotateAuthenticationSessions(ctx, tx, preAuthHash, replaceHash, "moderator", now); err != nil {
		return err
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

func (s *Store) RecordLinkRequest(ctx context.Context, requestHash []byte, purpose, pollID string, now time.Time) (bool, error) {
	if len(requestHash) != 32 || (purpose != "moderator" && purpose != "participant") || (purpose == "participant" && pollID == "") {
		return false, ErrConflict
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin link request: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	windowStart := now.Add(-15 * time.Minute).Unix()
	if _, err := tx.ExecContext(ctx, "DELETE FROM link_requests WHERE submitted_at<=?", windowStart); err != nil {
		return false, fmt.Errorf("expire link requests: %w", err)
	}
	var globalCount int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM link_requests").Scan(&globalCount); err != nil {
		return false, fmt.Errorf("count link requests: %w", err)
	}
	if globalCount >= 1000 {
		return false, tx.Commit()
	}
	var recentMinute, acceptedWindow int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM link_requests WHERE request_hash=? AND purpose=? AND poll_id=? AND accepted=1 AND submitted_at>?", requestHash, purpose, pollID, now.Add(-time.Minute).Unix()).Scan(&recentMinute); err != nil {
		return false, fmt.Errorf("count recent link requests: %w", err)
	}
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM link_requests WHERE request_hash=? AND purpose=? AND poll_id=? AND accepted=1", requestHash, purpose, pollID).Scan(&acceptedWindow); err != nil {
		return false, fmt.Errorf("count accepted link requests: %w", err)
	}
	allowed := recentMinute == 0 && acceptedWindow < 5
	if _, err := tx.ExecContext(ctx, "INSERT INTO link_requests(request_hash,purpose,poll_id,submitted_at,accepted) VALUES (?,?,?,?,?)", requestHash, purpose, pollID, now.Unix(), allowed); err != nil {
		return false, fmt.Errorf("record link request: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit link request: %w", err)
	}
	return allowed, nil
}

func (s *Store) ClaimDueWork(ctx context.Context, kind, claimToken string, now time.Time) (*ClaimedWork, error) {
	if kind == "" || claimToken == "" {
		return nil, ErrConflict
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin work claim: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var work ClaimedWork
	err = tx.QueryRowContext(ctx, `SELECT id,COALESCE(poll_id,''),kind,attempts FROM work_items WHERE kind=? AND due_at<=? AND (status='pending' OR (status='claimed' AND claim_expires_at<=?)) ORDER BY due_at,id LIMIT 1`, kind, now.Unix(), now.Unix()).Scan(&work.ID, &work.PollID, &work.Kind, &work.Attempts)
	if errors.Is(err, sql.ErrNoRows) {
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("commit empty work claim: %w", err)
		}
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("select due work: %w", err)
	}
	work.ClaimToken = claimToken
	work.ClaimExpiresAt = now.Add(120 * time.Second).Unix()
	result, err := tx.ExecContext(ctx, `UPDATE work_items SET status='claimed',claim_token=?,claim_expires_at=? WHERE id=? AND (status='pending' OR (status='claimed' AND claim_expires_at<=?))`, work.ClaimToken, work.ClaimExpiresAt, work.ID, now.Unix())
	if err != nil {
		return nil, fmt.Errorf("claim due work: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return nil, ErrConflict
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit work claim: %w", err)
	}
	return &work, nil
}

func (s *Store) CompleteWork(ctx context.Context, workID, claimToken string, now time.Time) error {
	result, err := s.DB.ExecContext(ctx, `UPDATE work_items SET status='succeeded',claim_token=NULL,claim_expires_at=NULL WHERE id=? AND status='claimed' AND claim_token=? AND claim_expires_at>?`, workID, claimToken, now.Unix())
	if err != nil {
		return fmt.Errorf("complete work: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return ErrConflict
	}
	return nil
}

func (s *Store) FailWork(ctx context.Context, workID, claimToken string, now time.Time, failureClass string) error {
	if failureClass == "" {
		return ErrConflict
	}
	result, err := s.DB.ExecContext(ctx, `UPDATE work_items SET status='failed',failure_class=?,claim_token=NULL,claim_expires_at=NULL WHERE id=? AND status='claimed' AND claim_token=? AND claim_expires_at>?`, failureClass, workID, claimToken, now.Unix())
	if err != nil {
		return fmt.Errorf("fail work: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return ErrConflict
	}
	return nil
}

func (s *Store) RetryWork(ctx context.Context, workID, claimToken string, now, nextDue time.Time, failureClass string) error {
	if failureClass == "" || !nextDue.After(now) {
		return ErrConflict
	}
	result, err := s.DB.ExecContext(ctx, `UPDATE work_items SET status='pending',due_at=?,failure_class=?,claim_token=NULL,claim_expires_at=NULL WHERE id=? AND status='claimed' AND claim_token=? AND claim_expires_at>?`, nextDue.Unix(), failureClass, workID, claimToken, now.Unix())
	if err != nil {
		return fmt.Errorf("retry work: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return ErrConflict
	}
	return nil
}

func (s *Store) LoadCloseWork(ctx context.Context, workID, claimToken string, now time.Time) (CloseWork, error) {
	var work CloseWork
	err := s.DB.QueryRowContext(ctx, `SELECT poll.id,poll.owner_id,poll.question,poll.deadline,poll.places,poll.state,poll.version FROM work_items AS work JOIN polls AS poll ON poll.id=work.poll_id WHERE work.id=? AND work.kind='close' AND work.status='claimed' AND work.claim_token=? AND work.claim_expires_at>?`, workID, claimToken, now.Unix()).Scan(&work.PollID, &work.OwnerID, &work.Question, &work.Deadline, &work.Places, &work.State, &work.Version)
	if err != nil {
		return CloseWork{}, ErrConflict
	}
	optionRows, err := s.DB.QueryContext(ctx, "SELECT id,label FROM options WHERE poll_id=? ORDER BY display_order", work.PollID)
	if err != nil {
		return CloseWork{}, fmt.Errorf("read close options: %w", err)
	}
	for optionRows.Next() {
		var option CloseOption
		if err := optionRows.Scan(&option.ID, &option.Label); err != nil {
			_ = optionRows.Close()
			return CloseWork{}, fmt.Errorf("scan close option: %w", err)
		}
		work.Options = append(work.Options, option)
	}
	if err := finishRows(optionRows, "close options"); err != nil {
		return CloseWork{}, err
	}
	participantRows, err := s.DB.QueryContext(ctx, "SELECT id FROM participants WHERE poll_id=? ORDER BY id", work.PollID)
	if err != nil {
		return CloseWork{}, fmt.Errorf("read close participants: %w", err)
	}
	for participantRows.Next() {
		var participant CloseParticipant
		if err := participantRows.Scan(&participant.ID); err != nil {
			_ = participantRows.Close()
			return CloseWork{}, fmt.Errorf("scan close participant: %w", err)
		}
		work.Participants = append(work.Participants, participant)
	}
	if err := finishRows(participantRows, "close participants"); err != nil {
		return CloseWork{}, err
	}
	ballotRows, err := s.DB.QueryContext(ctx, "SELECT participant_id,preferences_json,version,accepted_at FROM ballots WHERE poll_id=? ORDER BY participant_id", work.PollID)
	if err != nil {
		return CloseWork{}, fmt.Errorf("read close ballots: %w", err)
	}
	for ballotRows.Next() {
		var ballot CloseBallot
		var preferences []byte
		if err := ballotRows.Scan(&ballot.ParticipantID, &preferences, &ballot.Version, &ballot.AcceptedAt); err != nil || json.Unmarshal(preferences, &ballot.Preferences) != nil {
			_ = ballotRows.Close()
			return CloseWork{}, ErrConflict
		}
		work.Ballots = append(work.Ballots, ballot)
	}
	if err := finishRows(ballotRows, "close ballots"); err != nil {
		return CloseWork{}, err
	}
	return work, nil
}

func cancelOpenInvitations(ctx context.Context, tx *sql.Tx, pollID string) error {
	if _, err := tx.ExecContext(ctx, "UPDATE deliveries SET status='cancelled',smtp_outcome='poll closed' WHERE message_kind IN ('invitation','participant_return') AND status IN ('pending','retrying') AND work_id IN (SELECT id FROM work_items WHERE poll_id=? AND kind='delivery')", pollID); err != nil {
		return fmt.Errorf("cancel invitations: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE work_items SET status='succeeded',claim_token=NULL,claim_expires_at=NULL WHERE poll_id=? AND kind='delivery' AND status IN ('pending','claimed') AND id IN (SELECT work_id FROM deliveries WHERE message_kind IN ('invitation','participant_return') AND status='cancelled')", pollID); err != nil {
		return fmt.Errorf("finalize cancelled invitation work: %w", err)
	}
	return nil
}

func (s *Store) BeginDeliveryAttempt(ctx context.Context, workID, claimToken string, now time.Time) (DeliveryAttempt, error) {
	if workID == "" || claimToken == "" {
		return DeliveryAttempt{}, ErrConflict
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return DeliveryAttempt{}, fmt.Errorf("begin delivery attempt: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var attempt DeliveryAttempt
	var state string
	var deadline int64
	var countingStatus string
	var resultJSON []byte
	err = tx.QueryRowContext(ctx, `SELECT delivery.id,COALESCE(work.poll_id,''),COALESCE(delivery.contact_id,''),COALESCE(contact.participant_id,''),COALESCE(grant.principal_id,''),COALESCE(grant.claims_json,''),delivery.recipient_email,delivery.message_kind,COALESCE(poll.question,''),COALESCE(poll.state,''),COALESCE(poll.deadline,0),COALESCE(poll.counting_status,''),COALESCE((SELECT result_json FROM count_results WHERE snapshot_id=(SELECT id FROM count_snapshots WHERE poll_id=poll.id)),''),work.attempts FROM work_items AS work JOIN deliveries AS delivery ON delivery.work_id=work.id LEFT JOIN grants AS grant ON grant.id=delivery.grant_id LEFT JOIN polls AS poll ON poll.id=work.poll_id LEFT JOIN contacts AS contact ON contact.id=delivery.contact_id WHERE work.id=? AND work.kind='delivery' AND work.status='claimed' AND work.claim_token=? AND work.claim_expires_at>?`, workID, claimToken, now.Unix()).Scan(&attempt.DeliveryID, &attempt.PollID, &attempt.ContactID, &attempt.ParticipantID, &attempt.PrincipalID, &attempt.GrantPayload, &attempt.RecipientEmail, &attempt.MessageKind, &attempt.Question, &state, &deadline, &countingStatus, &resultJSON, &attempt.Attempts)
	if err != nil {
		return DeliveryAttempt{}, ErrConflict
	}
	isVotingAccess := attempt.MessageKind == "invitation" || attempt.MessageKind == "participant_return"
	if isVotingAccess && state == "paused" && now.Unix() < deadline {
		if err := deferClaimedDelivery(ctx, tx, workID, claimToken, now, now.Add(time.Minute)); err != nil {
			return DeliveryAttempt{}, err
		}
		if err := tx.Commit(); err != nil {
			return DeliveryAttempt{}, fmt.Errorf("commit held delivery: %w", err)
		}
		return DeliveryAttempt{}, ErrDeliveryHeld
	}
	if isVotingAccess && (state != "open" || now.Unix() >= deadline) {
		if err := cancelClaimedDelivery(ctx, tx, workID, claimToken, now); err != nil {
			return DeliveryAttempt{}, err
		}
		if err := tx.Commit(); err != nil {
			return DeliveryAttempt{}, fmt.Errorf("commit cancelled delivery: %w", err)
		}
		return DeliveryAttempt{}, ErrDeliveryCancelled
	}
	if attempt.MessageKind == "announcement" {
		attempt.NoVotes = countingStatus == "no_votes"
		if !attempt.NoVotes {
			var result struct {
				Winners []string `json:"winners"`
			}
			if countingStatus != "succeeded" || json.Unmarshal(resultJSON, &result) != nil || len(result.Winners) == 0 {
				return DeliveryAttempt{}, ErrConflict
			}
			for _, optionID := range result.Winners {
				var label string
				if err := tx.QueryRowContext(ctx, "SELECT label FROM options WHERE poll_id=? AND id=?", attempt.PollID, optionID).Scan(&label); err != nil {
					return DeliveryAttempt{}, ErrConflict
				}
				attempt.Winners = append(attempt.Winners, label)
			}
		}
	}
	if attempt.Attempts >= 6 {
		if _, err := tx.ExecContext(ctx, "UPDATE deliveries SET status='failed',smtp_outcome='SMTP attempt interrupted' WHERE id=?", attempt.DeliveryID); err != nil {
			return DeliveryAttempt{}, fmt.Errorf("finalize interrupted delivery: %w", err)
		}
		result, err := tx.ExecContext(ctx, "UPDATE work_items SET status='failed',failure_class='SMTP attempt interrupted',claim_token=NULL,claim_expires_at=NULL WHERE id=? AND status='claimed' AND claim_token=? AND claim_expires_at>?", workID, claimToken, now.Unix())
		if err != nil {
			return DeliveryAttempt{}, fmt.Errorf("finalize interrupted delivery work: %w", err)
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			return DeliveryAttempt{}, ErrConflict
		}
		if err := tx.Commit(); err != nil {
			return DeliveryAttempt{}, fmt.Errorf("commit interrupted delivery: %w", err)
		}
		return DeliveryAttempt{}, ErrDeliveryExhausted
	}
	attempt.Attempts++
	result, err := tx.ExecContext(ctx, "UPDATE work_items SET attempts=? WHERE id=? AND status='claimed' AND claim_token=? AND claim_expires_at>?", attempt.Attempts, workID, claimToken, now.Unix())
	if err != nil {
		return DeliveryAttempt{}, fmt.Errorf("persist delivery attempt: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return DeliveryAttempt{}, ErrConflict
	}
	if _, err := tx.ExecContext(ctx, "UPDATE deliveries SET status='in_flight' WHERE id=?", attempt.DeliveryID); err != nil {
		return DeliveryAttempt{}, fmt.Errorf("mark delivery in flight: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return DeliveryAttempt{}, fmt.Errorf("commit delivery attempt: %w", err)
	}
	return attempt, nil
}

func (s *Store) PrepareDeliveryGrant(ctx context.Context, workID, claimToken, activeKeyID string, now time.Time, candidate GrantMaterial) ([]byte, error) {
	if workID == "" || claimToken == "" || activeKeyID == "" || candidate.ID == "" || candidate.KeyID != activeKeyID || len(candidate.TokenHash) != 32 || !json.Valid(candidate.ClaimsJSON) || !candidate.ExpiresAt.After(candidate.IssuedAt) {
		return nil, ErrConflict
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin delivery grant: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var pollID, contactID, participantID string
	var existingID, existingKey string
	var existingPayload []byte
	var existingExpiry int64
	err = tx.QueryRowContext(ctx, `SELECT work.poll_id,delivery.contact_id,contact.participant_id,COALESCE(grant.id,''),COALESCE(grant.key_id,''),COALESCE(grant.claims_json,''),COALESCE(grant.expires_at,0) FROM work_items AS work JOIN deliveries AS delivery ON delivery.work_id=work.id JOIN contacts AS contact ON contact.id=delivery.contact_id LEFT JOIN grants AS grant ON grant.id=delivery.grant_id AND grant.revoked_at IS NULL WHERE work.id=? AND work.kind='delivery' AND delivery.message_kind='invitation' AND work.status='claimed' AND work.claim_token=? AND work.claim_expires_at>?`, workID, claimToken, now.Unix()).Scan(&pollID, &contactID, &participantID, &existingID, &existingKey, &existingPayload, &existingExpiry)
	if err != nil {
		return nil, ErrConflict
	}
	if existingID != "" && existingKey == activeKeyID && existingExpiry >= now.Add(5*time.Minute).Unix() {
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("commit existing delivery grant: %w", err)
		}
		return append([]byte(nil), existingPayload...), nil
	}
	if existingID != "" {
		if _, err := tx.ExecContext(ctx, "UPDATE grants SET revoked_at=? WHERE id=? AND revoked_at IS NULL", now.Unix(), existingID); err != nil {
			return nil, fmt.Errorf("revoke delivery grant: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO grants(id,purpose,principal_id,poll_id,contact_id,key_id,token_hash,claims_json,issued_at,expires_at) VALUES (?,'participant',?,?,?,?,?,?,?,?)`, candidate.ID, participantID, pollID, contactID, candidate.KeyID, candidate.TokenHash, candidate.ClaimsJSON, candidate.IssuedAt.Unix(), candidate.ExpiresAt.Unix()); err != nil {
		return nil, fmt.Errorf("insert delivery grant: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE deliveries SET grant_id=? WHERE work_id=?", candidate.ID, workID); err != nil {
		return nil, fmt.Errorf("attach delivery grant: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit delivery grant: %w", err)
	}
	return append([]byte(nil), candidate.ClaimsJSON...), nil
}

func deferClaimedDelivery(ctx context.Context, tx *sql.Tx, workID, claimToken string, now, nextDue time.Time) error {
	result, err := tx.ExecContext(ctx, "UPDATE work_items SET status='pending',due_at=?,claim_token=NULL,claim_expires_at=NULL WHERE id=? AND status='claimed' AND claim_token=? AND claim_expires_at>?", nextDue.Unix(), workID, claimToken, now.Unix())
	if err != nil {
		return fmt.Errorf("defer held delivery: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return ErrConflict
	}
	if _, err := tx.ExecContext(ctx, "UPDATE deliveries SET status='pending',next_due=? WHERE work_id=?", nextDue.Unix(), workID); err != nil {
		return fmt.Errorf("record held delivery: %w", err)
	}
	return nil
}

func cancelClaimedDelivery(ctx context.Context, tx *sql.Tx, workID, claimToken string, now time.Time) error {
	result, err := tx.ExecContext(ctx, "UPDATE work_items SET status='succeeded',claim_token=NULL,claim_expires_at=NULL WHERE id=? AND status='claimed' AND claim_token=? AND claim_expires_at>?", workID, claimToken, now.Unix())
	if err != nil {
		return fmt.Errorf("finalize cancelled delivery: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return ErrConflict
	}
	if _, err := tx.ExecContext(ctx, "UPDATE deliveries SET status='cancelled',smtp_outcome='ineligible' WHERE work_id=?", workID); err != nil {
		return fmt.Errorf("record cancelled delivery: %w", err)
	}
	return nil
}

func (s *Store) RetryDelivery(ctx context.Context, workID, claimToken string, now, nextDue time.Time, failureClass string) error {
	if workID == "" || claimToken == "" || failureClass == "" || !nextDue.After(now) {
		return ErrConflict
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin delivery retry: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `UPDATE deliveries SET status='retrying',next_due=?,smtp_outcome=? WHERE work_id=? AND EXISTS (SELECT 1 FROM work_items WHERE id=? AND kind='delivery' AND status='claimed' AND claim_token=? AND claim_expires_at>?)`, nextDue.Unix(), failureClass, workID, workID, claimToken, now.Unix())
	if err != nil {
		return fmt.Errorf("record delivery retry: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return ErrConflict
	}
	result, err = tx.ExecContext(ctx, `UPDATE work_items SET status='pending',due_at=?,failure_class=?,claim_token=NULL,claim_expires_at=NULL WHERE id=? AND status='claimed' AND claim_token=? AND claim_expires_at>?`, nextDue.Unix(), failureClass, workID, claimToken, now.Unix())
	if err != nil {
		return fmt.Errorf("release delivery retry: %w", err)
	}
	changed, err = result.RowsAffected()
	if err != nil || changed != 1 {
		return ErrConflict
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit delivery retry: %w", err)
	}
	return nil
}

func (s *Store) AcceptDelivery(ctx context.Context, workID, claimToken string, now time.Time) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin accepted delivery: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `UPDATE deliveries SET status='smtp_accepted',smtp_outcome='accepted' WHERE work_id=? AND EXISTS (SELECT 1 FROM work_items WHERE id=? AND kind='delivery' AND status='claimed' AND claim_token=? AND claim_expires_at>?)`, workID, workID, claimToken, now.Unix())
	if err != nil {
		return fmt.Errorf("accept delivery: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return ErrConflict
	}
	result, err = tx.ExecContext(ctx, `UPDATE work_items SET status='succeeded',claim_token=NULL,claim_expires_at=NULL WHERE id=? AND kind='delivery' AND status='claimed' AND claim_token=? AND claim_expires_at>?`, workID, claimToken, now.Unix())
	if err != nil {
		return fmt.Errorf("complete accepted delivery work: %w", err)
	}
	changed, err = result.RowsAffected()
	if err != nil || changed != 1 {
		return ErrConflict
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit accepted delivery: %w", err)
	}
	return nil
}

func (s *Store) FailDelivery(ctx context.Context, workID, claimToken string, now time.Time, failureClass string) error {
	if failureClass == "" {
		return ErrConflict
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin terminal delivery failure: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `UPDATE deliveries SET status='failed',smtp_outcome=? WHERE work_id=? AND EXISTS (SELECT 1 FROM work_items WHERE id=? AND kind='delivery' AND status='claimed' AND claim_token=? AND claim_expires_at>?)`, failureClass, workID, workID, claimToken, now.Unix())
	if err != nil {
		return fmt.Errorf("record terminal delivery failure: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return ErrConflict
	}
	result, err = tx.ExecContext(ctx, `UPDATE work_items SET status='failed',failure_class=?,claim_token=NULL,claim_expires_at=NULL WHERE id=? AND status='claimed' AND claim_token=? AND claim_expires_at>?`, failureClass, workID, claimToken, now.Unix())
	if err != nil {
		return fmt.Errorf("finalize failed delivery work: %w", err)
	}
	changed, err = result.RowsAffected()
	if err != nil || changed != 1 {
		return ErrConflict
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit terminal delivery failure: %w", err)
	}
	return nil
}

func (s *Store) LoadCountWork(ctx context.Context, workID, claimToken string, now time.Time) (CountWork, error) {
	var work CountWork
	err := s.DB.QueryRowContext(ctx, `SELECT snapshots.id,snapshots.input_json,COALESCE(results.result_json,'') FROM work_items AS work JOIN count_snapshots AS snapshots ON snapshots.poll_id=work.poll_id LEFT JOIN count_results AS results ON results.snapshot_id=snapshots.id WHERE work.id=? AND work.kind='count' AND work.status='claimed' AND work.claim_token=? AND work.claim_expires_at>?`, workID, claimToken, now.Unix()).Scan(&work.SnapshotID, &work.InputJSON, &work.ExistingResult)
	if err != nil {
		return CountWork{}, ErrConflict
	}
	rows, err := s.DB.QueryContext(ctx, "SELECT decision_json FROM count_decisions WHERE snapshot_id=? ORDER BY sequence", work.SnapshotID)
	if err != nil {
		return CountWork{}, fmt.Errorf("read count decisions: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var decision []byte
		if err := rows.Scan(&decision); err != nil {
			return CountWork{}, fmt.Errorf("scan count decision: %w", err)
		}
		work.DecisionsJSON = append(work.DecisionsJSON, append([]byte(nil), decision...))
	}
	if err := rows.Err(); err != nil {
		return CountWork{}, fmt.Errorf("iterate count decisions: %w", err)
	}
	return work, nil
}

func (s *Store) CommitCountDecision(ctx context.Context, workID, claimToken string, now time.Time, sequence int, requestFingerprint string, decisionJSON []byte) (bool, error) {
	if sequence < 1 || requestFingerprint == "" || !json.Valid(decisionJSON) {
		return false, ErrConflict
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin count decision: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	snapshotID, err := claimedSnapshotID(ctx, tx, workID, claimToken, now)
	if err != nil {
		return false, err
	}
	var existingFingerprint string
	var existingJSON []byte
	err = tx.QueryRowContext(ctx, "SELECT request_fingerprint,decision_json FROM count_decisions WHERE snapshot_id=? AND sequence=?", snapshotID, sequence).Scan(&existingFingerprint, &existingJSON)
	if err == nil {
		if existingFingerprint != requestFingerprint || !bytes.Equal(existingJSON, decisionJSON) {
			return false, ErrConflict
		}
		return false, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("read existing count decision: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO count_decisions(snapshot_id,sequence,request_fingerprint,decision_json) VALUES (?,?,?,?)", snapshotID, sequence, requestFingerprint, decisionJSON); err != nil {
		return false, fmt.Errorf("insert count decision: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit count decision: %w", err)
	}
	return true, nil
}

func (s *Store) CommitCountResult(ctx context.Context, workID, claimToken string, now time.Time, resultJSON []byte) (bool, error) {
	if !json.Valid(resultJSON) {
		return false, ErrConflict
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin count result: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	snapshotID, err := claimedSnapshotID(ctx, tx, workID, claimToken, now)
	if err != nil {
		return false, err
	}
	var existing []byte
	err = tx.QueryRowContext(ctx, "SELECT result_json FROM count_results WHERE snapshot_id=?", snapshotID).Scan(&existing)
	if err == nil {
		if !bytes.Equal(existing, resultJSON) {
			return false, ErrConflict
		}
		if err := completeClaimedCountWork(ctx, tx, workID, claimToken, now); err != nil {
			return false, err
		}
		return false, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("read existing count result: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO count_results(snapshot_id,result_json,committed_at) VALUES (?,?,?)", snapshotID, resultJSON, now.Unix()); err != nil {
		return false, fmt.Errorf("insert count result: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE polls SET counting_status='succeeded' WHERE id=(SELECT poll_id FROM count_snapshots WHERE id=?)", snapshotID); err != nil {
		return false, fmt.Errorf("mark count succeeded: %w", err)
	}
	var pollID string
	if err := tx.QueryRowContext(ctx, "SELECT poll_id FROM count_snapshots WHERE id=?", snapshotID).Scan(&pollID); err != nil {
		return false, fmt.Errorf("read counted poll: %w", err)
	}
	if err := insertAnnouncementWork(ctx, tx, pollID, snapshotID, now); err != nil {
		return false, err
	}
	if err := completeClaimedCountWork(ctx, tx, workID, claimToken, now); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit count result: %w", err)
	}
	return true, nil
}

func completeClaimedCountWork(ctx context.Context, tx *sql.Tx, workID, claimToken string, now time.Time) error {
	result, err := tx.ExecContext(ctx, "UPDATE work_items SET status='succeeded',claim_token=NULL,claim_expires_at=NULL WHERE id=? AND kind='count' AND status='claimed' AND claim_token=? AND claim_expires_at>?", workID, claimToken, now.Unix())
	if err != nil {
		return fmt.Errorf("complete count work: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return ErrConflict
	}
	return nil
}

func (s *Store) MarkCountFailed(ctx context.Context, workID, claimToken string, now time.Time) error {
	result, err := s.DB.ExecContext(ctx, `UPDATE polls SET counting_status='failed' WHERE id=(SELECT poll_id FROM work_items WHERE id=? AND kind='count' AND status='claimed' AND claim_token=? AND claim_expires_at>?)`, workID, claimToken, now.Unix())
	if err != nil {
		return fmt.Errorf("mark count failed: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return ErrConflict
	}
	return nil
}

func insertAnnouncementWork(ctx context.Context, tx *sql.Tx, pollID, resultKey string, now time.Time) error {
	var announce bool
	if err := tx.QueryRowContext(ctx, "SELECT announce FROM polls WHERE id=?", pollID).Scan(&announce); err != nil {
		return fmt.Errorf("read announcement preference: %w", err)
	}
	if !announce {
		return nil
	}
	rows, err := tx.QueryContext(ctx, "SELECT id,delivery_email FROM contacts WHERE poll_id=? ORDER BY id", pollID)
	if err != nil {
		return fmt.Errorf("read announcement contacts: %w", err)
	}
	type recipient struct{ contactID, email string }
	var recipients []recipient
	for rows.Next() {
		var contactID, recipient string
		if err := rows.Scan(&contactID, &recipient); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan announcement contact: %w", err)
		}
		recipients = append(recipients, struct{ contactID, email string }{contactID: contactID, email: recipient})
	}
	if err := finishRows(rows, "announcement contacts"); err != nil {
		return err
	}
	for _, recipient := range recipients {
		workID := "announcement-work:" + resultKey + ":" + recipient.contactID
		deliveryID := "announcement-delivery:" + resultKey + ":" + recipient.contactID
		if _, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO work_items(id,poll_id,kind,logical_key,due_at,status) VALUES (?,?,'delivery',? ,?,'pending')", workID, pollID, "announcement:"+resultKey+":"+recipient.contactID, now.Unix()); err != nil {
			return fmt.Errorf("insert announcement work: %w", err)
		}
		if _, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO deliveries(id,work_id,contact_id,recipient_email,message_kind,status,next_due) VALUES (?,?,?,?, 'announcement','pending',?)", deliveryID, workID, recipient.contactID, recipient.email, now.Unix()); err != nil {
			return fmt.Errorf("insert announcement delivery: %w", err)
		}
	}
	return nil
}

type rowResult interface {
	Err() error
	Close() error
}

func finishRows(rows rowResult, label string) error {
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("iterate %s: %w", label, err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close %s: %w", label, err)
	}
	return nil
}

func claimedSnapshotID(ctx context.Context, tx *sql.Tx, workID, claimToken string, now time.Time) (string, error) {
	var snapshotID string
	if err := tx.QueryRowContext(ctx, `SELECT snapshots.id FROM work_items AS work JOIN count_snapshots AS snapshots ON snapshots.poll_id=work.poll_id WHERE work.id=? AND work.kind='count' AND work.status='claimed' AND work.claim_token=? AND work.claim_expires_at>?`, workID, claimToken, now.Unix()).Scan(&snapshotID); err != nil {
		return "", ErrConflict
	}
	return snapshotID, nil
}
