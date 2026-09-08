// ABOUTME: Serves passwordless grant request and session exchange routes.
// ABOUTME: It keeps bearer URLs out of redirects, logs, and persistent storage.
package web

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"html/template"
	"io"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"github.com/tigger-developer/Instant-Runoff-Polls-PR-STV-/internal/config"
	"github.com/tigger-developer/Instant-Runoff-Polls-PR-STV-/internal/store"
	"github.com/tigger-developer/Instant-Runoff-Polls-PR-STV-/internal/workflow"
)

type authApplication struct {
	config      config.Config
	store       *store.Store
	templates   *template.Template
	randomness  io.Reader
	now         func() time.Time
	moderators  map[string]config.Moderator
	moderatorID map[string]string
}

func WorkflowHandler(cfg config.Config, st *store.Store, page *template.Template, assets http.Handler, randomness io.Reader, now func() time.Time) http.Handler {
	base := Handler(cfg.BaseURL, st, page, assets)
	application := &authApplication{config: cfg, store: st, templates: page, randomness: randomness, now: now, moderators: make(map[string]config.Moderator), moderatorID: make(map[string]string)}
	for _, moderator := range cfg.Moderators {
		normalized := strings.ToLower(moderator.Email)
		application.moderators[normalized] = moderator
		application.moderatorID[moderator.ID] = normalized
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /moderator/login", application.getModeratorLogin)
	mux.HandleFunc("POST /moderator/login", application.postModeratorLogin)
	mux.HandleFunc("GET /auth/verify", application.getVerify)
	mux.HandleFunc("POST /auth/verify", application.postVerify)
	mux.HandleFunc("POST /logout", application.postLogout)
	mux.Handle("/", base)
	return mux
}

func (app *authApplication) getModeratorLogin(response http.ResponseWriter, request *http.Request) {
	securePrivateResponse(response)
	app.render(response, "moderator_login.html", map[string]any{"Sent": request.URL.Query().Get("sent") == "1"})
}

func (app *authApplication) postModeratorLogin(response http.ResponseWriter, request *http.Request) {
	securePrivateResponse(response)
	values, problem := DecodeForm(response, request, FormSchema{Scalars: []string{"email"}}, 8<<20)
	if problem != nil {
		http.Error(response, problem.Error(), problem.Status)
		return
	}
	normalized, valid := normalizeAddress(values.Get("email"))
	requestHash := sha256.Sum256([]byte("moderator\x00" + normalized))
	allowed, err := app.store.RecordLinkRequest(request.Context(), requestHash[:], "moderator", "", app.now())
	if err != nil {
		http.Error(response, "Service unavailable", http.StatusServiceUnavailable)
		return
	}
	moderator, configured := app.moderators[normalized]
	if allowed && valid && configured {
		if err := app.queueModeratorLogin(request.Context(), moderator, normalized); err != nil {
			http.Error(response, "Service unavailable", http.StatusServiceUnavailable)
			return
		}
	}
	http.Redirect(response, request, "/moderator/login?sent=1", http.StatusSeeOther)
}

func (app *authApplication) queueModeratorLogin(ctx context.Context, moderator config.Moderator, recipient string) error {
	now := app.now()
	claims := workflow.GrantClaims{Version: 1, KeyID: app.config.Auth.KeyID, Purpose: workflow.ModeratorGrant, PrincipalID: moderator.ID, IssuedAt: now, ExpiresAt: now.Add(15 * time.Minute)}
	token, payload, err := workflow.IssuePersistableGrant(claims, app.config.Auth.SigningKey, app.randomness)
	if err != nil {
		return err
	}
	hash := sha256.Sum256([]byte(token))
	id := hex.EncodeToString(hash[:])
	material := store.GrantMaterial{ID: id, KeyID: claims.KeyID, TokenHash: hash[:], ClaimsJSON: payload, IssuedAt: claims.IssuedAt, ExpiresAt: claims.ExpiresAt}
	return app.store.QueueModeratorLogin(ctx, moderator.ID, recipient, "moderator-work:"+id, "moderator-delivery:"+id, material, now)
}

func (app *authApplication) getVerify(response http.ResponseWriter, request *http.Request) {
	securePrivateResponse(response)
	token := request.URL.Query().Get("grant")
	if _, _, err := app.authorizeGrant(request.Context(), token); err != nil {
		http.Error(response, "Invalid or expired link", http.StatusForbidden)
		return
	}
	issued, err := workflow.NewSession(workflow.PreAuthSession, "", "", app.now(), app.randomness)
	if err != nil || app.store.CreatePreAuthSession(request.Context(), issued.Record.TokenHash[:], issued.Record.CSRFHash[:], issued.Record.ExpiresAt, app.now()) != nil {
		http.Error(response, "Service unavailable", http.StatusServiceUnavailable)
		return
	}
	http.SetCookie(response, workflow.SessionCookie(workflow.PreAuthSession, issued.Token, app.config.HTTP.SecureCookies))
	app.render(response, "verify.html", map[string]any{"Grant": token, "CSRF": issued.CSRFToken})
}

func (app *authApplication) postVerify(response http.ResponseWriter, request *http.Request) {
	securePrivateResponse(response)
	values, problem := DecodeForm(response, request, FormSchema{Scalars: []string{"grant", "csrf"}}, 8<<20)
	if problem != nil {
		http.Error(response, problem.Error(), problem.Status)
		return
	}
	preAuthCookie, err := request.Cookie("stv_preauth")
	if err != nil {
		http.Error(response, "Invalid session", http.StatusForbidden)
		return
	}
	preAuthHash := sha256.Sum256([]byte(preAuthCookie.Value))
	preAuth, err := app.store.SessionByHash(request.Context(), preAuthHash[:], app.now())
	csrfHash := sha256.Sum256([]byte(values.Get("csrf")))
	if err != nil || preAuth.Purpose != "preauth" || subtle.ConstantTimeCompare(preAuth.CSRFHash, csrfHash[:]) != 1 {
		http.Error(response, "Invalid session", http.StatusForbidden)
		return
	}
	claims, grantHash, err := app.authorizeGrant(request.Context(), values.Get("grant"))
	if err != nil {
		http.Error(response, "Invalid or expired link", http.StatusForbidden)
		return
	}
	purpose := workflow.ParticipantSession
	if claims.Purpose == workflow.ModeratorGrant {
		if _, active := app.moderatorID[claims.PrincipalID]; !active {
			http.Error(response, "Invalid or expired link", http.StatusForbidden)
			return
		}
		purpose = workflow.ModeratorSession
	}
	issued, err := workflow.NewSession(purpose, claims.PrincipalID, claims.PollID, app.now(), app.randomness)
	if err != nil {
		http.Error(response, "Service unavailable", http.StatusServiceUnavailable)
		return
	}
	if purpose == workflow.ModeratorSession {
		err = app.store.ConsumeModeratorGrant(request.Context(), grantHash, claims.PrincipalID, issued.Record.TokenHash[:], issued.Record.CSRFHash[:], issued.Record.ExpiresAt, app.now())
	} else {
		err = app.store.CreateParticipantSession(request.Context(), grantHash, issued.Record.TokenHash[:], issued.Record.CSRFHash[:], issued.Record.ExpiresAt, app.now())
	}
	if err != nil {
		http.Error(response, "Invalid or expired link", http.StatusForbidden)
		return
	}
	_ = app.store.RevokeSession(request.Context(), preAuthHash[:], app.now())
	http.SetCookie(response, workflow.SessionCookie(purpose, issued.Token, app.config.HTTP.SecureCookies))
	destination := "/polls/" + claims.PollID
	if purpose == workflow.ModeratorSession {
		destination = "/moderator/polls"
	}
	http.Redirect(response, request, destination, http.StatusSeeOther)
}

func (app *authApplication) authorizeGrant(ctx context.Context, token string) (workflow.GrantClaims, []byte, error) {
	claims, err := workflow.VerifyGrant(token, app.config.Auth.KeyID, app.config.Auth.SigningKey, app.now())
	if err != nil {
		return workflow.GrantClaims{}, nil, err
	}
	hash := sha256.Sum256([]byte(token))
	persisted, err := app.store.GrantByHash(ctx, hash[:], app.now())
	if err != nil || persisted.KeyID != claims.KeyID || persisted.Purpose != string(claims.Purpose) || persisted.PrincipalID != claims.PrincipalID || persisted.PollID != claims.PollID || persisted.ContactID != claims.ContactID || claims.Purpose == workflow.ModeratorGrant && persisted.Consumed {
		return workflow.GrantClaims{}, nil, errors.New("grant scope mismatch")
	}
	return claims, hash[:], nil
}

func (app *authApplication) postLogout(response http.ResponseWriter, request *http.Request) {
	securePrivateResponse(response)
	for _, name := range []string{"stv_moderator", "stv_participant"} {
		if cookie, err := request.Cookie(name); err == nil {
			hash := sha256.Sum256([]byte(cookie.Value))
			_ = app.store.RevokeSession(request.Context(), hash[:], app.now())
			http.SetCookie(response, &http.Cookie{Name: name, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: app.config.HTTP.SecureCookies, SameSite: http.SameSiteLaxMode})
		}
	}
	http.Redirect(response, request, "/", http.StatusSeeOther)
}

func (app *authApplication) render(response http.ResponseWriter, name string, data any) {
	if err := app.templates.ExecuteTemplate(response, name, data); err != nil {
		http.Error(response, "Service unavailable", http.StatusServiceUnavailable)
	}
}

func securePrivateResponse(response http.ResponseWriter) {
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("Referrer-Policy", "no-referrer")
}

func normalizeAddress(value string) (string, bool) {
	trimmed := strings.TrimSpace(value)
	address, err := mail.ParseAddress(trimmed)
	if err != nil || address.Name != "" || address.Address != trimmed {
		return strings.ToLower(trimmed), false
	}
	return strings.ToLower(address.Address), true
}
