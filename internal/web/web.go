// ABOUTME: Serves the foundation landing page, static assets, and health route.
// ABOUTME: Keeps HTTP presentation independent from configuration and storage.
package web

import (
	"context"
	"encoding/json"
	"github.com/tigger-developer/Instant-Runoff-Polls-PR-STV-/internal/store"
	"html/template"
	"log/slog"
	"net/http"
	"time"
)

func Handler(baseURL string, st *store.Store, page *template.Template, assets http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		if err := page.ExecuteTemplate(w, "index.html", struct{ BaseURL string }{baseURL}); err != nil {
			slog.Error("render landing page", "error", err)
		}
	})
	for path, templateName := range map[string]string{"/help/voting": "voting.html", "/help/counting": "counting.html"} {
		mux.HandleFunc("GET "+path, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Referrer-Policy", "no-referrer")
			if err := page.ExecuteTemplate(w, templateName, nil); err != nil {
				slog.Error("render help page", "template", templateName, "error", err)
			}
		})
	}
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 500*time.Millisecond)
		defer cancel()
		w.Header().Set("Content-Type", "application/json")
		if !st.Healthy(ctx) {
			w.WriteHeader(http.StatusServiceUnavailable)
			if err := json.NewEncoder(w).Encode(map[string]string{"status": "unavailable"}); err != nil {
				slog.Error("write health response", "error", err)
			}
			return
		}
		if err := json.NewEncoder(w).Encode(map[string]string{"status": "ok"}); err != nil {
			slog.Error("write health response", "error", err)
		}
	})
	mux.Handle("/static/", http.StripPrefix("/static/", assets))
	return mux
}
