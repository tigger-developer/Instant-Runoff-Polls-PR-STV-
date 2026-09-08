// ABOUTME: Persists owner-scoped poll definitions and lifecycle transitions.
// ABOUTME: It applies optimistic versions inside each SQLite write boundary.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type PollOption struct {
	ID    string
	Label string
}

type PollRecord struct {
	ID             string
	OwnerID        string
	Question       string
	Deadline       time.Time
	DisplayOffset  string
	Places         int
	Announce       bool
	State          string
	CountingStatus string
	Version        int
	CreatedAt      time.Time
	Options        []PollOption
}

type DraftPoll struct {
	Question      string
	Deadline      time.Time
	DisplayOffset string
	Places        int
	Announce      bool
	Options       []PollOption
}

type ParticipantPoll struct {
	Poll            PollRecord
	ParticipantID   string
	ParticipantName string
	Preferences     []string
	BallotVersion   int
}

func (s *Store) CreateDraftPoll(ctx context.Context, ownerID, pollID string, draft DraftPoll, now time.Time) error {
	if ownerID == "" || pollID == "" || draft.Question == "" || draft.Places < 1 || !draft.Deadline.After(now) || len(draft.Options) < 2 {
		return ErrConflict
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin draft creation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var configured string
	if err := tx.QueryRowContext(ctx, "SELECT id FROM moderators WHERE id=?", ownerID).Scan(&configured); err != nil {
		return ErrConflict
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO polls(id,owner_id,question,deadline,display_offset,places,announce,state,version,created_at) VALUES (?,?,?,?,?,?,?,'draft',1,?)", pollID, ownerID, draft.Question, draft.Deadline.Unix(), draft.DisplayOffset, draft.Places, draft.Announce, now.Unix()); err != nil {
		return fmt.Errorf("insert draft poll: %w", err)
	}
	if err := replaceOptions(ctx, tx, pollID, draft.Options); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit draft creation: %w", err)
	}
	return nil
}

func (s *Store) UpdateDraftPoll(ctx context.Context, ownerID, pollID string, expectedVersion int, draft DraftPoll) error {
	if ownerID == "" || pollID == "" || expectedVersion < 1 || draft.Question == "" || draft.Places < 1 || len(draft.Options) < 2 {
		return ErrConflict
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin draft update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, "UPDATE polls SET question=?,deadline=?,display_offset=?,places=?,announce=?,version=version+1 WHERE id=? AND owner_id=? AND state='draft' AND version=?", draft.Question, draft.Deadline.Unix(), draft.DisplayOffset, draft.Places, draft.Announce, pollID, ownerID, expectedVersion)
	if err != nil {
		return fmt.Errorf("update draft poll: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return ErrConflict
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM options WHERE poll_id=?", pollID); err != nil {
		return fmt.Errorf("replace draft options: %w", err)
	}
	if err := replaceOptions(ctx, tx, pollID, draft.Options); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit draft update: %w", err)
	}
	return nil
}

func replaceOptions(ctx context.Context, tx *sql.Tx, pollID string, options []PollOption) error {
	seen := make(map[string]struct{}, len(options))
	for index, option := range options {
		if option.ID == "" || option.Label == "" {
			return ErrConflict
		}
		if _, exists := seen[option.ID]; exists {
			return ErrConflict
		}
		seen[option.ID] = struct{}{}
		if _, err := tx.ExecContext(ctx, "INSERT INTO options(poll_id,id,label,display_order) VALUES (?,?,?,?)", pollID, option.ID, option.Label, index+1); err != nil {
			return fmt.Errorf("insert draft option: %w", err)
		}
	}
	return nil
}

func (s *Store) OwnedPoll(ctx context.Context, ownerID, pollID string) (PollRecord, error) {
	var poll PollRecord
	var deadline, createdAt int64
	err := s.DB.QueryRowContext(ctx, "SELECT id,owner_id,question,deadline,display_offset,places,announce,state,counting_status,version,created_at FROM polls WHERE id=? AND owner_id=?", pollID, ownerID).Scan(&poll.ID, &poll.OwnerID, &poll.Question, &deadline, &poll.DisplayOffset, &poll.Places, &poll.Announce, &poll.State, &poll.CountingStatus, &poll.Version, &createdAt)
	if err != nil {
		return PollRecord{}, ErrConflict
	}
	poll.Deadline = time.Unix(deadline, 0).UTC()
	poll.CreatedAt = time.Unix(createdAt, 0).UTC()
	options, err := s.pollOptions(ctx, pollID)
	if err != nil {
		return PollRecord{}, err
	}
	poll.Options = options
	return poll, nil
}

func (s *Store) OwnedPolls(ctx context.Context, ownerID string, offset int) ([]PollRecord, error) {
	if ownerID == "" || offset < 0 {
		return nil, ErrConflict
	}
	rows, err := s.DB.QueryContext(ctx, "SELECT id,owner_id,question,deadline,display_offset,places,announce,state,counting_status,version,created_at FROM polls WHERE owner_id=? ORDER BY created_at,id LIMIT 50 OFFSET ?", ownerID, offset)
	if err != nil {
		return nil, fmt.Errorf("list owned polls: %w", err)
	}
	defer rows.Close()
	var polls []PollRecord
	for rows.Next() {
		var poll PollRecord
		var deadline, createdAt int64
		if err := rows.Scan(&poll.ID, &poll.OwnerID, &poll.Question, &deadline, &poll.DisplayOffset, &poll.Places, &poll.Announce, &poll.State, &poll.CountingStatus, &poll.Version, &createdAt); err != nil {
			return nil, fmt.Errorf("scan owned poll: %w", err)
		}
		poll.Deadline = time.Unix(deadline, 0).UTC()
		poll.CreatedAt = time.Unix(createdAt, 0).UTC()
		polls = append(polls, poll)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate owned polls: %w", err)
	}
	return polls, nil
}

func (s *Store) pollOptions(ctx context.Context, pollID string) ([]PollOption, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT id,label FROM options WHERE poll_id=? ORDER BY display_order", pollID)
	if err != nil {
		return nil, fmt.Errorf("read poll options: %w", err)
	}
	defer rows.Close()
	var options []PollOption
	for rows.Next() {
		var option PollOption
		if err := rows.Scan(&option.ID, &option.Label); err != nil {
			return nil, fmt.Errorf("scan poll option: %w", err)
		}
		options = append(options, option)
	}
	return options, rows.Err()
}

func (s *Store) SetPollState(ctx context.Context, ownerID, pollID, fromState, toState string, expectedVersion int, now time.Time) error {
	if ownerID == "" || pollID == "" || expectedVersion < 1 || fromState == "" || toState == "" {
		return ErrConflict
	}
	query := "UPDATE polls SET state=?,version=version+1 WHERE id=? AND owner_id=? AND state=? AND version=?"
	arguments := []any{toState, pollID, ownerID, fromState, expectedVersion}
	if fromState == "paused" && toState == "open" {
		query += " AND deadline>?"
		arguments = append(arguments, now.Unix())
	}
	result, err := s.DB.ExecContext(ctx, query, arguments...)
	if err != nil {
		return fmt.Errorf("change poll state: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return ErrConflict
	}
	return nil
}

func (s *Store) OwnedElectorate(ctx context.Context, ownerID, pollID string) ([]ElectorateParticipant, error) {
	var exists int
	if err := s.DB.QueryRowContext(ctx, "SELECT 1 FROM polls WHERE id=? AND owner_id=?", pollID, ownerID).Scan(&exists); err != nil {
		return nil, ErrConflict
	}
	rows, err := s.DB.QueryContext(ctx, "SELECT p.id,p.display_name,c.id,c.delivery_email,c.normalized_email FROM participants AS p JOIN contacts AS c ON c.participant_id=p.id AND c.poll_id=p.poll_id WHERE p.poll_id=? ORDER BY p.id,c.id", pollID)
	if err != nil {
		return nil, fmt.Errorf("read owned electorate: %w", err)
	}
	defer rows.Close()
	var participants []ElectorateParticipant
	indexes := make(map[string]int)
	for rows.Next() {
		var participantID, displayName string
		var contact Contact
		if err := rows.Scan(&participantID, &displayName, &contact.ID, &contact.DeliveryEmail, &contact.NormalizedEmail); err != nil {
			return nil, fmt.Errorf("scan owned electorate: %w", err)
		}
		index, exists := indexes[participantID]
		if !exists {
			index = len(participants)
			indexes[participantID] = index
			participants = append(participants, ElectorateParticipant{ID: participantID, DisplayName: displayName})
		}
		participants[index].Contacts = append(participants[index].Contacts, contact)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate owned electorate: %w", err)
	}
	return participants, nil
}

func (s *Store) PollForParticipant(ctx context.Context, participantID, pollID string) (ParticipantPoll, error) {
	var view ParticipantPoll
	var deadline, createdAt int64
	err := s.DB.QueryRowContext(ctx, `SELECT poll.id,poll.owner_id,poll.question,poll.deadline,poll.display_offset,poll.places,poll.announce,poll.state,poll.counting_status,poll.version,poll.created_at,participant.id,participant.display_name FROM polls AS poll JOIN participants AS participant ON participant.poll_id=poll.id WHERE poll.id=? AND participant.id=?`, pollID, participantID).Scan(&view.Poll.ID, &view.Poll.OwnerID, &view.Poll.Question, &deadline, &view.Poll.DisplayOffset, &view.Poll.Places, &view.Poll.Announce, &view.Poll.State, &view.Poll.CountingStatus, &view.Poll.Version, &createdAt, &view.ParticipantID, &view.ParticipantName)
	if err != nil {
		return ParticipantPoll{}, ErrConflict
	}
	view.Poll.Deadline = time.Unix(deadline, 0).UTC()
	view.Poll.CreatedAt = time.Unix(createdAt, 0).UTC()
	view.Poll.Options, err = s.pollOptions(ctx, pollID)
	if err != nil {
		return ParticipantPoll{}, err
	}
	var preferences []byte
	err = s.DB.QueryRowContext(ctx, "SELECT preferences_json,version FROM ballots WHERE poll_id=? AND participant_id=?", pollID, participantID).Scan(&preferences, &view.BallotVersion)
	if err == nil {
		if json.Unmarshal(preferences, &view.Preferences) != nil {
			return ParticipantPoll{}, ErrConflict
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return ParticipantPoll{}, fmt.Errorf("read participant ballot: %w", err)
	}
	return view, nil
}
