// ABOUTME: Defines the invited poll lifecycle and effective-ballot invariants.
// ABOUTME: It keeps deadline and optimistic-version checks at the mutation boundary.
package workflow

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrInvalidPoll     = errors.New("invalid poll")
	ErrPollFrozen      = errors.New("poll definition is frozen")
	ErrVersionConflict = errors.New("poll version conflict")
	ErrVotingClosed    = errors.New("voting is not open")
	ErrInvalidBallot   = errors.New("invalid ballot")
)

type PollState string

const (
	Draft  PollState = "draft"
	Open   PollState = "open"
	Paused PollState = "paused"
	Closed PollState = "closed"
)

type Option struct {
	ID    string
	Label string
}

type Ballot struct {
	ParticipantID string
	Preferences   []string
	Version       int
	AcceptedAt    time.Time
}

type Poll struct {
	ID           string
	OwnerID      string
	Question     string
	Options      []Option
	Places       int
	Deadline     time.Time
	State        PollState
	Version      int
	Participants []Participant
	Ballots      map[string]Ballot
	ClosedAt     time.Time
}

func (p *Poll) UpdateDefinition(question string, options []Option, places int, deadline time.Time, expectedVersion int) error {
	if p.State != Draft {
		return ErrPollFrozen
	}
	if p.Version != expectedVersion {
		return ErrVersionConflict
	}
	p.Question = question
	p.Options = append([]Option(nil), options...)
	p.Places = places
	p.Deadline = deadline
	p.Version++
	return nil
}

func (p *Poll) Open(now time.Time, expectedVersion int) error {
	if p.State != Draft {
		return ErrPollFrozen
	}
	if p.Version != expectedVersion {
		return ErrVersionConflict
	}
	if err := p.validateForOpening(now); err != nil {
		return err
	}
	p.State = Open
	p.Version++
	if p.Ballots == nil {
		p.Ballots = make(map[string]Ballot)
	}
	return nil
}

func (p *Poll) Pause(expectedVersion int) error {
	if p.Version != expectedVersion {
		return ErrVersionConflict
	}
	if p.State != Open {
		return ErrVotingClosed
	}
	p.State = Paused
	p.Version++
	return nil
}

func (p *Poll) Resume(now time.Time, expectedVersion int) error {
	if p.Version != expectedVersion {
		return ErrVersionConflict
	}
	if p.State != Paused || !now.Before(p.Deadline) {
		return ErrVotingClosed
	}
	p.State = Open
	p.Version++
	return nil
}

func (p *Poll) Close(now time.Time, expectedVersion int) (bool, error) {
	if p.Version != expectedVersion {
		return false, ErrVersionConflict
	}
	if p.State == Closed {
		return false, nil
	}
	if p.State != Open && p.State != Paused {
		return false, ErrVotingClosed
	}
	p.State = Closed
	p.ClosedAt = now
	p.Version++
	return true, nil
}

func (p *Poll) ReplaceBallot(participantID string, preferences []string, expectedVersion int, acceptedAt time.Time) error {
	if p.State != Open || !acceptedAt.Before(p.Deadline) {
		return ErrVotingClosed
	}
	if !p.hasParticipant(participantID) {
		return ErrInvalidBallot
	}
	current := p.Ballots[participantID]
	if current.Version != expectedVersion {
		return ErrVersionConflict
	}
	if err := p.validatePreferences(preferences); err != nil {
		return err
	}
	p.Ballots[participantID] = Ballot{ParticipantID: participantID, Preferences: append([]string(nil), preferences...), Version: current.Version + 1, AcceptedAt: acceptedAt}
	return nil
}

func (p Poll) validateForOpening(now time.Time) error {
	if length := len([]rune(strings.TrimSpace(p.Question))); length < 1 || length > 500 {
		return fmt.Errorf("%w: question length", ErrInvalidPoll)
	}
	if len(p.Options) < 2 || len(p.Options) > 50 || p.Places < 1 || p.Places > len(p.Options) {
		return fmt.Errorf("%w: options or places", ErrInvalidPoll)
	}
	if len(p.Participants) == 0 || !p.Deadline.After(now) {
		return fmt.Errorf("%w: participants or deadline", ErrInvalidPoll)
	}
	ids := make(map[string]struct{}, len(p.Options))
	labels := make(map[string]struct{}, len(p.Options))
	for _, option := range p.Options {
		label := strings.TrimSpace(option.Label)
		if option.ID == "" || len([]rune(label)) < 1 || len([]rune(label)) > 200 {
			return fmt.Errorf("%w: option", ErrInvalidPoll)
		}
		if _, exists := ids[option.ID]; exists {
			return fmt.Errorf("%w: duplicate option ID", ErrInvalidPoll)
		}
		if _, exists := labels[label]; exists {
			return fmt.Errorf("%w: duplicate option label", ErrInvalidPoll)
		}
		ids[option.ID] = struct{}{}
		labels[label] = struct{}{}
	}
	return nil
}

func (p Poll) hasParticipant(participantID string) bool {
	for _, participant := range p.Participants {
		if participant.ID == participantID {
			return true
		}
	}
	return false
}

func (p Poll) validatePreferences(preferences []string) error {
	if len(preferences) == 0 || len(preferences) > len(p.Options) {
		return ErrInvalidBallot
	}
	valid := make(map[string]struct{}, len(p.Options))
	for _, option := range p.Options {
		valid[option.ID] = struct{}{}
	}
	seen := make(map[string]struct{}, len(preferences))
	for _, preference := range preferences {
		if _, exists := valid[preference]; !exists {
			return ErrInvalidBallot
		}
		if _, exists := seen[preference]; exists {
			return ErrInvalidBallot
		}
		seen[preference] = struct{}{}
	}
	return nil
}
