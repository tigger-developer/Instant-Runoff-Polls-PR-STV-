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
	page := template.Must(template.New("index").Parse(`<html><head><link rel="canonical" href="{{.BaseURL}}"></head><body><h1>STV Poll</h1></body></html>`))
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
