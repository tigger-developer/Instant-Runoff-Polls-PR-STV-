// ABOUTME: Persists configured identity, bearer grant, and session boundaries.
// ABOUTME: It keeps raw bearer and session secrets outside SQLite.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

type ConfiguredModerator struct {
	ID              string
	NormalizedEmail string
}

type PersistedGrant struct {
	ID          string
	Purpose     string
	PrincipalID string
	PollID      string
	ContactID   string
	KeyID       string
	ClaimsJSON  []byte
	ExpiresAt   time.Time
	Consumed    bool
}

type PersistedSession struct {
	Purpose     string
	PrincipalID string
	PollID      string
	CSRFHash    []byte
	ExpiresAt   time.Time
}

type EligibleContact struct {
	PollID        string
	ParticipantID string
	ContactID     string
	Recipient     string
	Question      string
}

func (s *Store) SyncModerators(ctx context.Context, moderators []ConfiguredModerator) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin moderator synchronization: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, moderator := range moderators {
		if moderator.ID == "" || moderator.NormalizedEmail == "" {
			return ErrConflict
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO moderators(id,normalized_email) VALUES (?,?) ON CONFLICT(id) DO UPDATE SET normalized_email=excluded.normalized_email`, moderator.ID, moderator.NormalizedEmail); err != nil {
			return fmt.Errorf("synchronize moderator: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit moderator synchronization: %w", err)
	}
	return nil
}

func (s *Store) QueueModeratorLogin(ctx context.Context, moderatorID, recipient, workID, deliveryID string, material GrantMaterial, now time.Time) error {
	if moderatorID == "" || recipient == "" || workID == "" || deliveryID == "" || material.ID == "" || material.KeyID == "" || len(material.TokenHash) != 32 || !material.ExpiresAt.After(now) {
		return ErrConflict
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin moderator login queue: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var configuredEmail string
	if err := tx.QueryRowContext(ctx, "SELECT normalized_email FROM moderators WHERE id=?", moderatorID).Scan(&configuredEmail); err != nil || configuredEmail != recipient {
		return ErrConflict
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO grants(id,purpose,principal_id,key_id,token_hash,claims_json,issued_at,expires_at) VALUES (?,'moderator',?,?,?,?,?,?)`, material.ID, moderatorID, material.KeyID, material.TokenHash, material.ClaimsJSON, material.IssuedAt.Unix(), material.ExpiresAt.Unix()); err != nil {
		return fmt.Errorf("insert moderator grant: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO work_items(id,kind,logical_key,due_at,status) VALUES (?,'delivery',? ,?,'pending')", workID, "moderator-login:"+material.ID, now.Unix()); err != nil {
		return fmt.Errorf("insert moderator login work: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO deliveries(id,work_id,grant_id,recipient_email,message_kind,status,next_due) VALUES (?,?,?,?, 'moderator_login','pending',?)", deliveryID, workID, material.ID, recipient, now.Unix()); err != nil {
		return fmt.Errorf("insert moderator login delivery: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit moderator login queue: %w", err)
	}
	return nil
}

func (s *Store) ContactForReturn(ctx context.Context, pollID, normalizedEmail string, now time.Time) (EligibleContact, error) {
	var contact EligibleContact
	err := s.DB.QueryRowContext(ctx, `SELECT poll.id,contact.participant_id,contact.id,contact.delivery_email,poll.question FROM polls AS poll JOIN contacts AS contact ON contact.poll_id=poll.id WHERE poll.id=? AND contact.normalized_email=? AND poll.state='open' AND poll.deadline>?`, pollID, normalizedEmail, now.Unix()).Scan(&contact.PollID, &contact.ParticipantID, &contact.ContactID, &contact.Recipient, &contact.Question)
	if err != nil {
		return EligibleContact{}, ErrConflict
	}
	return contact, nil
}

func (s *Store) QueueParticipantReturn(ctx context.Context, contact EligibleContact, workID, deliveryID string, material GrantMaterial, now time.Time) error {
	if contact.PollID == "" || contact.ParticipantID == "" || contact.ContactID == "" || contact.Recipient == "" || workID == "" || deliveryID == "" || material.ID == "" || len(material.TokenHash) != 32 || !material.ExpiresAt.After(now) {
		return ErrConflict
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin participant return queue: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var eligible int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM polls AS poll JOIN contacts AS contact ON contact.poll_id=poll.id WHERE poll.id=? AND contact.id=? AND contact.participant_id=? AND contact.delivery_email=? AND poll.state='open' AND poll.deadline>?`, contact.PollID, contact.ContactID, contact.ParticipantID, contact.Recipient, now.Unix()).Scan(&eligible); err != nil || eligible != 1 {
		return ErrConflict
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO grants(id,purpose,principal_id,poll_id,contact_id,key_id,token_hash,claims_json,issued_at,expires_at) VALUES (?,'participant',?,?,?,?,?,?,?,?)`, material.ID, contact.ParticipantID, contact.PollID, contact.ContactID, material.KeyID, material.TokenHash, material.ClaimsJSON, material.IssuedAt.Unix(), material.ExpiresAt.Unix()); err != nil {
		return fmt.Errorf("insert participant return grant: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO work_items(id,poll_id,kind,logical_key,due_at,status) VALUES (?,?,'delivery',? ,?,'pending')", workID, contact.PollID, "participant-return:"+material.ID, now.Unix()); err != nil {
		return fmt.Errorf("insert participant return work: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO deliveries(id,work_id,grant_id,contact_id,recipient_email,message_kind,status,next_due) VALUES (?,?,?,?,?,'participant_return','pending',?)", deliveryID, workID, material.ID, contact.ContactID, contact.Recipient, now.Unix()); err != nil {
		return fmt.Errorf("insert participant return delivery: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit participant return queue: %w", err)
	}
	return nil
}

func (s *Store) GrantByHash(ctx context.Context, tokenHash []byte, now time.Time) (PersistedGrant, error) {
	if len(tokenHash) != 32 {
		return PersistedGrant{}, ErrConflict
	}
	var grant PersistedGrant
	var pollID, contactID sql.NullString
	var expiresAt int64
	var consumedAt sql.NullInt64
	err := s.DB.QueryRowContext(ctx, `SELECT id,purpose,principal_id,poll_id,contact_id,key_id,claims_json,expires_at,consumed_at FROM grants WHERE token_hash=? AND revoked_at IS NULL AND expires_at>?`, tokenHash, now.Unix()).Scan(&grant.ID, &grant.Purpose, &grant.PrincipalID, &pollID, &contactID, &grant.KeyID, &grant.ClaimsJSON, &expiresAt, &consumedAt)
	if err != nil {
		return PersistedGrant{}, ErrConflict
	}
	grant.PollID = pollID.String
	grant.ContactID = contactID.String
	grant.ExpiresAt = time.Unix(expiresAt, 0).UTC()
	grant.Consumed = consumedAt.Valid
	return grant, nil
}

func (s *Store) CreateParticipantSession(ctx context.Context, grantHash, sessionHash, csrfHash, preAuthHash, replaceHash []byte, sessionExpiresAt, now time.Time) error {
	if len(grantHash) != 32 || len(sessionHash) != 32 || len(csrfHash) != 32 || len(preAuthHash) != 0 && len(preAuthHash) != 32 || len(replaceHash) != 0 && len(replaceHash) != 32 || !sessionExpiresAt.After(now) {
		return ErrConflict
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin participant session: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := rotateAuthenticationSessions(ctx, tx, preAuthHash, replaceHash, "participant", now); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO sessions(token_hash,purpose,principal_id,poll_id,csrf_hash,expires_at) SELECT ?,g.purpose,g.principal_id,g.poll_id,?,? FROM grants AS g JOIN contacts AS c ON c.id=g.contact_id AND c.poll_id=g.poll_id AND c.participant_id=g.principal_id WHERE g.token_hash=? AND g.purpose='participant' AND g.revoked_at IS NULL AND g.expires_at>?`, sessionHash, csrfHash, sessionExpiresAt.Unix(), grantHash, now.Unix())
	if err != nil {
		return fmt.Errorf("create participant session: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return ErrConflict
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit participant session: %w", err)
	}
	return nil
}

func rotateAuthenticationSessions(ctx context.Context, tx *sql.Tx, preAuthHash, replaceHash []byte, purpose string, now time.Time) error {
	if len(preAuthHash) != 0 {
		result, err := tx.ExecContext(ctx, "UPDATE sessions SET revoked_at=? WHERE token_hash=? AND purpose='preauth' AND revoked_at IS NULL AND expires_at>?", now.Unix(), preAuthHash, now.Unix())
		if err != nil {
			return fmt.Errorf("revoke pre-authentication session: %w", err)
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			return ErrConflict
		}
	}
	if len(replaceHash) != 0 {
		if _, err := tx.ExecContext(ctx, "UPDATE sessions SET revoked_at=? WHERE token_hash=? AND purpose=? AND revoked_at IS NULL", now.Unix(), replaceHash, purpose); err != nil {
			return fmt.Errorf("revoke presented session: %w", err)
		}
	}
	return nil
}

func (s *Store) SessionByHash(ctx context.Context, tokenHash []byte, now time.Time) (PersistedSession, error) {
	if len(tokenHash) != 32 {
		return PersistedSession{}, ErrConflict
	}
	var session PersistedSession
	var pollID sql.NullString
	var expiresAt int64
	err := s.DB.QueryRowContext(ctx, `SELECT purpose,principal_id,poll_id,csrf_hash,expires_at FROM sessions WHERE token_hash=? AND revoked_at IS NULL AND expires_at>?`, tokenHash, now.Unix()).Scan(&session.Purpose, &session.PrincipalID, &pollID, &session.CSRFHash, &expiresAt)
	if err != nil {
		return PersistedSession{}, ErrConflict
	}
	session.PollID = pollID.String
	session.ExpiresAt = time.Unix(expiresAt, 0).UTC()
	return session, nil
}

func (s *Store) RevokeSession(ctx context.Context, tokenHash []byte, now time.Time) error {
	if len(tokenHash) != 32 {
		return ErrConflict
	}
	result, err := s.DB.ExecContext(ctx, "UPDATE sessions SET revoked_at=? WHERE token_hash=? AND revoked_at IS NULL", now.Unix(), tokenHash)
	if err != nil {
		return fmt.Errorf("revoke session: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return ErrConflict
	}
	return nil
}
