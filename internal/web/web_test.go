package web

import (
	"context"
	"github.com/tigger-developer/Instant-Runoff-Polls-PR-STV-/internal/store"
	"html/template"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandlerServesExactRootAndHealth(t *testing.T) {
	page := template.Must(template.New("index.html").Parse(`<html><head><link rel="canonical" href="{{.BaseURL}}"></head><body><h1>STV Poll</h1></body></html>`))
	template.Must(page.New("voting.html").Parse(`<html><body><h1>How to vote</h1><label for="rank-a">Rank A</label><input id="rank-a" name="rank" type="number"><p>In a small poll, results may allow an inference.</p></body></html>`))
	template.Must(page.New("counting.html").Parse(`<html><body><h1>How votes are counted</h1><a href="https://en.wikipedia.org/wiki/Single_transferable_vote">Single transferable vote</a></body></html>`))
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	response := httptest.NewRecorder()
	database, err := store.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	handler := Handler("https://poll.example", database, page, http.NotFoundHandler())
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "https://poll.example") {
		t.Fatalf("root response = %d %q", response.Code, response.Body.String())
	}
	unknown := httptest.NewRecorder()
	handler.ServeHTTP(unknown, httptest.NewRequest(http.MethodGet, "/unknown", nil))
	if unknown.Code != http.StatusNotFound {
		t.Fatalf("unknown response = %d", unknown.Code)
	}
	health := httptest.NewRecorder()
	handler.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if health.Code != http.StatusOK || health.Body.String() != "{\"status\":\"ok\"}\n" {
		t.Fatalf("health response = %d %q", health.Code, health.Body.String())
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	unavailable := httptest.NewRecorder()
	handler.ServeHTTP(unavailable, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if unavailable.Code != http.StatusServiceUnavailable || unavailable.Body.String() != "{\"status\":\"unavailable\"}\n" {
		t.Fatalf("unavailable response = %d %q", unavailable.Code, unavailable.Body.String())
	}
	method := httptest.NewRecorder()
	handler.ServeHTTP(method, httptest.NewRequest(http.MethodPost, "/", nil))
	if method.Code != http.StatusMethodNotAllowed {
		t.Fatalf("method response = %d", method.Code)
	}
}

func TestHandlerServesPublicNoScriptHelpWithPrivacyNotice(t *testing.T) {
	page := template.Must(template.New("index.html").Parse(`<html><body><h1>Home</h1></body></html>`))
	template.Must(page.New("voting.html").Parse(`<html><body><h1>How to vote</h1><p>In a small poll, the results or turnout may allow someone to infer how a person voted or whether they took part.</p><form method="post"><label for="rank-a">Rank A</label><input id="rank-a" name="rank" type="number"></form></body></html>`))
	template.Must(page.New("counting.html").Parse(`<html><body><h1>How votes are counted</h1><a href="https://en.wikipedia.org/wiki/Single_transferable_vote">Learn about STV</a></body></html>`))
	database, err := store.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	handler := Handler("https://poll.example", database, page, http.NotFoundHandler())
	for path, want := range map[string]string{"/help/voting": "In a small poll", "/help/counting": "wikipedia.org/wiki/Single_transferable_vote"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), want) || strings.Contains(strings.ToLower(response.Body.String()), "javascript") {
			t.Fatalf("%s response = %d %q", path, response.Code, response.Body.String())
		}
		if response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("Referrer-Policy") != "no-referrer" {
			t.Fatalf("%s security headers = %#v", path, response.Header())
		}
	}
}
