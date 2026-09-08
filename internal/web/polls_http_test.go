// ABOUTME: Verifies clear-and-revote behaviour through the authenticated HTTP route.
// ABOUTME: It protects CSRF, ballot versions, poll state, and redirect outcomes.
package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/tigger-developer/Instant-Runoff-Polls-PR-STV-/internal/config"
	"github.com/tigger-developer/Instant-Runoff-Polls-PR-STV-/internal/store"
)

func TestClearBallotHTTPEnforcesCSRFVersionStateAndRedirect(t *testing.T) {
	for _, tc := range []struct {
		name         string
		state        string
		formCSRF     string
		version      string
		wantStatus   int
		wantLocation string
		wantBallots  int
	}{
		{name: "valid clear", state: "open", formCSRF: "csrf-token", version: "1", wantStatus: http.StatusSeeOther, wantLocation: "/polls/poll?start=1", wantBallots: 0},
		{name: "wrong csrf", state: "open", formCSRF: "wrong", version: "1", wantStatus: http.StatusForbidden, wantBallots: 1},
		{name: "stale version", state: "open", formCSRF: "csrf-token", version: "0", wantStatus: http.StatusConflict, wantBallots: 1},
		{name: "closed poll", state: "closed", formCSRF: "csrf-token", version: "1", wantStatus: http.StatusConflict, wantBallots: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler, st, sessionToken, csrfToken := clearBallotHTTPFixture(t, tc.state)
			form := url.Values{"csrf": {tc.formCSRF}, "version": {tc.version}}
			request := httptest.NewRequest(http.MethodPost, "/polls/poll/ballot/clear", strings.NewReader(form.Encode()))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			request.AddCookie(&http.Cookie{Name: "stv_participant", Value: sessionToken})
			request.AddCookie(&http.Cookie{Name: "stv_participant_csrf", Value: csrfToken})
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != tc.wantStatus || response.Header().Get("Location") != tc.wantLocation {
				t.Fatalf("status=%d location=%q body=%s", response.Code, response.Header().Get("Location"), response.Body.String())
			}
			var ballots int
			if err := st.DB.QueryRow("SELECT count(*) FROM ballots WHERE poll_id='poll'").Scan(&ballots); err != nil || ballots != tc.wantBallots {
				t.Fatalf("ballots=%d error=%v", ballots, err)
			}
		})
	}
}

func clearBallotHTTPFixture(t *testing.T, state string) (http.Handler, *store.Store, string, string) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.SyncModerators(ctx, []store.ConfiguredModerator{{ID: "owner", NormalizedEmail: "owner@example.test"}}); err != nil {
		t.Fatal(err)
	}
	draft := store.DraftPoll{Question: "Question", Deadline: time.Unix(500, 0), Places: 1, Options: []store.PollOption{{ID: "a", Label: "Alpha"}, {ID: "b", Label: "Beta"}}}
	if err := st.CreateDraftPoll(ctx, "owner", "poll", draft, time.Unix(50, 0)); err != nil {
		t.Fatal(err)
	}
	participant := store.ElectorateParticipant{ID: "person", DisplayName: "Alex", Contacts: []store.Contact{{ID: "contact", DeliveryEmail: "one@example.test", NormalizedEmail: "one@example.test"}}}
	if err := st.ReplaceElectorate(ctx, "owner", "poll", 1, []store.ElectorateParticipant{participant}); err != nil {
		t.Fatal(err)
	}
	invitation := store.InvitationWork{WorkID: "invite", DeliveryID: "delivery", ParticipantID: "person", RecipientEmails: []string{"one@example.test"}}
	if _, err := st.OpenPoll(ctx, "owner", "poll", 2, "scheduled-close", []store.InvitationWork{invitation}, time.Unix(60, 0)); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceBallot(ctx, "poll", "person", 0, []byte(`["a"]`), func() time.Time { return time.Unix(90, 0) }); err != nil {
		t.Fatal(err)
	}
	if state == "closed" {
		if _, err := st.DB.ExecContext(ctx, "UPDATE polls SET state='closed' WHERE id='poll'"); err != nil {
			t.Fatal(err)
		}
	}
	sessionToken, csrfToken := "session-token", "csrf-token"
	sessionHash, csrfHash := sha256.Sum256([]byte(sessionToken)), sha256.Sum256([]byte(csrfToken))
	if _, err := st.DB.ExecContext(ctx, "INSERT INTO sessions(token_hash,purpose,principal_id,poll_id,csrf_hash,expires_at) VALUES (?,'participant','person','poll',?,500)", sessionHash[:], csrfHash[:]); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{BaseURL: "https://poll.example"}
	handler := WorkflowHandler(cfg, st, authTemplates(t), http.NotFoundHandler(), bytes.NewReader(make([]byte, 256)), func() time.Time { return time.Unix(100, 0) })
	return handler, st, sessionToken, csrfToken
}
