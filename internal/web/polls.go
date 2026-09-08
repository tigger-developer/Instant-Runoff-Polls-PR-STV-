// ABOUTME: Serves owner-scoped draft and poll lifecycle forms.
// ABOUTME: It validates bounded input before versioned store mutations.
package web

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/tigger-developer/Instant-Runoff-Polls-PR-STV-/internal/count"
	"github.com/tigger-developer/Instant-Runoff-Polls-PR-STV-/internal/store"
	"github.com/tigger-developer/Instant-Runoff-Polls-PR-STV-/internal/workflow"
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

func (app *authApplication) getParticipants(response http.ResponseWriter, request *http.Request) {
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
	participants, err := app.store.OwnedElectorate(request.Context(), access.ID, poll.ID)
	if err != nil {
		http.Error(response, "Service unavailable", http.StatusServiceUnavailable)
		return
	}
	rows := make([]string, 0, len(participants))
	for _, participant := range participants {
		addresses := make([]string, 0, len(participant.Contacts))
		for _, contact := range participant.Contacts {
			addresses = append(addresses, contact.DeliveryEmail)
		}
		rows = append(rows, strings.Join(addresses, ", "))
	}
	app.render(response, "participants.html", map[string]any{"Poll": poll, "Rows": strings.Join(rows, "\n"), "CSRF": access.CSRF})
}

func (app *authApplication) postParticipants(response http.ResponseWriter, request *http.Request) {
	securePrivateResponse(response)
	access, ok := app.moderatorAccess(response, request)
	if !ok {
		return
	}
	values, problem := DecodeForm(response, request, FormSchema{Scalars: []string{"csrf", "version", "participants", "copy_poll_id"}}, 8<<20)
	if problem != nil {
		http.Error(response, problem.Error(), problem.Status)
		return
	}
	if !app.requireModeratorCSRF(response, request, access, values.Get("csrf")) {
		return
	}
	version, err := strconv.Atoi(values.Get("version"))
	if err != nil {
		http.Error(response, "Invalid version", http.StatusUnprocessableEntity)
		return
	}
	var participants []store.ElectorateParticipant
	if sourceID := strings.TrimSpace(values.Get("copy_poll_id")); sourceID != "" {
		source, err := app.store.OwnedElectorate(request.Context(), access.ID, sourceID)
		if err != nil {
			http.Error(response, "Not found. Request access from the poll page.", http.StatusNotFound)
			return
		}
		participants, err = app.reidentifyElectorate(source)
		if err != nil {
			http.Error(response, "Service unavailable", http.StatusServiceUnavailable)
			return
		}
	} else {
		rows := strings.Split(strings.TrimSpace(values.Get("participants")), "\n")
		if len(rows) > 1000 {
			http.Error(response, "Too many participants", http.StatusUnprocessableEntity)
			return
		}
		parsed, err := workflow.ParseElectorate(rows)
		if err != nil {
			http.Error(response, err.Error(), http.StatusUnprocessableEntity)
			return
		}
		participants, err = app.identifyElectorate(parsed)
		if err != nil {
			http.Error(response, "Service unavailable", http.StatusServiceUnavailable)
			return
		}
	}
	if err := app.store.ReplaceElectorate(request.Context(), access.ID, request.PathValue("id"), version, participants); err != nil {
		http.Error(response, "Conflict", http.StatusConflict)
		return
	}
	http.Redirect(response, request, request.URL.Path, http.StatusSeeOther)
}

func (app *authApplication) identifyElectorate(parsed []workflow.Participant) ([]store.ElectorateParticipant, error) {
	participants := make([]store.ElectorateParticipant, 0, len(parsed))
	for _, parsedParticipant := range parsed {
		participantID, err := randomID(app.randomness)
		if err != nil {
			return nil, err
		}
		participant := store.ElectorateParticipant{ID: participantID}
		for _, address := range parsedParticipant.Addresses {
			contactID, err := randomID(app.randomness)
			if err != nil {
				return nil, err
			}
			participant.Contacts = append(participant.Contacts, store.Contact{ID: contactID, DeliveryEmail: address.Delivery, NormalizedEmail: address.Normalized})
		}
		participants = append(participants, participant)
	}
	return participants, nil
}

func (app *authApplication) reidentifyElectorate(source []store.ElectorateParticipant) ([]store.ElectorateParticipant, error) {
	parsed := make([]workflow.Participant, 0, len(source))
	for _, sourceParticipant := range source {
		participant := workflow.Participant{}
		for _, contact := range sourceParticipant.Contacts {
			participant.Addresses = append(participant.Addresses, workflow.Address{Delivery: contact.DeliveryEmail, Normalized: contact.NormalizedEmail})
		}
		parsed = append(parsed, participant)
	}
	return app.identifyElectorate(parsed)
}

func (app *authApplication) postOpenPoll(response http.ResponseWriter, request *http.Request) {
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
	if err != nil {
		http.Error(response, "Invalid version", http.StatusUnprocessableEntity)
		return
	}
	poll, err := app.store.OwnedPoll(request.Context(), access.ID, request.PathValue("id"))
	if err != nil {
		http.Error(response, "Not found. Request access from the poll page.", http.StatusNotFound)
		return
	}
	electorate, err := app.store.OwnedElectorate(request.Context(), access.ID, poll.ID)
	if err != nil || len(electorate) == 0 || len(poll.Options) < 2 || !poll.Deadline.After(app.now()) {
		http.Error(response, "Complete the poll before opening", http.StatusUnprocessableEntity)
		return
	}
	closeWorkID, err := randomID(app.randomness)
	if err != nil {
		http.Error(response, "Service unavailable", http.StatusServiceUnavailable)
		return
	}
	var invitations []store.InvitationWork
	for _, participant := range electorate {
		for _, contact := range participant.Contacts {
			workID, workErr := randomID(app.randomness)
			deliveryID, deliveryErr := randomID(app.randomness)
			if workErr != nil || deliveryErr != nil {
				http.Error(response, "Service unavailable", http.StatusServiceUnavailable)
				return
			}
			invitations = append(invitations, store.InvitationWork{WorkID: workID, DeliveryID: deliveryID, ContactID: contact.ID})
		}
	}
	changed, err := app.store.OpenPoll(request.Context(), access.ID, poll.ID, version, closeWorkID, invitations, app.now())
	if err != nil {
		http.Error(response, "Conflict", http.StatusConflict)
		return
	}
	_ = changed
	http.Redirect(response, request, "/moderator/polls/"+poll.ID, http.StatusSeeOther)
}

type ballotOption struct {
	ID    string
	Label string
	Rank  int
}

func (app *authApplication) participantAccess(response http.ResponseWriter, request *http.Request) (store.PersistedSession, string, bool) {
	sessionCookie, err := request.Cookie("stv_participant")
	if err != nil {
		return store.PersistedSession{}, "", false
	}
	sessionHash := sha256.Sum256([]byte(sessionCookie.Value))
	session, err := app.store.SessionByHash(request.Context(), sessionHash[:], app.now())
	if err != nil || session.Purpose != "participant" || session.PollID != request.PathValue("id") {
		return store.PersistedSession{}, "", false
	}
	csrf, err := request.Cookie("stv_participant_csrf")
	if err != nil {
		http.Error(response, "Invalid session", http.StatusForbidden)
		return store.PersistedSession{}, "", false
	}
	csrfHash := sha256.Sum256([]byte(csrf.Value))
	if subtle.ConstantTimeCompare(csrfHash[:], session.CSRFHash) != 1 {
		http.Error(response, "Invalid session", http.StatusForbidden)
		return store.PersistedSession{}, "", false
	}
	return session, csrf.Value, true
}

func (app *authApplication) getParticipantPoll(response http.ResponseWriter, request *http.Request) {
	securePrivateResponse(response)
	session, csrf, ok := app.participantAccess(response, request)
	if !ok {
		preAuthCSRF, available := app.ensurePreAuth(response, request)
		if !available {
			return
		}
		app.render(response, "poll_access.html", map[string]any{"PollID": request.PathValue("id"), "Sent": request.URL.Query().Get("sent") == "1", "CSRF": preAuthCSRF})
		return
	}
	view, err := app.store.PollForParticipant(request.Context(), session.PrincipalID, session.PollID)
	if err != nil {
		http.Error(response, "Not found. Request access from the poll page.", http.StatusNotFound)
		return
	}
	ranks := make(map[string]int, len(view.Preferences))
	for index, optionID := range view.Preferences {
		ranks[optionID] = index + 1
	}
	options := make([]ballotOption, 0, len(view.Poll.Options))
	for _, option := range view.Poll.Options {
		options = append(options, ballotOption{ID: option.ID, Label: option.Label, Rank: ranks[option.ID]})
	}
	app.render(response, "ballot.html", map[string]any{"Poll": view.Poll, "Options": options, "Version": view.BallotVersion, "CSRF": csrf, "Saved": request.URL.Query().Get("saved") == "1"})
}

func (app *authApplication) postBallot(response http.ResponseWriter, request *http.Request) {
	securePrivateResponse(response)
	session, csrf, ok := app.participantAccess(response, request)
	if !ok {
		http.Error(response, "Not found. Request access from the poll page.", http.StatusNotFound)
		return
	}
	values, problem := DecodeForm(response, request, FormSchema{Scalars: []string{"csrf", "version"}, Repeated: []string{"rank"}}, 8<<20)
	if problem != nil {
		http.Error(response, problem.Error(), problem.Status)
		return
	}
	if subtle.ConstantTimeCompare([]byte(values.Get("csrf")), []byte(csrf)) != 1 {
		http.Error(response, "Invalid CSRF token", http.StatusForbidden)
		return
	}
	view, err := app.store.PollForParticipant(request.Context(), session.PrincipalID, session.PollID)
	version, versionErr := strconv.Atoi(values.Get("version"))
	preferences, rankErr := rankedPreferences(view.Poll.Options, values["rank"])
	if err != nil || versionErr != nil || rankErr != nil {
		http.Error(response, "Ranks must be unique and consecutive from 1", http.StatusUnprocessableEntity)
		return
	}
	encodedPreferences, err := json.Marshal(preferences)
	if err != nil {
		http.Error(response, "Service unavailable", http.StatusServiceUnavailable)
		return
	}
	if err := app.store.ReplaceBallot(request.Context(), session.PollID, session.PrincipalID, version, encodedPreferences, app.now()); err != nil {
		http.Error(response, "Conflict", http.StatusConflict)
		return
	}
	http.Redirect(response, request, "/polls/"+session.PollID+"?saved=1", http.StatusSeeOther)
}

func rankedPreferences(options []store.PollOption, ranks []string) ([]string, error) {
	if len(ranks) != len(options) {
		return nil, errors.New("one rank per option is required")
	}
	byRank := make(map[int]string)
	for index, raw := range ranks {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		rank, err := strconv.Atoi(raw)
		if err != nil || rank < 1 || rank > len(options) {
			return nil, errors.New("invalid rank")
		}
		if _, duplicate := byRank[rank]; duplicate {
			return nil, errors.New("duplicate rank")
		}
		byRank[rank] = options[index].ID
	}
	if len(byRank) == 0 {
		return nil, errors.New("at least one rank is required")
	}
	preferences := make([]string, len(byRank))
	for rank := 1; rank <= len(byRank); rank++ {
		optionID, exists := byRank[rank]
		if !exists {
			return nil, errors.New("ranks are not consecutive")
		}
		preferences[rank-1] = optionID
	}
	return preferences, nil
}

func (app *authApplication) postClosePoll(response http.ResponseWriter, request *http.Request) {
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
	if err != nil {
		http.Error(response, "Invalid version", http.StatusUnprocessableEntity)
		return
	}
	work, err := app.store.PollForClose(request.Context(), access.ID, request.PathValue("id"))
	if err != nil {
		http.Error(response, "Not found. Request access from the poll page.", http.StatusNotFound)
		return
	}
	if work.State != "paused" || work.Version != version {
		http.Error(response, "Conflict", http.StatusConflict)
		return
	}
	_, _, err = app.store.ClosePoll(request.Context(), access.ID, work.PollID, version, func(current store.CloseWork) (store.CountSnapshot, error) {
		return workflow.BuildCloseSnapshot(current, app.randomness)
	}, "count:"+work.PollID, app.now())
	if err != nil {
		http.Error(response, "Unable to close poll", http.StatusServiceUnavailable)
		return
	}
	http.Redirect(response, request, "/moderator/polls/"+work.PollID+"/results", http.StatusSeeOther)
}

func (app *authApplication) getResults(response http.ResponseWriter, request *http.Request) {
	securePrivateResponse(response)
	access, ok := app.moderatorAccess(response, request)
	if !ok {
		return
	}
	stored, err := app.store.ResultForOwner(request.Context(), access.ID, request.PathValue("id"))
	if err != nil {
		http.Error(response, "Not found. Request access from the poll page.", http.StatusNotFound)
		return
	}
	var result workflow.ResultView
	if stored.Poll.CountingStatus == "no_votes" {
		result = workflow.ProjectZeroTurnout()
	} else if len(stored.ResultJSON) != 0 {
		var counted count.Result
		if json.Unmarshal(stored.ResultJSON, &counted) != nil {
			http.Error(response, "Service unavailable", http.StatusServiceUnavailable)
			return
		}
		result = workflow.ProjectResult(stored.Turnout, counted)
	}
	labels := make(map[string]string, len(stored.Poll.Options))
	for _, option := range stored.Poll.Options {
		labels[option.ID] = option.Label
	}
	app.render(response, "results.html", map[string]any{"Poll": stored.Poll, "Result": result, "Deliveries": stored.Deliveries, "Labels": labels})
}
