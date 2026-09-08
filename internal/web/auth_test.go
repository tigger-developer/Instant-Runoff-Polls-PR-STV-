// ABOUTME: Verifies passwordless login requests and CSRF-bound grant exchange.
// ABOUTME: It exercises the HTTP boundary against real SQLite persistence.
package web

import (
	"bytes"
	"context"
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
	if len(cookies) != 1 || cookies[0].Name != "stv_moderator" || cookies[0].Value == token {
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

func authTemplates(t *testing.T) *template.Template {
	t.Helper()
	page := template.Must(template.New("index.html").Parse(`<html><body>Home</body></html>`))
	template.Must(page.New("voting.html").Parse(`<html><body>Voting</body></html>`))
	template.Must(page.New("counting.html").Parse(`<html><body>Counting</body></html>`))
	template.Must(page.New("moderator_login.html").Parse(`<html><body>{{if .Sent}}Sent{{end}}<form method="post"><input name="email"></form></body></html>`))
	template.Must(page.New("verify.html").Parse(`<html><body><form method="post"><input name="grant" type="hidden" value="{{.Grant}}"><input name="csrf" type="hidden" value="{{.CSRF}}"></form></body></html>`))
	return page
}
