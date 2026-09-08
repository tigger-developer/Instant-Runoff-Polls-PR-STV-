// ABOUTME: Creates an open invited poll through the Exodan admin CLI boundary.
// ABOUTME: It validates voter identities before queuing one invitation per address.
package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/tigger-developer/Instant-Runoff-Polls-PR-STV-/internal/config"
	"github.com/tigger-developer/Instant-Runoff-Polls-PR-STV-/internal/store"
	"github.com/tigger-developer/Instant-Runoff-Polls-PR-STV-/internal/workflow"
)

var adminPollID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

type pollDefinition struct {
	ID           string                      `yaml:"id"`
	OwnerID      string                      `yaml:"owner_id"`
	Question     string                      `yaml:"question"`
	Options      []string                    `yaml:"options"`
	Places       int                         `yaml:"places"`
	Deadline     string                      `yaml:"deadline"`
	Announce     bool                        `yaml:"announce"`
	Participants []pollParticipantDefinition `yaml:"participants"`
}

type pollParticipantDefinition struct {
	Name   string   `yaml:"name"`
	Emails []string `yaml:"emails"`
}

type createdPoll struct {
	PollURL     string `json:"poll_url"`
	Invitations int    `json:"invitations"`
}

func createPollCommand(input io.Reader, output io.Writer) error {
	defaults := os.Getenv("DEFAULT_CONFIG_PATH")
	if defaults == "" {
		return errors.New("DEFAULT_CONFIG_PATH is required")
	}
	state := os.Getenv("STATE_DIRECTORY")
	if state == "" {
		return errors.New("STATE_DIRECTORY is required")
	}
	cfg, err := config.Load(defaults, os.Getenv("CONFIG_PATH"), os.Getenv("SECRETS_PATH"))
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	st, err := store.Open(ctx, state)
	if err != nil {
		return err
	}
	defer st.Close()
	moderators := make([]store.ConfiguredModerator, 0, len(cfg.Moderators))
	for _, moderator := range cfg.Moderators {
		moderators = append(moderators, store.ConfiguredModerator{ID: moderator.ID, NormalizedEmail: strings.ToLower(moderator.Email)})
	}
	if err := st.SyncModerators(ctx, moderators); err != nil {
		return err
	}
	created, err := createPollFromDefinition(ctx, st, cfg, input, rand.Reader, time.Now())
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(created)
}

func createPollFromDefinition(ctx context.Context, st *store.Store, cfg config.Config, input io.Reader, randomness io.Reader, now time.Time) (createdPoll, error) {
	if st == nil || input == nil || randomness == nil {
		return createdPoll{}, errors.New("poll creation dependencies are required")
	}
	var definition pollDefinition
	decoder := yaml.NewDecoder(io.LimitReader(input, 1<<20))
	decoder.KnownFields(true)
	if err := decoder.Decode(&definition); err != nil {
		return createdPoll{}, fmt.Errorf("decode poll definition: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return createdPoll{}, errors.New("poll definition must contain one YAML document")
	}
	definition.ID = strings.TrimSpace(definition.ID)
	definition.OwnerID = strings.TrimSpace(definition.OwnerID)
	definition.Question = strings.TrimSpace(definition.Question)
	if !adminPollID.MatchString(definition.ID) || !adminPollID.MatchString(definition.OwnerID) || len([]rune(definition.Question)) < 1 || len([]rune(definition.Question)) > 500 || len(definition.Options) < 2 || len(definition.Options) > 50 || definition.Places < 1 || definition.Places > len(definition.Options) || len(definition.Participants) < 1 || len(definition.Participants) > 1000 {
		return createdPoll{}, errors.New("invalid poll definition")
	}
	deadline, err := time.Parse(time.RFC3339, definition.Deadline)
	if err != nil || !deadline.After(now) {
		return createdPoll{}, errors.New("deadline must be a future RFC3339 timestamp")
	}
	options := make([]store.PollOption, 0, len(definition.Options))
	seenLabels := make(map[string]struct{}, len(definition.Options))
	for _, raw := range definition.Options {
		label := strings.TrimSpace(raw)
		if len([]rune(label)) < 1 || len([]rune(label)) > 200 {
			return createdPoll{}, errors.New("option labels must contain 1 to 200 characters")
		}
		if _, exists := seenLabels[label]; exists {
			return createdPoll{}, errors.New("option labels must be unique")
		}
		seenLabels[label] = struct{}{}
		id, err := adminID(randomness)
		if err != nil {
			return createdPoll{}, err
		}
		options = append(options, store.PollOption{ID: id, Label: label})
	}
	participantRows := make([]string, 0, len(definition.Participants))
	for index := range definition.Participants {
		definition.Participants[index].Name = strings.TrimSpace(definition.Participants[index].Name)
		if len([]rune(definition.Participants[index].Name)) < 1 || len([]rune(definition.Participants[index].Name)) > 200 || len(definition.Participants[index].Emails) < 1 {
			return createdPoll{}, errors.New("participants require a name and at least one email address")
		}
		participantRows = append(participantRows, strings.Join(definition.Participants[index].Emails, ","))
	}
	parsed, err := workflow.ParseElectorate(participantRows)
	if err != nil {
		return createdPoll{}, fmt.Errorf("parse participants: %w", err)
	}
	participants := make([]store.ElectorateParticipant, 0, len(parsed))
	for index, source := range parsed {
		participantID, err := adminID(randomness)
		if err != nil {
			return createdPoll{}, err
		}
		participant := store.ElectorateParticipant{ID: participantID, DisplayName: definition.Participants[index].Name}
		for _, address := range source.Addresses {
			contactID, err := adminID(randomness)
			if err != nil {
				return createdPoll{}, err
			}
			participant.Contacts = append(participant.Contacts, store.Contact{ID: contactID, DeliveryEmail: address.Delivery, NormalizedEmail: address.Normalized})
		}
		participants = append(participants, participant)
	}
	closeWorkID, err := adminID(randomness)
	if err != nil {
		return createdPoll{}, err
	}
	var invitations []store.InvitationWork
	for _, participant := range participants {
		workID, workErr := adminID(randomness)
		deliveryID, deliveryErr := adminID(randomness)
		if workErr != nil || deliveryErr != nil {
			return createdPoll{}, errors.New("generate invitation identifiers")
		}
		recipients := make([]string, 0, len(participant.Contacts))
		for _, contact := range participant.Contacts {
			recipients = append(recipients, contact.DeliveryEmail)
		}
		invitations = append(invitations, store.InvitationWork{WorkID: workID, DeliveryID: deliveryID, ParticipantID: participant.ID, RecipientEmails: recipients})
	}
	draft := store.DraftPoll{Question: definition.Question, Deadline: deadline, DisplayOffset: deadline.Format("-07:00"), Places: definition.Places, Announce: definition.Announce, Options: options}
	if err := st.CreateDraftPoll(ctx, definition.OwnerID, definition.ID, draft, now); err != nil {
		return createdPoll{}, fmt.Errorf("create poll: %w", err)
	}
	if err := st.ReplaceElectorate(ctx, definition.OwnerID, definition.ID, 1, participants); err != nil {
		return createdPoll{}, fmt.Errorf("store participants: %w", err)
	}
	if _, err := st.OpenPoll(ctx, definition.OwnerID, definition.ID, 2, closeWorkID, invitations, now); err != nil {
		return createdPoll{}, fmt.Errorf("open poll: %w", err)
	}
	return createdPoll{PollURL: strings.TrimRight(cfg.BaseURL, "/") + "/polls/" + definition.ID, Invitations: len(invitations)}, nil
}

func adminID(randomness io.Reader) (string, error) {
	value := make([]byte, 16)
	if _, err := io.ReadFull(randomness, value); err != nil {
		return "", fmt.Errorf("generate identifier: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}
