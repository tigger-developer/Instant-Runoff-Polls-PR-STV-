// ABOUTME: Serves the foundation landing page, static assets, and health route.
// ABOUTME: Keeps HTTP presentation independent from configuration and storage.
package web

import (
	"context"
	"encoding/json"
	"github.com/tigger-developer/Instant-Runoff-Polls-PR-STV-/internal/store"
	"html/template"
	"net/http"
	"time"
)

func Handler(baseURL string, st *store.Store, page *template.Template, assets http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) { _ = page.Execute(w, struct{ BaseURL string }{baseURL}) })
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 500*time.Millisecond)
		defer cancel()
		w.Header().Set("Content-Type", "application/json")
		if !st.Healthy(ctx) {
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "unavailable"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})
	mux.Handle("/static/", http.StripPrefix("/static/", assets))
	return mux
}
