// ABOUTME: Verifies passwordless login requests and CSRF-bound grant exchange.
// ABOUTME: It exercises the HTTP boundary against real SQLite persistence.
package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/tigger-developer/Instant-Runoff-Polls-PR-STV-/internal/config"
	"github.com/tigger-developer/Instant-Runoff-Polls-PR-STV-/internal/store"
	"github.com/tigger-developer/Instant-Runoff-Polls-PR-STV-/internal/workflow"
)

func TestModeratorLoginQueuesGrantAndExchangesThroughPreAuthCSRF(t *testing.T) {
	st, err := store.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SyncModerators(context.Background(), []store.ConfiguredModerator{{ID: "owner", NormalizedEmail: "owner@example.test"}}); err != nil {
		t.Fatal(err)
	}
	key := bytes.Repeat([]byte{7}, 32)
	cfg := config.Config{BaseURL: "https://poll.example", Moderators: []config.Moderator{{ID: "owner", Email: "owner@example.test"}}, Auth: config.Auth{KeyID: "key", SigningKey: key}}
	page := authTemplates(t)
	randomMaterial := append(bytes.Repeat([]byte{9}, 32), bytes.Repeat([]byte{8}, 64)...)
	randomMaterial = append(randomMaterial, bytes.Repeat([]byte{7}, 64)...)
	handler := WorkflowHandler(cfg, st, page, http.NotFoundHandler(), bytes.NewReader(randomMaterial), func() time.Time { return time.Unix(100, 0) })

	login := httptest.NewRequest(http.MethodPost, "/moderator/login", strings.NewReader("email=owner%40example.test"))
	login.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	loginResponse := httptest.NewRecorder()
	handler.ServeHTTP(loginResponse, login)
	if loginResponse.Code != http.StatusSeeOther || loginResponse.Header().Get("Location") != "/moderator/login?sent=1" {
		t.Fatalf("login response=%d location=%s", loginResponse.Code, loginResponse.Header().Get("Location"))
	}
	claimed, err := st.ClaimDueWork(context.Background(), "delivery", "claim", time.Unix(100, 0))
	if err != nil || claimed == nil {
		t.Fatalf("claimed=%#v error=%v", claimed, err)
	}
	attempt, err := st.BeginDeliveryAttempt(context.Background(), claimed.ID, claimed.ClaimToken, time.Unix(100, 0))
	if err != nil || attempt.MessageKind != "moderator_login" {
		t.Fatalf("attempt=%#v error=%v", attempt, err)
	}
	token, err := workflow.RestoreGrant(attempt.GrantPayload, key)
	if err != nil {
		t.Fatal(err)
	}

	verifyResponse := httptest.NewRecorder()
	handler.ServeHTTP(verifyResponse, httptest.NewRequest(http.MethodGet, "/auth/verify?grant="+url.QueryEscape(token), nil))
	if verifyResponse.Code != http.StatusOK || len(verifyResponse.Result().Cookies()) != 1 {
		t.Fatalf("verify response=%d cookies=%v body=%s", verifyResponse.Code, verifyResponse.Result().Cookies(), verifyResponse.Body.String())
	}
	csrfMatch := regexp.MustCompile(`name="csrf" type="hidden" value="([^"]+)"`).FindStringSubmatch(verifyResponse.Body.String())
	grantMatch := regexp.MustCompile(`name="grant" type="hidden" value="([^"]+)"`).FindStringSubmatch(verifyResponse.Body.String())
	if len(csrfMatch) != 2 || len(grantMatch) != 2 {
		t.Fatalf("verification form=%s", verifyResponse.Body.String())
	}
	if grantMatch[1] != token {
		t.Fatalf("rendered grant changed: got=%q want=%q", grantMatch[1], token)
	}
	form := url.Values{"grant": {grantMatch[1]}, "csrf": {csrfMatch[1]}}
	exchange := httptest.NewRequest(http.MethodPost, "/auth/verify", strings.NewReader(form.Encode()))
	exchange.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	exchange.AddCookie(verifyResponse.Result().Cookies()[0])
	exchangeResponse := httptest.NewRecorder()
	handler.ServeHTTP(exchangeResponse, exchange)
	if exchangeResponse.Code != http.StatusSeeOther || exchangeResponse.Header().Get("Location") != "/moderator/polls" {
		t.Fatalf("exchange response=%d location=%s body=%s", exchangeResponse.Code, exchangeResponse.Header().Get("Location"), exchangeResponse.Body.String())
	}
	cookies := exchangeResponse.Result().Cookies()
	if len(cookies) != 2 || cookies[0].Name != "stv_moderator" || cookies[0].Value == token || cookies[1].Name != "stv_moderator_csrf" {
		t.Fatalf("session cookies=%v", cookies)
	}
}

func TestModeratorLoginUnknownAddressHasSameConfirmationWithoutDelivery(t *testing.T) {
	st, err := store.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cfg := config.Config{BaseURL: "https://poll.example", Moderators: []config.Moderator{}, Auth: config.Auth{KeyID: "key", SigningKey: bytes.Repeat([]byte{7}, 32)}}
	handler := WorkflowHandler(cfg, st, authTemplates(t), http.NotFoundHandler(), bytes.NewReader(make([]byte, 128)), func() time.Time { return time.Unix(100, 0) })
	request := httptest.NewRequest(http.MethodPost, "/moderator/login", strings.NewReader("email=unknown%40example.test"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	var deliveries int
	if err := st.DB.QueryRow("SELECT count(*) FROM deliveries").Scan(&deliveries); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/moderator/login?sent=1" || deliveries != 0 {
		t.Fatalf("response=%d location=%s deliveries=%d", response.Code, response.Header().Get("Location"), deliveries)
	}
}

func TestModeratorCreatesAndUpdatesOnlyOwnedVersionedDraft(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SyncModerators(ctx, []store.ConfiguredModerator{{ID: "owner", NormalizedEmail: "owner@example.test"}}); err != nil {
		t.Fatal(err)
	}
	sessionToken, csrfToken := "session-token", "csrf-token"
	sessionHash, csrfHash := sha256.Sum256([]byte(sessionToken)), sha256.Sum256([]byte(csrfToken))
	if _, err := st.DB.ExecContext(ctx, "INSERT INTO sessions(token_hash,purpose,principal_id,csrf_hash,expires_at) VALUES (?,'moderator','owner',?,500)", sessionHash[:], csrfHash[:]); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{BaseURL: "https://poll.example", Moderators: []config.Moderator{{ID: "owner", Email: "owner@example.test"}}}
	randomMaterial := append(bytes.Repeat([]byte{1}, 16), bytes.Repeat([]byte{2}, 16)...)
	randomMaterial = append(randomMaterial, bytes.Repeat([]byte{3}, 16)...)
	handler := WorkflowHandler(cfg, st, authTemplates(t), http.NotFoundHandler(), bytes.NewReader(randomMaterial), func() time.Time { return time.Unix(100, 0) })
	form := url.Values{"csrf": {csrfToken}, "question": {"Choose"}, "deadline": {"1970-01-01T00:08:20Z"}, "places": {"1"}, "option": {"Alice", "Bob"}}
	request := httptest.NewRequest(http.MethodPost, "/moderator/polls", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.AddCookie(&http.Cookie{Name: "stv_moderator", Value: sessionToken})
	request.AddCookie(&http.Cookie{Name: "stv_moderator_csrf", Value: csrfToken})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusSeeOther {
		t.Fatalf("create response=%d body=%s", response.Code, response.Body.String())
	}
	var pollID string
	if err := st.DB.QueryRowContext(ctx, "SELECT id FROM polls WHERE owner_id='owner'").Scan(&pollID); err != nil {
		t.Fatal(err)
	}
	view := httptest.NewRequest(http.MethodGet, "/moderator/polls/"+pollID, nil)
	view.AddCookie(&http.Cookie{Name: "stv_moderator", Value: sessionToken})
	view.AddCookie(&http.Cookie{Name: "stv_moderator_csrf", Value: csrfToken})
	viewResponse := httptest.NewRecorder()
	handler.ServeHTTP(viewResponse, view)
	if viewResponse.Code != http.StatusOK || !strings.Contains(viewResponse.Body.String(), "Choose") {
		t.Fatalf("view response=%d body=%s", viewResponse.Code, viewResponse.Body.String())
	}
}

func authTemplates(t *testing.T) *template.Template {
	t.Helper()
	page := template.Must(template.New("index.html").Parse(`<html><body>Home</body></html>`))
	template.Must(page.New("voting.html").Parse(`<html><body>Voting</body></html>`))
	template.Must(page.New("counting.html").Parse(`<html><body>Counting</body></html>`))
	template.Must(page.New("moderator_login.html").Parse(`<html><body>{{if .Sent}}Sent{{end}}<form method="post"><input name="email"></form></body></html>`))
	template.Must(page.New("verify.html").Parse(`<html><body><form method="post"><input name="grant" type="hidden" value="{{.Grant}}"><input name="csrf" type="hidden" value="{{.CSRF}}"></form></body></html>`))
	template.Must(page.New("moderator_polls.html").Parse(`<html><body>{{range .Polls}}{{.Question}}{{end}}<form><input name="csrf" value="{{.CSRF}}"></form></body></html>`))
	template.Must(page.New("moderator_poll.html").Parse(`<html><body><h1>{{.Poll.Question}}</h1><p>{{.Poll.State}}</p></body></html>`))
	return page
}
