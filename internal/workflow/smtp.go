// ABOUTME: Sends one-recipient poll messages through the configured SMTP policy.
// ABOUTME: It requires verified TLS except for explicit loopback development capture.
package workflow

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/smtp"
	"strconv"
	"strings"
	"time"

	"github.com/tigger-developer/Instant-Runoff-Polls-PR-STV-/internal/config"
)

type SMTPTransport struct {
	Settings config.SMTP
}

func (transport SMTPTransport) Send(ctx context.Context, message Message) error {
	if err := validateMessageFields(message.To, message.Subject); err != nil || containsNewline(message.Subject) {
		return ErrInvalidMessage
	}
	attemptCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	address := net.JoinHostPort(transport.Settings.Host, strconv.Itoa(transport.Settings.Port))
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: transport.Settings.Host}
	var connection net.Conn
	var err error
	if transport.Settings.TLSMode == "implicit" {
		connection, err = (&tls.Dialer{NetDialer: &net.Dialer{}, Config: tlsConfig}).DialContext(attemptCtx, "tcp", address)
	} else {
		connection, err = (&net.Dialer{}).DialContext(attemptCtx, "tcp", address)
	}
	if err != nil {
		return fmt.Errorf("connect SMTP: %w", err)
	}
	defer connection.Close()
	deadline := time.Now().Add(10 * time.Second)
	if contextDeadline, ok := attemptCtx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	if err := connection.SetDeadline(deadline); err != nil {
		return fmt.Errorf("set SMTP deadline: %w", err)
	}
	client, err := smtp.NewClient(connection, transport.Settings.Host)
	if err != nil {
		return fmt.Errorf("start SMTP: %w", err)
	}
	defer client.Close()
	if transport.Settings.TLSMode == "starttls" {
		if supported, _ := client.Extension("STARTTLS"); !supported {
			return errors.New("SMTP server does not advertise required STARTTLS")
		}
		if err := client.StartTLS(tlsConfig); err != nil {
			return fmt.Errorf("start SMTP TLS: %w", err)
		}
	} else if transport.Settings.TLSMode != "implicit" && transport.Settings.TLSMode != "development_plain" {
		return errors.New("unsupported SMTP TLS mode")
	}
	if transport.Settings.Username != "" {
		auth := smtp.PlainAuth("", transport.Settings.Username, transport.Settings.Password, transport.Settings.Host)
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("authenticate SMTP: %w", err)
		}
	}
	if err := client.Mail(transport.Settings.From); err != nil {
		return fmt.Errorf("set SMTP sender: %w", err)
	}
	if err := client.Rcpt(message.To); err != nil {
		return fmt.Errorf("set SMTP recipient: %w", err)
	}
	data, err := client.Data()
	if err != nil {
		return fmt.Errorf("start SMTP data: %w", err)
	}
	body := strings.ReplaceAll(strings.ReplaceAll(message.Body, "\r\n", "\n"), "\n", "\r\n")
	wire := "From: " + transport.Settings.From + "\r\nTo: " + message.To + "\r\nSubject: " + message.Subject + "\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n" + body
	if _, err := data.Write([]byte(wire)); err != nil {
		_ = data.Close()
		return fmt.Errorf("write SMTP data: %w", err)
	}
	if err := data.Close(); err != nil {
		return fmt.Errorf("finish SMTP data: %w", err)
	}
	if err := client.Quit(); err != nil {
		return fmt.Errorf("finish SMTP session: %w", err)
	}
	return nil
}
