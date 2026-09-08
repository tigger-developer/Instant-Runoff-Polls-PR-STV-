// ABOUTME: Creates opaque scoped sessions and session-bound CSRF credentials.
// ABOUTME: It centralizes expiry and host-only cookie security policy.
package workflow

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"time"
)

var ErrInvalidSession = errors.New("invalid session")

type SessionPurpose string

const (
	ModeratorSession   SessionPurpose = "moderator"
	ParticipantSession SessionPurpose = "participant"
	PreAuthSession     SessionPurpose = "preauth"
)

type SessionRecord struct {
	Purpose     SessionPurpose
	PrincipalID string
	PollID      string
	TokenHash   [32]byte
	CSRFHash    [32]byte
	ExpiresAt   time.Time
	RevokedAt   time.Time
}

type IssuedSession struct {
	Token     string
	CSRFToken string
	Record    SessionRecord
}

func NewSession(purpose SessionPurpose, principalID, pollID string, now time.Time, randomness io.Reader) (IssuedSession, error) {
	if randomness == nil || !validSessionScope(purpose, principalID, pollID) {
		return IssuedSession{}, ErrInvalidSession
	}
	material := make([]byte, 64)
	if _, err := io.ReadFull(randomness, material); err != nil {
		return IssuedSession{}, ErrInvalidSession
	}
	token := base64.RawURLEncoding.EncodeToString(material[:32])
	csrf := base64.RawURLEncoding.EncodeToString(material[32:])
	lifetime := 12 * time.Hour
	if purpose == PreAuthSession {
		lifetime = 15 * time.Minute
	}
	return IssuedSession{Token: token, CSRFToken: csrf, Record: SessionRecord{Purpose: purpose, PrincipalID: principalID, PollID: pollID, TokenHash: sha256.Sum256([]byte(token)), CSRFHash: sha256.Sum256([]byte(csrf)), ExpiresAt: now.Add(lifetime)}}, nil
}

func (record SessionRecord) Authenticates(token string, now time.Time) bool {
	if token == "" || !record.RevokedAt.IsZero() || !now.Before(record.ExpiresAt) {
		return false
	}
	digest := sha256.Sum256([]byte(token))
	return subtle.ConstantTimeCompare(digest[:], record.TokenHash[:]) == 1
}

func (record SessionRecord) AcceptsCSRF(token string) bool {
	if token == "" || !record.RevokedAt.IsZero() {
		return false
	}
	digest := sha256.Sum256([]byte(token))
	return subtle.ConstantTimeCompare(digest[:], record.CSRFHash[:]) == 1
}

func SessionCookie(purpose SessionPurpose, token string, secure bool) *http.Cookie {
	name := "stv_participant"
	if purpose == ModeratorSession {
		name = "stv_moderator"
	} else if purpose == PreAuthSession {
		name = "stv_preauth"
	}
	return &http.Cookie{Name: name, Value: token, Path: "/", HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode}
}

func validSessionScope(purpose SessionPurpose, principalID, pollID string) bool {
	switch purpose {
	case ModeratorSession:
		return principalID != "" && pollID == ""
	case ParticipantSession:
		return principalID != "" && pollID != ""
	case PreAuthSession:
		return principalID == "" && pollID == ""
	default:
		return false
	}
}
