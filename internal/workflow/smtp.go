// ABOUTME: Submits complete poll messages through Exodan's host-local Sendmail adapter.
// ABOUTME: It passes one envelope sender and separate envelope recipients without SMTP credentials.
package workflow

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type SendmailTransport struct {
	Path string
	From string
}

type sendmailError struct {
	cause     error
	temporary bool
}

func (failure *sendmailError) Error() string { return failure.cause.Error() }
func (failure *sendmailError) Unwrap() error { return failure.cause }

func (transport SendmailTransport) Send(ctx context.Context, message Message) error {
	if !filepath.IsAbs(transport.Path) || validateMessageFields(transport.From) != nil || validateRecipients(message.To) != nil || validateValues(message.Subject) != nil {
		return ErrInvalidMessage
	}
	attemptCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	body := strings.ReplaceAll(strings.ReplaceAll(message.Body, "\r\n", "\n"), "\n", "\r\n")
	wire := "From: " + transport.From + "\r\nTo: " + strings.Join(message.To, ", ") + "\r\nSubject: " + message.Subject + "\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n" + body
	arguments := append([]string{"--from", transport.From}, message.To...)
	command := exec.CommandContext(attemptCtx, transport.Path, arguments...)
	command.Stdin = strings.NewReader(wire)
	if err := command.Run(); err != nil {
		failure := &sendmailError{cause: fmt.Errorf("submit message to local mail queue: %w", err), temporary: true}
		if exit, ok := err.(*exec.ExitError); ok {
			switch exit.ExitCode() {
			case 2, 64, 65, 67, 77:
				failure.temporary = false
			}
		}
		return failure
	}
	return nil
}
