// ABOUTME: Serves owner-scoped draft and poll lifecycle forms.
// ABOUTME: It validates bounded input before versioned store mutations.
package web

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/tigger-developer/Instant-Runoff-Polls-PR-STV-/internal/store"
)

type moderatorAccess struct {
	ID   string
	CSRF string
}

func (app *authApplication) moderatorAccess(response http.ResponseWriter, request *http.Request) (moderatorAccess, bool) {
	sessionCookie, err := request.Cookie("stv_moderator")
	if err != nil {
		http.Redirect(response, request, "/moderator/login", http.StatusSeeOther)
		return moderatorAccess{}, false
	}
	sessionHash := sha256.Sum256([]byte(sessionCookie.Value))
	session, err := app.store.SessionByHash(request.Context(), sessionHash[:], app.now())
	if err != nil || session.Purpose != "moderator" {
		http.Redirect(response, request, "/moderator/login", http.StatusSeeOther)
		return moderatorAccess{}, false
	}
	if _, active := app.moderatorID[session.PrincipalID]; !active {
		http.Error(response, "Not found", http.StatusNotFound)
		return moderatorAccess{}, false
	}
	csrf, err := request.Cookie("stv_moderator_csrf")
	if err != nil {
		http.Error(response, "Invalid session", http.StatusForbidden)
		return moderatorAccess{}, false
	}
	csrfHash := sha256.Sum256([]byte(csrf.Value))
	if subtle.ConstantTimeCompare(csrfHash[:], session.CSRFHash) != 1 {
		http.Error(response, "Invalid session", http.StatusForbidden)
		return moderatorAccess{}, false
	}
	return moderatorAccess{ID: session.PrincipalID, CSRF: csrf.Value}, true
}

func (app *authApplication) requireModeratorCSRF(response http.ResponseWriter, request *http.Request, access moderatorAccess, submitted string) bool {
	if submitted == "" || subtle.ConstantTimeCompare([]byte(submitted), []byte(access.CSRF)) != 1 {
		http.Error(response, "Invalid CSRF token", http.StatusForbidden)
		return false
	}
	return true
}

func (app *authApplication) getModeratorPolls(response http.ResponseWriter, request *http.Request) {
	securePrivateResponse(response)
	access, ok := app.moderatorAccess(response, request)
	if !ok {
		return
	}
	page, _ := strconv.Atoi(request.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	polls, err := app.store.OwnedPolls(request.Context(), access.ID, (page-1)*50)
	if err != nil {
		http.Error(response, "Service unavailable", http.StatusServiceUnavailable)
		return
	}
	app.render(response, "moderator_polls.html", map[string]any{"Polls": polls, "CSRF": access.CSRF})
}

func (app *authApplication) postModeratorPolls(response http.ResponseWriter, request *http.Request) {
	securePrivateResponse(response)
	access, ok := app.moderatorAccess(response, request)
	if !ok {
		return
	}
	values, problem := DecodeForm(response, request, FormSchema{Scalars: []string{"csrf", "question", "deadline", "places", "announce"}, Repeated: []string{"option"}}, 8<<20)
	if problem != nil {
		http.Error(response, problem.Error(), problem.Status)
		return
	}
	if !app.requireModeratorCSRF(response, request, access, values.Get("csrf")) {
		return
	}
	draft, err := parseDraft(values["question"], values["deadline"], values["places"], values["announce"], values["option"], app.now(), app.randomness)
	if err != nil {
		http.Error(response, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	pollID, err := randomID(app.randomness)
	if err != nil {
		http.Error(response, "Service unavailable", http.StatusServiceUnavailable)
		return
	}
	if err := app.store.CreateDraftPoll(request.Context(), access.ID, pollID, draft, app.now()); err != nil {
		http.Error(response, "Conflict", http.StatusConflict)
		return
	}
	http.Redirect(response, request, "/moderator/polls/"+pollID, http.StatusSeeOther)
}

func (app *authApplication) getModeratorPoll(response http.ResponseWriter, request *http.Request) {
	securePrivateResponse(response)
	access, ok := app.moderatorAccess(response, request)
	if !ok {
		return
	}
	poll, err := app.store.OwnedPoll(request.Context(), access.ID, request.PathValue("id"))
	if err != nil {
		http.Error(response, "Not found. Request access from the poll page.", http.StatusNotFound)
		return
	}
	app.render(response, "moderator_poll.html", map[string]any{"Poll": poll, "CSRF": access.CSRF})
}

func (app *authApplication) postModeratorPoll(response http.ResponseWriter, request *http.Request) {
	securePrivateResponse(response)
	access, ok := app.moderatorAccess(response, request)
	if !ok {
		return
	}
	values, problem := DecodeForm(response, request, FormSchema{Scalars: []string{"csrf", "version", "question", "deadline", "places", "announce"}, Repeated: []string{"option"}}, 8<<20)
	if problem != nil {
		http.Error(response, problem.Error(), problem.Status)
		return
	}
	if !app.requireModeratorCSRF(response, request, access, values.Get("csrf")) {
		return
	}
	draft, err := parseDraft(values["question"], values["deadline"], values["places"], values["announce"], values["option"], app.now(), app.randomness)
	version, versionErr := strconv.Atoi(values.Get("version"))
	if err != nil || versionErr != nil {
		http.Error(response, "Invalid poll fields", http.StatusUnprocessableEntity)
		return
	}
	if err := app.store.UpdateDraftPoll(request.Context(), access.ID, request.PathValue("id"), version, draft); err != nil {
		http.Error(response, "Conflict", http.StatusConflict)
		return
	}
	http.Redirect(response, request, request.URL.Path, http.StatusSeeOther)
}

func (app *authApplication) postPausePoll(response http.ResponseWriter, request *http.Request) {
	app.changePollState(response, request, "open", "paused")
}

func (app *authApplication) postResumePoll(response http.ResponseWriter, request *http.Request) {
	app.changePollState(response, request, "paused", "open")
}

func (app *authApplication) changePollState(response http.ResponseWriter, request *http.Request, from, to string) {
	securePrivateResponse(response)
	access, ok := app.moderatorAccess(response, request)
	if !ok {
		return
	}
	values, problem := DecodeForm(response, request, FormSchema{Scalars: []string{"csrf", "version"}}, 8<<20)
	if problem != nil {
		http.Error(response, problem.Error(), problem.Status)
		return
	}
	if !app.requireModeratorCSRF(response, request, access, values.Get("csrf")) {
		return
	}
	version, err := strconv.Atoi(values.Get("version"))
	if err != nil || app.store.SetPollState(request.Context(), access.ID, request.PathValue("id"), from, to, version, app.now()) != nil {
		http.Error(response, "Conflict", http.StatusConflict)
		return
	}
	http.Redirect(response, request, "/moderator/polls/"+request.PathValue("id"), http.StatusSeeOther)
}

func parseDraft(questionValues, deadlineValues, placesValues, announceValues, labels []string, now time.Time, randomness io.Reader) (store.DraftPoll, error) {
	if len(questionValues) != 1 || len(deadlineValues) != 1 || len(placesValues) != 1 || len(announceValues) > 1 || len(labels) < 2 || len(labels) > 50 {
		return store.DraftPoll{}, errors.New("invalid poll fields")
	}
	question := strings.TrimSpace(questionValues[0])
	deadline, err := time.Parse(time.RFC3339, deadlineValues[0])
	places, placesErr := strconv.Atoi(placesValues[0])
	if err != nil || placesErr != nil || !deadline.After(now) || places < 1 || places > len(labels) || len([]rune(question)) < 1 || len([]rune(question)) > 500 {
		return store.DraftPoll{}, errors.New("invalid poll fields")
	}
	draft := store.DraftPoll{Question: question, Deadline: deadline, DisplayOffset: deadline.Format("-07:00"), Places: places, Announce: len(announceValues) == 1 && announceValues[0] == "on"}
	seen := make(map[string]struct{}, len(labels))
	for _, rawLabel := range labels {
		label := strings.TrimSpace(rawLabel)
		if len([]rune(label)) < 1 || len([]rune(label)) > 200 {
			return store.DraftPoll{}, errors.New("invalid option")
		}
		if _, exists := seen[label]; exists {
			return store.DraftPoll{}, errors.New("duplicate option")
		}
		seen[label] = struct{}{}
		id, err := randomID(randomness)
		if err != nil {
			return store.DraftPoll{}, err
		}
		draft.Options = append(draft.Options, store.PollOption{ID: id, Label: label})
	}
	return draft, nil
}

func randomID(randomness io.Reader) (string, error) {
	value := make([]byte, 16)
	if _, err := io.ReadFull(randomness, value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}
