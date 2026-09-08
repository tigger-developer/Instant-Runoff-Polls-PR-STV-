// ABOUTME: Verifies isolated invitation and result message construction.
// ABOUTME: It protects recipient privacy, approved copy, and header boundaries.
package workflow

import (
	"fmt"
	"strings"
	"testing"
)

func TestInvitationMessageUsesApprovedCopyAndMultipleRecipients(t *testing.T) {
	message, err := InvitationMessage([]string{"reader@example.test", "alias@example.test"}, "Favourite book", "https://poll.example/polls/one?grant=secret")
	if err != nil {
		t.Fatal(err)
	}
	if len(message.To) != 2 || message.To[0] != "reader@example.test" || message.To[1] != "alias@example.test" || message.Subject != "Vote: Favourite book" {
		t.Fatalf("headers = %#v", message)
	}
	want := "Please vote for Favourite book by clicking the link below:\n\nhttps://poll.example/polls/one?grant=secret\n\nYou will be asked to vote by ranking your preferences 1, 2, 3 and so on.\nYou have what is called a *Single Transferable Vote*.\nEvery vote counts towards choosing the result.\nVoter preferences count.\n"
	if message.Body != want {
		t.Fatalf("body = %q", message.Body)
	}
}

func TestMessagesMatchExodanRecipientBoundary(t *testing.T) {
	recipients := make([]string, 50)
	for index := range recipients {
		recipients[index] = fmt.Sprintf("reader-%d@example.test", index)
	}
	if _, err := InvitationMessage(recipients, "Question", "https://poll.example/link"); err != nil {
		t.Fatalf("50 recipients rejected: %v", err)
	}
	recipients = append(recipients, "excess@example.test")
	if _, err := InvitationMessage(recipients, "Question", "https://poll.example/link"); err == nil {
		t.Fatal("51 recipients accepted")
	}
}

func TestMessagesRejectHeaderInjection(t *testing.T) {
	if _, err := InvitationMessage([]string{"reader@example.test"}, "Question\r\nBcc: stolen@example.test", "https://poll.example"); err == nil {
		t.Fatal("header injection succeeded")
	}
	if _, err := ResultMessage("reader@example.test", "Question", []string{"A\nB"}, false); err == nil {
		t.Fatal("winner injection succeeded")
	}
}

func TestResultMessagesUseApprovedOutcomes(t *testing.T) {
	result, err := ResultMessage("reader@example.test", "Question", []string{"A", "B"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Subject != "Result: Question" || result.Body != "The result for Question is:\n\nA\nB\n\nThank you for voting.\n" {
		t.Fatalf("result = %#v", result)
	}
	none, err := ResultMessage("reader@example.test", "Question", nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(none.Body, "No votes were received for Question, so there is no winner.") {
		t.Fatalf("zero turnout body = %q", none.Body)
	}
}

func TestAccessMessagesUseScopedSubjectsAndLinks(t *testing.T) {
	moderator, err := ModeratorLoginMessage("owner@example.test", "https://poll.example/auth/verify?grant=one")
	if err != nil || moderator.Subject != "Sign in to STV Poll" || !strings.Contains(moderator.Body, "grant=one") {
		t.Fatalf("moderator message=%#v error=%v", moderator, err)
	}
	participant, err := ParticipantReturnMessage("reader@example.test", "Favourite book", "https://poll.example/auth/verify?grant=two")
	if err != nil || participant.Subject != "Return to Favourite book" || !strings.Contains(participant.Body, "grant=two") {
		t.Fatalf("participant message=%#v error=%v", participant, err)
	}
}
