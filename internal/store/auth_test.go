// ABOUTME: Verifies persisted grants and sessions enforce their identity scope.
// ABOUTME: It proves authentication secrets are represented only by hashes.
package store

import (
	"bytes"
	"context"
	"testing"
	"time"
)

func TestModeratorLoginGrantAndParticipantSessionBoundaries(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SyncModerators(ctx, []ConfiguredModerator{{ID: "moderator", NormalizedEmail: "owner@example.test"}}); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(100, 0)
	material := GrantMaterial{ID: "grant", KeyID: "key", TokenHash: bytes.Repeat([]byte{1}, 32), ClaimsJSON: []byte(`{"v":1}`), IssuedAt: now, ExpiresAt: now.Add(15 * time.Minute)}
	if err := st.QueueModeratorLogin(ctx, "moderator", "owner@example.test", "work", "delivery", material, now); err != nil {
		t.Fatal(err)
	}
	grant, err := st.GrantByHash(ctx, material.TokenHash, now)
	if err != nil || grant.Purpose != "moderator" || grant.PrincipalID != "moderator" || grant.Consumed {
		t.Fatalf("grant=%#v error=%v", grant, err)
	}
	if _, err := st.DB.ExecContext(ctx, "INSERT INTO polls(id,owner_id,question,deadline,places,state,version) VALUES ('poll','moderator','Question',500,1,'open',1)"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.ExecContext(ctx, "INSERT INTO participants(id,poll_id) VALUES ('person','poll')"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.ExecContext(ctx, "INSERT INTO contacts(id,poll_id,participant_id,delivery_email,normalized_email) VALUES ('contact','poll','person','reader@example.test','reader@example.test')"); err != nil {
		t.Fatal(err)
	}
	participantHash := bytes.Repeat([]byte{2}, 32)
	if _, err := st.DB.ExecContext(ctx, "INSERT INTO grants(id,purpose,principal_id,poll_id,contact_id,key_id,token_hash,claims_json,issued_at,expires_at) VALUES ('participant-grant','participant','person','poll','contact','key',?,'{}',100,500)", participantHash); err != nil {
		t.Fatal(err)
	}
	sessionHash := bytes.Repeat([]byte{3}, 32)
	csrfHash := bytes.Repeat([]byte{4}, 32)
	if err := st.CreateParticipantSession(ctx, participantHash, sessionHash, csrfHash, now.Add(time.Hour), now); err != nil {
		t.Fatal(err)
	}
	session, err := st.SessionByHash(ctx, sessionHash, now)
	if err != nil || session.Purpose != "participant" || session.PrincipalID != "person" || session.PollID != "poll" || !bytes.Equal(session.CSRFHash, csrfHash) {
		t.Fatalf("session=%#v error=%v", session, err)
	}
	if err := st.RevokeSession(ctx, sessionHash, now); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SessionByHash(ctx, sessionHash, now); err == nil {
		t.Fatal("revoked session remained active")
	}
}
