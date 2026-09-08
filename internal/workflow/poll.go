// ABOUTME: Defines the frozen poll data passed into the counting boundary.
// ABOUTME: Mutable lifecycle rules remain in the transactional store.
package workflow

import "time"

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
	Options      []Option
	Places       int
	State        PollState
	Participants []Participant
	Ballots      map[string]Ballot
}
