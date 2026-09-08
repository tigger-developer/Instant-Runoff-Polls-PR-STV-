// ABOUTME: Verifies opaque session credentials, CSRF binding, and cookie policy.
// ABOUTME: It protects authentication state without trusting request headers.
package workflow

import (
	"bytes"
	"net/http"
	"testing"
	"time"
)

func TestNewSessionStoresOnlyCredentialAndCSRFDigests(t *testing.T) {
	now := time.Unix(1_789_000_000, 0).UTC()
	material := append(bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32)...)
	issued, err := NewSession(ParticipantSession, "participant-1", "poll-1", now, bytes.NewReader(material))
	if err != nil {
		t.Fatal(err)
	}
	if issued.Token == "" || issued.CSRFToken == "" || issued.Record.TokenHash == [32]byte{} || issued.Record.CSRFHash == [32]byte{} {
		t.Fatalf("issued session = %#v", issued)
	}
	if !issued.Record.Authenticates(issued.Token, now) || !issued.Record.AcceptsCSRF(issued.CSRFToken) {
		t.Fatal("issued credentials did not authenticate")
	}
	if issued.Record.Authenticates(issued.Token, issued.Record.ExpiresAt) || issued.Record.AcceptsCSRF(issued.Token) {
		t.Fatal("expiry equality or cross-purpose credential was accepted")
	}
}

func TestSessionCookieUsesConfiguredSecurityOnly(t *testing.T) {
	secure := SessionCookie(ModeratorSession, "opaque", true)
	if secure.Name != "stv_moderator" || !secure.Secure || !secure.HttpOnly || secure.SameSite != http.SameSiteLaxMode || secure.Path != "/" {
		t.Fatalf("secure cookie = %#v", secure)
	}
	local := SessionCookie(ParticipantSession, "opaque", false)
	if local.Name != "stv_participant" || local.Secure {
		t.Fatalf("local cookie = %#v", local)
	}
}

func TestSessionRejectsInvalidScopeAndShortRandomness(t *testing.T) {
	now := time.Now().UTC()
	if _, err := NewSession(ParticipantSession, "participant-1", "", now, bytes.NewReader(make([]byte, 64))); err == nil {
		t.Fatal("participant session without poll succeeded")
	}
	if _, err := NewSession(ModeratorSession, "moderator-1", "poll-1", now, bytes.NewReader(make([]byte, 64))); err == nil {
		t.Fatal("moderator session with poll scope succeeded")
	}
	if _, err := NewSession(ModeratorSession, "moderator-1", "", now, bytes.NewReader(make([]byte, 12))); err == nil {
		t.Fatal("short randomness succeeded")
	}
}
