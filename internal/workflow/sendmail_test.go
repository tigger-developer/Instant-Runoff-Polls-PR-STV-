// ABOUTME: Verifies mail submission through Exodan's local process contract.
// ABOUTME: It protects the envelope arguments and complete RFC message framing.
package workflow

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSendmailTransportSubmitsOneMessageToAllRecipients(t *testing.T) {
	directory := t.TempDir()
	adapter := filepath.Join(directory, "exodan-sendmail")
	arguments := filepath.Join(directory, "arguments")
	messageFile := filepath.Join(directory, "message")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$CAPTURE_ARGUMENTS\"\n/usr/bin/tee \"$CAPTURE_MESSAGE\" >/dev/null\n"
	if err := os.WriteFile(adapter, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CAPTURE_ARGUMENTS", arguments)
	t.Setenv("CAPTURE_MESSAGE", messageFile)
	transport := SendmailTransport{Path: adapter, From: "stv-poll@lobb.ie"}
	mail, err := InvitationMessage([]string{"reader@example.test", "alias@example.test"}, "Question", "https://vote.lobb.ie/link")
	if err != nil {
		t.Fatal(err)
	}
	if err := transport.Send(context.Background(), mail); err != nil {
		t.Fatal(err)
	}
	argumentData, err := os.ReadFile(arguments)
	if err != nil {
		t.Fatal(err)
	}
	if string(argumentData) != "--from\nstv-poll@lobb.ie\nreader@example.test\nalias@example.test\n" {
		t.Fatalf("adapter arguments = %q", argumentData)
	}
	messageData, err := os.ReadFile(messageFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"From: stv-poll@lobb.ie\r\n", "To: reader@example.test, alias@example.test\r\n", "Subject: Vote: Question\r\n", "Please vote for Question"} {
		if !strings.Contains(string(messageData), required) {
			t.Fatalf("message lacks %q: %q", required, messageData)
		}
	}
}

func TestSendmailTransportClassifiesAdapterFailuresForDurableRetry(t *testing.T) {
	for _, test := range []struct {
		name      string
		exitCode  string
		temporary bool
	}{
		{name: "adapter rejection", exitCode: "2", temporary: false},
		{name: "temporary queue failure", exitCode: "75", temporary: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			adapter := filepath.Join(t.TempDir(), "exodan-sendmail")
			if err := os.WriteFile(adapter, []byte("#!/bin/sh\nexit "+test.exitCode+"\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			transport := SendmailTransport{Path: adapter, From: "stv-poll@lobb.ie"}
			err := transport.Send(context.Background(), Message{To: []string{"reader@example.test"}, Subject: "Question", Body: "Body"})
			failure, temporary := classifyDeliveryFailure(err)
			if temporary != test.temporary || !strings.Contains(failure, "mail") {
				t.Fatalf("failure=%q temporary=%t error=%v", failure, temporary, err)
			}
		})
	}
}
