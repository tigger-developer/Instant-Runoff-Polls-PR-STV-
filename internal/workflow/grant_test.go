// ABOUTME: Verifies signed bearer-grant encoding and rejection boundaries.
// ABOUTME: It keeps credentials scoped, versioned, bounded, and expiry-safe.
package workflow

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestGrantRoundTripPreservesParticipantScope(t *testing.T) {
	now := time.Unix(1_789_000_000, 0).UTC()
	key := bytes.Repeat([]byte{7}, 32)
	claims := GrantClaims{Version: 1, KeyID: "key-1", Purpose: ParticipantGrant, PrincipalID: "participant-1", PollID: "poll-1", ContactID: "contact-1", IssuedAt: now, ExpiresAt: now.Add(24 * time.Hour)}
	token, err := IssueGrant(claims, key, bytes.NewReader(bytes.Repeat([]byte{9}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	got, err := VerifyGrant(token, "key-1", key, now)
	if err != nil {
		t.Fatal(err)
	}
	if got.PrincipalID != claims.PrincipalID || got.PollID != claims.PollID || got.ContactID != claims.ContactID || got.Purpose != ParticipantGrant {
		t.Fatalf("verified claims = %#v", got)
	}
}

func TestGrantRejectsTamperingKeyRotationExpiryAndOversize(t *testing.T) {
	now := time.Unix(1_789_000_000, 0).UTC()
	key := bytes.Repeat([]byte{7}, 32)
	claims := GrantClaims{Version: 1, KeyID: "key-1", Purpose: ModeratorGrant, PrincipalID: "moderator-1", IssuedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Minute)}
	token, err := IssueGrant(claims, key, bytes.NewReader(bytes.Repeat([]byte{9}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, token, keyID string
		key                []byte
		at                 time.Time
	}{
		{name: "tampered", token: token[:len(token)-1] + "A", keyID: "key-1", key: key, at: now},
		{name: "rotated", token: token, keyID: "key-2", key: key, at: now},
		{name: "wrong key", token: token, keyID: "key-1", key: bytes.Repeat([]byte{8}, 32), at: now},
		{name: "expiry equality", token: token, keyID: "key-1", key: key, at: claims.ExpiresAt},
		{name: "oversize", token: strings.Repeat("a", 4097), keyID: "key-1", key: key, at: now},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := VerifyGrant(tc.token, tc.keyID, tc.key, tc.at); !errors.Is(err, ErrInvalidGrant) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestGrantRejectsInvalidIssuance(t *testing.T) {
	now := time.Now().UTC()
	for _, claims := range []GrantClaims{
		{Version: 2, KeyID: "key-1", Purpose: ModeratorGrant, PrincipalID: "m", IssuedAt: now, ExpiresAt: now.Add(time.Minute)},
		{Version: 1, KeyID: "key-1", Purpose: ParticipantGrant, PrincipalID: "p", IssuedAt: now, ExpiresAt: now.Add(time.Minute)},
		{Version: 1, KeyID: "key-1", Purpose: ModeratorGrant, PrincipalID: "m", IssuedAt: now, ExpiresAt: now},
	} {
		if _, err := IssueGrant(claims, bytes.Repeat([]byte{1}, 32), bytes.NewReader(bytes.Repeat([]byte{2}, 32))); !errors.Is(err, ErrInvalidGrant) {
			t.Fatalf("claims %#v error = %v", claims, err)
		}
	}
}
