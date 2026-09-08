// ABOUTME: Issues and verifies versioned HMAC bearer grants for workflow access.
// ABOUTME: It exposes scoped claims while keeping signatures and nonces explicit.
package workflow

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"
)

var ErrInvalidGrant = errors.New("invalid grant")

type GrantPurpose string

const (
	ModeratorGrant   GrantPurpose = "moderator"
	ParticipantGrant GrantPurpose = "participant"
)

type GrantClaims struct {
	Version     int
	KeyID       string
	Purpose     GrantPurpose
	PrincipalID string
	PollID      string
	ContactID   string
	IssuedAt    time.Time
	ExpiresAt   time.Time
}

type grantPayload struct {
	Version     int          `json:"v"`
	KeyID       string       `json:"key_id"`
	Purpose     GrantPurpose `json:"purpose"`
	PrincipalID string       `json:"principal_id"`
	PollID      string       `json:"poll_id,omitempty"`
	ContactID   string       `json:"contact_id,omitempty"`
	IssuedAt    int64        `json:"issued_at"`
	ExpiresAt   int64        `json:"expires_at"`
	Nonce       string       `json:"nonce"`
}

func IssueGrant(claims GrantClaims, key []byte, randomness io.Reader) (string, error) {
	token, _, err := IssuePersistableGrant(claims, key, randomness)
	return token, err
}

func IssuePersistableGrant(claims GrantClaims, key []byte, randomness io.Reader) (string, []byte, error) {
	if !validClaims(claims) || len(key) != 32 || randomness == nil {
		return "", nil, ErrInvalidGrant
	}
	nonce := make([]byte, 32)
	if _, err := io.ReadFull(randomness, nonce); err != nil {
		return "", nil, ErrInvalidGrant
	}
	payload := grantPayload{Version: claims.Version, KeyID: claims.KeyID, Purpose: claims.Purpose, PrincipalID: claims.PrincipalID, PollID: claims.PollID, ContactID: claims.ContactID, IssuedAt: claims.IssuedAt.Unix(), ExpiresAt: claims.ExpiresAt.Unix(), Nonce: base64.RawURLEncoding.EncodeToString(nonce)}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", nil, ErrInvalidGrant
	}
	payloadText := base64.RawURLEncoding.EncodeToString(encoded)
	signature := signGrant(payloadText, key)
	return payloadText + "." + base64.RawURLEncoding.EncodeToString(signature), encoded, nil
}

func RestoreGrant(encodedPayload, key []byte) (string, error) {
	if len(key) != 32 {
		return "", ErrInvalidGrant
	}
	var payload grantPayload
	if json.Unmarshal(encodedPayload, &payload) != nil || payload.Nonce == "" {
		return "", ErrInvalidGrant
	}
	payloadText := base64.RawURLEncoding.EncodeToString(encodedPayload)
	return payloadText + "." + base64.RawURLEncoding.EncodeToString(signGrant(payloadText, key)), nil
}

func VerifyGrant(token, activeKeyID string, key []byte, now time.Time) (GrantClaims, error) {
	if len(token) > 4096 || len(key) != 32 {
		return GrantClaims{}, ErrInvalidGrant
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return GrantClaims{}, ErrInvalidGrant
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || !hmac.Equal(signature, signGrant(parts[0], key)) {
		return GrantClaims{}, ErrInvalidGrant
	}
	encoded, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return GrantClaims{}, ErrInvalidGrant
	}
	var payload grantPayload
	if err := json.Unmarshal(encoded, &payload); err != nil {
		return GrantClaims{}, ErrInvalidGrant
	}
	claims := GrantClaims{Version: payload.Version, KeyID: payload.KeyID, Purpose: payload.Purpose, PrincipalID: payload.PrincipalID, PollID: payload.PollID, ContactID: payload.ContactID, IssuedAt: time.Unix(payload.IssuedAt, 0).UTC(), ExpiresAt: time.Unix(payload.ExpiresAt, 0).UTC()}
	if payload.KeyID != activeKeyID || payload.Nonce == "" || !validClaims(claims) || !now.Before(claims.ExpiresAt) {
		return GrantClaims{}, ErrInvalidGrant
	}
	return claims, nil
}

func validClaims(claims GrantClaims) bool {
	if claims.Version != 1 || claims.KeyID == "" || claims.PrincipalID == "" || !claims.ExpiresAt.After(claims.IssuedAt) {
		return false
	}
	switch claims.Purpose {
	case ModeratorGrant:
		return claims.PollID == "" && claims.ContactID == ""
	case ParticipantGrant:
		// ContactID is accepted only for already-issued v1 links. New participant
		// grants omit it and authenticate the poll participant directly.
		return claims.PollID != ""
	default:
		return false
	}
}

func signGrant(payload string, key []byte) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(payload))
	return mac.Sum(nil)
}
