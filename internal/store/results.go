// ABOUTME: Reads poll closure inputs and owner-only aggregate result evidence.
// ABOUTME: It never exposes individual ballots through result projections.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

type closeWorkQuery interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type OwnedResult struct {
	Poll       PollRecord
	Turnout    int
	ResultJSON []byte
	Deliveries map[string]int
}

type CountAuditOption struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

type CountAudit struct {
	Version          int                `json:"version"`
	PollID           string             `json:"poll_id"`
	Question         string             `json:"question"`
	State            string             `json:"state"`
	CountingStatus   string             `json:"counting_status"`
	Options          []CountAuditOption `json:"options"`
	SnapshotID       string             `json:"snapshot_id"`
	SchemaVersion    int                `json:"schema_version"`
	Rule             string             `json:"rule"`
	InputFingerprint string             `json:"input_fingerprint"`
	Input            json.RawMessage    `json:"input"`
	Decisions        []json.RawMessage  `json:"decisions"`
	Result           json.RawMessage    `json:"result"`
}

func (s *Store) PollOwner(ctx context.Context, pollID string) (string, error) {
	if s == nil || s.DB == nil || pollID == "" {
		return "", ErrConflict
	}
	var ownerID string
	if err := s.DB.QueryRowContext(ctx, "SELECT owner_id FROM polls WHERE id=?", pollID).Scan(&ownerID); err != nil {
		return "", ErrConflict
	}
	return ownerID, nil
}

func (s *Store) PollForClose(ctx context.Context, ownerID, pollID string) (CloseWork, error) {
	return loadCloseWork(ctx, s.DB, ownerID, pollID)
}

func loadCloseWork(ctx context.Context, query closeWorkQuery, ownerID, pollID string) (CloseWork, error) {
	var work CloseWork
	err := query.QueryRowContext(ctx, "SELECT id,owner_id,question,deadline,places,state,version FROM polls WHERE id=? AND owner_id=?", pollID, ownerID).Scan(&work.PollID, &work.OwnerID, &work.Question, &work.Deadline, &work.Places, &work.State, &work.Version)
	if err != nil {
		return CloseWork{}, ErrConflict
	}
	optionRows, err := query.QueryContext(ctx, "SELECT id,label FROM options WHERE poll_id=? ORDER BY display_order", pollID)
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
	participantRows, err := query.QueryContext(ctx, "SELECT id FROM participants WHERE poll_id=? ORDER BY id", pollID)
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
	ballotRows, err := query.QueryContext(ctx, "SELECT participant_id,preferences_json,version,accepted_at FROM ballots WHERE poll_id=? ORDER BY participant_id", pollID)
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

func (s *Store) ResultForOwner(ctx context.Context, ownerID, pollID string) (OwnedResult, error) {
	poll, err := s.OwnedPoll(ctx, ownerID, pollID)
	if err != nil {
		return OwnedResult{}, err
	}
	result := OwnedResult{Poll: poll, Deliveries: make(map[string]int)}
	if err := s.DB.QueryRowContext(ctx, "SELECT count(*) FROM ballots WHERE poll_id=?", pollID).Scan(&result.Turnout); err != nil {
		return OwnedResult{}, fmt.Errorf("read turnout: %w", err)
	}
	err = s.DB.QueryRowContext(ctx, "SELECT result_json FROM count_results WHERE snapshot_id=(SELECT id FROM count_snapshots WHERE poll_id=?)", pollID).Scan(&result.ResultJSON)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return OwnedResult{}, fmt.Errorf("read count result: %w", err)
	}
	rows, err := s.DB.QueryContext(ctx, "SELECT status,count(*) FROM deliveries WHERE work_id IN (SELECT id FROM work_items WHERE poll_id=?) GROUP BY status", pollID)
	if err != nil {
		return OwnedResult{}, fmt.Errorf("read delivery status: %w", err)
	}
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return OwnedResult{}, fmt.Errorf("scan delivery status: %w", err)
		}
		result.Deliveries[status] = count
	}
	if err := finishRows(rows, "delivery status"); err != nil {
		return OwnedResult{}, err
	}
	return result, nil
}

func (s *Store) CountAuditForOwner(ctx context.Context, ownerID, pollID string) (CountAudit, error) {
	poll, err := s.OwnedPoll(ctx, ownerID, pollID)
	if err != nil || poll.State != "closed" || poll.CountingStatus != "succeeded" {
		return CountAudit{}, ErrConflict
	}
	audit := CountAudit{
		Version:        1,
		PollID:         poll.ID,
		Question:       poll.Question,
		State:          poll.State,
		CountingStatus: poll.CountingStatus,
		Options:        make([]CountAuditOption, 0, len(poll.Options)),
		Decisions:      make([]json.RawMessage, 0),
	}
	for _, option := range poll.Options {
		audit.Options = append(audit.Options, CountAuditOption(option))
	}
	err = s.DB.QueryRowContext(ctx, `SELECT snapshot.id,snapshot.schema_version,snapshot.rule,snapshot.input_fingerprint,snapshot.input_json,result.result_json
		FROM count_snapshots AS snapshot
		JOIN count_results AS result ON result.snapshot_id=snapshot.id
		WHERE snapshot.poll_id=?`, pollID).Scan(&audit.SnapshotID, &audit.SchemaVersion, &audit.Rule, &audit.InputFingerprint, &audit.Input, &audit.Result)
	if err != nil || !json.Valid(audit.Input) || !json.Valid(audit.Result) {
		return CountAudit{}, ErrConflict
	}
	rows, err := s.DB.QueryContext(ctx, "SELECT decision_json FROM count_decisions WHERE snapshot_id=? ORDER BY sequence", audit.SnapshotID)
	if err != nil {
		return CountAudit{}, fmt.Errorf("read count audit decisions: %w", err)
	}
	for rows.Next() {
		var decision json.RawMessage
		if err := rows.Scan(&decision); err != nil || !json.Valid(decision) {
			_ = rows.Close()
			return CountAudit{}, ErrConflict
		}
		audit.Decisions = append(audit.Decisions, decision)
	}
	if err := finishRows(rows, "count audit decisions"); err != nil {
		return CountAudit{}, err
	}
	return audit, nil
}
