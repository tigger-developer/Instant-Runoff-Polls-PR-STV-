// ABOUTME: Constructs approved isolated invitation and result email messages.
// ABOUTME: It rejects header injection and keeps one envelope recipient per message.
package workflow

import (
	"errors"
	"fmt"
	"mime"
	"net/mail"
	"strings"
)

var ErrInvalidMessage = errors.New("invalid message")

type Message struct {
	To      string
	Subject string
	Body    string
}

func InvitationMessage(recipient, question, link string) (Message, error) {
	if err := validateMessageFields(recipient, question, link); err != nil {
		return Message{}, err
	}
	body := fmt.Sprintf("Please vote for %s by clicking the link below:\n\n%s\n\nYou will be asked to vote by ranking your preferences 1, 2, 3 and so on.\nYou have what is called a *Single Transferable Vote*.\nEvery vote counts towards choosing the result.\nVoter preferences count.\n", question, link)
	return Message{To: recipient, Subject: encodeSubject("Vote: " + question), Body: body}, nil
}

func ResultMessage(recipient, question string, winners []string, noVotes bool) (Message, error) {
	if err := validateMessageFields(recipient, question); err != nil {
		return Message{}, err
	}
	for _, winner := range winners {
		if winner == "" || containsNewline(winner) {
			return Message{}, ErrInvalidMessage
		}
	}
	body := fmt.Sprintf("The result for %s is:\n\n%s\n\nThank you for voting.\n", question, strings.Join(winners, "\n"))
	if noVotes {
		if len(winners) != 0 {
			return Message{}, ErrInvalidMessage
		}
		body = fmt.Sprintf("No votes were received for %s, so there is no winner.\n", question)
	}
	return Message{To: recipient, Subject: encodeSubject("Result: " + question), Body: body}, nil
}

func validateMessageFields(recipient string, values ...string) error {
	address, err := mail.ParseAddress(recipient)
	if err != nil || address.Name != "" || address.Address != recipient || containsNewline(recipient) {
		return ErrInvalidMessage
	}
	for _, value := range values {
		if value == "" || containsNewline(value) {
			return ErrInvalidMessage
		}
	}
	return nil
}

func containsNewline(value string) bool {
	return strings.ContainsAny(value, "\r\n")
}

func encodeSubject(subject string) string {
	for _, character := range subject {
		if character > 127 {
			return mime.QEncoding.Encode("utf-8", subject)
		}
	}
	return subject
}
