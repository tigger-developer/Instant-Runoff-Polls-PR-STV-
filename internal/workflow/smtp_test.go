// ABOUTME: Verifies the owned SMTP transport against a local protocol server.
// ABOUTME: It protects TLS policy, recipient isolation, and message framing.
package workflow

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/tigger-developer/Instant-Runoff-Polls-PR-STV-/internal/config"
)

func TestSMTPTransportSendsOneMessageToMultipleRecipientsInDevelopmentMode(t *testing.T) {
	address, captured := startSMTPServer(t)
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatal(err)
	}
	var port int
	if _, err := fmt.Sscan(portText, &port); err != nil {
		t.Fatal(err)
	}
	transport := SMTPTransport{Settings: config.SMTP{Host: host, Port: port, From: "polls@example.test", TLSMode: "development_plain"}}
	message, err := InvitationMessage([]string{"reader@example.test", "alias@example.test"}, "Question", "http://poll.test/link")
	if err != nil {
		t.Fatal(err)
	}
	if err := transport.Send(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	data := <-captured
	for _, want := range []string{"MAIL FROM:<polls@example.test>", "RCPT TO:<reader@example.test>", "RCPT TO:<alias@example.test>", "To: reader@example.test, alias@example.test", "Subject: Vote: Question", "Please vote for Question"} {
		if !strings.Contains(data, want) {
			t.Fatalf("SMTP capture lacks %q: %q", want, data)
		}
	}
	if strings.Count(data, "RCPT TO:") != 2 {
		t.Fatalf("recipient count in %q", data)
	}
}

func TestSMTPTransportRequiresAdvertisedSTARTTLS(t *testing.T) {
	address, _ := startSMTPServer(t)
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatal(err)
	}
	var port int
	if _, err := fmt.Sscan(portText, &port); err != nil {
		t.Fatal(err)
	}
	transport := SMTPTransport{Settings: config.SMTP{Host: host, Port: port, From: "polls@example.test", TLSMode: "starttls"}}
	if err := transport.Send(context.Background(), Message{To: []string{"reader@example.test"}, Subject: "Subject", Body: "Body\n"}); err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Fatalf("STARTTLS error = %v", err)
	}
}

func startSMTPServer(t *testing.T) (string, <-chan string) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	captured := make(chan string, 1)
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		defer connection.Close()
		_ = connection.SetDeadline(time.Now().Add(5 * time.Second))
		reader := bufio.NewReader(connection)
		writer := bufio.NewWriter(connection)
		writeSMTPLine(writer, "220 local test")
		var transcript strings.Builder
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			transcript.WriteString(line)
			command := strings.TrimSpace(line)
			switch {
			case strings.HasPrefix(command, "EHLO"):
				writeSMTPLine(writer, "250-localhost")
				writeSMTPLine(writer, "250 OK")
			case strings.HasPrefix(command, "MAIL FROM:"), strings.HasPrefix(command, "RCPT TO:"):
				writeSMTPLine(writer, "250 OK")
			case command == "DATA":
				writeSMTPLine(writer, "354 End data")
				for {
					dataLine, err := reader.ReadString('\n')
					if err != nil {
						return
					}
					if strings.TrimSpace(dataLine) == "." {
						break
					}
					transcript.WriteString(dataLine)
				}
				writeSMTPLine(writer, "250 accepted")
			case command == "QUIT":
				writeSMTPLine(writer, "221 bye")
				captured <- transcript.String()
				return
			default:
				writeSMTPLine(writer, "500 unsupported")
			}
		}
	}()
	return listener.Addr().String(), captured
}

func writeSMTPLine(writer *bufio.Writer, line string) {
	_, _ = writer.WriteString(line + "\r\n")
	_ = writer.Flush()
}
