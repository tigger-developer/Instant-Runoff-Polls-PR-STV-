// ABOUTME: Verifies manual poll closure and count-audit export through admin services.
// ABOUTME: It protects frozen count evidence while excluding voter contact data.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tigger-developer/Instant-Runoff-Polls-PR-STV-/internal/config"
	"github.com/tigger-developer/Instant-Runoff-Polls-PR-STV-/internal/store"
	"github.com/tigger-developer/Instant-Runoff-Polls-PR-STV-/internal/workflow"
)

func TestAdminCLIProcessesPollLifecycleThroughConfiguredBoundary(t *testing.T) {
	state := t.TempDir()
	overlay := filepath.Join(t.TempDir(), "host.yaml")
	smtpHost, smtpPort, captured := startAdminSMTP(t)
	configuration := fmt.Sprintf("base_url: https://poll.example\nmoderators:\n  - id: owner\n    email: owner@example.test\nauth:\n  key_id: test-key\n  signing_key: AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=\nsmtp:\n  host: %s\n  port: %d\n  from: polls@example.test\n  tls_mode: development_plain\n", smtpHost, smtpPort)
	if err := os.WriteFile(overlay, []byte(configuration), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DEFAULT_CONFIG_PATH", filepath.Join(projectRoot(t), "config", "defaults.yaml"))
	t.Setenv("CONFIG_PATH", overlay)
	t.Setenv("SECRETS_PATH", "")
	t.Setenv("STATE_DIRECTORY", state)

	definition := `id: cli-poll
owner_id: owner
question: Where shall we eat?
options: [Cafe, Pizza]
places: 1
deadline: "2099-01-01T18:00:00Z"
participants:
  - name: Alex
    emails: [one@example.test, alias@example.test]
`
	var createdOutput, commandError bytes.Buffer
	if code := run([]string{"create-poll"}, strings.NewReader(definition), &createdOutput, &commandError); code != 0 {
		t.Fatalf("create code=%d stdout=%s stderr=%s", code, createdOutput.String(), commandError.String())
	}
	if !strings.Contains(createdOutput.String(), `"invitations":1`) {
		t.Fatalf("create output=%s", createdOutput.String())
	}
	var deliveryOutput bytes.Buffer
	commandError.Reset()
	if code := run([]string{"process-due-work"}, strings.NewReader(""), &deliveryOutput, &commandError); code != 0 || !strings.Contains(deliveryOutput.String(), `"smtp_accepted":1`) {
		t.Fatalf("delivery code=%d stdout=%s stderr=%s", code, deliveryOutput.String(), commandError.String())
	}
	transcript := <-captured
	for _, evidence := range []string{"RCPT TO:<one@example.test>", "RCPT TO:<alias@example.test>", "To: one@example.test, alias@example.test", "/auth/verify?grant="} {
		if !strings.Contains(transcript, evidence) {
			t.Fatalf("SMTP transcript missing %q: %s", evidence, transcript)
		}
	}
	if strings.Count(transcript, "RCPT TO:") != 2 || strings.Count(transcript, "/auth/verify?grant=") != 1 || strings.Count(transcript, "DATA\n") != 1 {
		t.Fatalf("SMTP cardinality mismatch in %s", transcript)
	}

	ctx := context.Background()
	st, err := store.Open(ctx, state)
	if err != nil {
		t.Fatal(err)
	}
	var participantID, optionID string
	if err := st.DB.QueryRowContext(ctx, "SELECT id FROM participants WHERE poll_id='cli-poll'").Scan(&participantID); err != nil {
		t.Fatal(err)
	}
	if err := st.DB.QueryRowContext(ctx, "SELECT id FROM options WHERE poll_id='cli-poll' ORDER BY display_order LIMIT 1").Scan(&optionID); err != nil {
		t.Fatal(err)
	}
	preferences, err := json.Marshal([]string{optionID})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceBallot(ctx, "cli-poll", participantID, 0, preferences, time.Now); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	var closedOutput bytes.Buffer
	commandError.Reset()
	if code := run([]string{"close-poll", "cli-poll"}, strings.NewReader(""), &closedOutput, &commandError); code != 0 || !strings.Contains(closedOutput.String(), `"counting_status":"pending"`) {
		t.Fatalf("close code=%d stdout=%s stderr=%s", code, closedOutput.String(), commandError.String())
	}
	var workerOutput bytes.Buffer
	commandError.Reset()
	if code := run([]string{"process-due-work"}, strings.NewReader(""), &workerOutput, &commandError); code != 0 || !strings.Contains(workerOutput.String(), `"counted":1`) {
		t.Fatalf("worker code=%d stdout=%s stderr=%s", code, workerOutput.String(), commandError.String())
	}
	var auditOutput bytes.Buffer
	commandError.Reset()
	if code := run([]string{"count-audit", "cli-poll"}, strings.NewReader(""), &auditOutput, &commandError); code != 0 {
		t.Fatalf("audit code=%d stdout=%s stderr=%s", code, auditOutput.String(), commandError.String())
	}
	for _, evidence := range []string{`"poll_id":"cli-poll"`, `"winners":["` + optionID + `"]`, `"input_fingerprint"`} {
		if !strings.Contains(auditOutput.String(), evidence) {
			t.Fatalf("audit missing %s: %s", evidence, auditOutput.String())
		}
	}
}

func startAdminSMTP(t *testing.T) (string, int, <-chan string) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	host, portText, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	captured := make(chan string, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			captured <- "accept failed: " + acceptErr.Error()
			return
		}
		defer connection.Close()
		reader := bufio.NewReader(connection)
		writer := bufio.NewWriter(connection)
		reply := func(line string) {
			_, _ = writer.WriteString(line + "\r\n")
			_ = writer.Flush()
		}
		var transcript strings.Builder
		reply("220 local test")
		for {
			line, readErr := reader.ReadString('\n')
			if readErr != nil {
				captured <- transcript.String()
				return
			}
			command := strings.ToUpper(strings.TrimSpace(line))
			switch {
			case strings.HasPrefix(command, "EHLO"):
				reply("250 localhost")
			case strings.HasPrefix(command, "MAIL FROM:"), strings.HasPrefix(command, "RCPT TO:"):
				transcript.WriteString(strings.TrimSpace(line) + "\n")
				reply("250 OK")
			case command == "DATA":
				transcript.WriteString("DATA\n")
				reply("354 End data")
				for {
					part, dataErr := reader.ReadString('\n')
					if dataErr != nil || strings.TrimSpace(part) == "." {
						break
					}
					transcript.WriteString(part)
				}
				reply("250 accepted")
			case command == "QUIT":
				reply("221 bye")
				captured <- transcript.String()
				return
			default:
				reply("250 OK")
			}
		}
	}()
	return host, port, captured
}

func TestManualCloseFreezesBallotsAndQueuesCount(t *testing.T) {
	ctx := context.Background()
	st := adminPollStore(t)
	defer st.Close()

	closed, err := closePoll(ctx, st, adminConfig(), "poll", bytes.NewReader(bytes.Repeat([]byte{7}, 64)), time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	if closed.PollID != "poll" || closed.State != "closed" || closed.CountingStatus != "pending" {
		t.Fatalf("closed=%#v", closed)
	}
	var snapshots, countWork int
	if err := st.DB.QueryRowContext(ctx, "SELECT count(*) FROM count_snapshots WHERE poll_id='poll'").Scan(&snapshots); err != nil {
		t.Fatal(err)
	}
	if err := st.DB.QueryRowContext(ctx, "SELECT count(*) FROM work_items WHERE poll_id='poll' AND kind='count' AND status='pending'").Scan(&countWork); err != nil {
		t.Fatal(err)
	}
	if snapshots != 1 || countWork != 1 {
		t.Fatalf("snapshots=%d count work=%d", snapshots, countWork)
	}
}

func TestCountAuditExportsFrozenInputAndResultWithoutVoterIdentity(t *testing.T) {
	ctx := context.Background()
	st := adminPollStore(t)
	defer st.Close()
	if _, err := closePoll(ctx, st, adminConfig(), "poll", bytes.NewReader(bytes.Repeat([]byte{7}, 64)), time.Unix(100, 0)); err != nil {
		t.Fatal(err)
	}
	summary, err := workflow.ProcessDueWork(ctx, st, map[string]workflow.WorkHandler{
		"count": workflow.NewCountHandler(st, bytes.NewReader(bytes.Repeat([]byte{8}, 256)), func() time.Time { return time.Unix(101, 0) }),
	}, func() string { return "claim" }, func() time.Time { return time.Unix(101, 0) })
	if err != nil || summary.Counted != 1 {
		t.Fatalf("summary=%#v error=%v", summary, err)
	}
	var output bytes.Buffer
	if err := exportCountAudit(ctx, st, adminConfig(), "poll", &output); err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(output.Bytes(), &document); err != nil {
		t.Fatalf("decode count audit: %v", err)
	}
	for _, field := range []string{"options", "input", "decisions", "result"} {
		if document[field] == nil {
			t.Fatalf("parsed audit lacks %s: %#v", field, document)
		}
	}
	rejectAuditIdentity(t, document)
	for _, required := range []string{`"poll_id":"poll"`, `"rule":"irish-guided-stv-v1"`, `"input_fingerprint"`, `"result"`, `"winners":["a"]`} {
		if !strings.Contains(output.String(), required) {
			t.Fatalf("audit missing %s: %s", required, output.String())
		}
	}
	for _, forbidden := range []string{"Alex", "one@example.test", `"participant_id"`, `"contact_id"`, `"person"`, `"contact"`} {
		if strings.Contains(output.String(), forbidden) {
			t.Fatalf("audit exposed %q: %s", forbidden, output.String())
		}
	}
}

func rejectAuditIdentity(t *testing.T, value any) {
	t.Helper()
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			switch key {
			case "participant_id", "contact_id", "email", "recipient_email":
				t.Fatalf("audit exposed identity field %q", key)
			}
			rejectAuditIdentity(t, child)
		}
	case []any:
		for _, child := range typed {
			rejectAuditIdentity(t, child)
		}
	case string:
		switch typed {
		case "Alex", "one@example.test", "person", "contact":
			t.Fatalf("audit exposed identity value %q", typed)
		}
	}
}

func TestAdminPollOperationsRejectInvalidScopeAndPrematureAudit(t *testing.T) {
	ctx := context.Background()
	st := adminPollStore(t)
	defer st.Close()

	if _, err := closePoll(ctx, st, adminConfig(), "not valid", bytes.NewReader(make([]byte, 64)), time.Unix(100, 0)); err == nil {
		t.Fatal("invalid poll ID was accepted")
	}
	if _, err := closePoll(ctx, st, config.Config{}, "poll", bytes.NewReader(make([]byte, 64)), time.Unix(100, 0)); err == nil {
		t.Fatal("unconfigured owner was authorized")
	}
	if err := exportCountAudit(ctx, st, adminConfig(), "poll", &bytes.Buffer{}); err == nil {
		t.Fatal("pending count audit was exported")
	}
	if err := exportCountAudit(ctx, st, adminConfig(), "poll", nil); err == nil {
		t.Fatal("nil audit output was accepted")
	}
	if _, err := closePoll(ctx, st, adminConfig(), "poll", bytes.NewReader(make([]byte, 64)), time.Unix(100, 0)); err != nil {
		t.Fatal(err)
	}
	if _, err := closePoll(ctx, st, adminConfig(), "poll", bytes.NewReader(make([]byte, 64)), time.Unix(101, 0)); err != nil {
		t.Fatalf("repeated close was not idempotent: %v", err)
	}
	var snapshots int
	if err := st.DB.QueryRowContext(ctx, "SELECT count(*) FROM count_snapshots WHERE poll_id='poll'").Scan(&snapshots); err != nil || snapshots != 1 {
		t.Fatalf("snapshots=%d error=%v", snapshots, err)
	}
}

func adminConfig() config.Config {
	return config.Config{Moderators: []config.Moderator{{ID: "owner", Email: "owner@example.test"}}}
}

func adminPollStore(t *testing.T) *store.Store {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SyncModerators(ctx, []store.ConfiguredModerator{{ID: "owner", NormalizedEmail: "owner@example.test"}}); err != nil {
		t.Fatal(err)
	}
	draft := store.DraftPoll{Question: "Question", Deadline: time.Unix(500, 0), Places: 1, Options: []store.PollOption{{ID: "a", Label: "Alpha"}, {ID: "b", Label: "Beta"}}}
	if err := st.CreateDraftPoll(ctx, "owner", "poll", draft, time.Unix(50, 0)); err != nil {
		t.Fatal(err)
	}
	participant := store.ElectorateParticipant{ID: "person", DisplayName: "Alex", Contacts: []store.Contact{{ID: "contact", DeliveryEmail: "one@example.test", NormalizedEmail: "one@example.test"}}}
	if err := st.ReplaceElectorate(ctx, "owner", "poll", 1, []store.ElectorateParticipant{participant}); err != nil {
		t.Fatal(err)
	}
	invitation := store.InvitationWork{WorkID: "invite", DeliveryID: "delivery", ParticipantID: "person", RecipientEmails: []string{"one@example.test"}}
	if _, err := st.OpenPoll(ctx, "owner", "poll", 2, "scheduled-close", []store.InvitationWork{invitation}, time.Unix(60, 0)); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceBallot(ctx, "poll", "person", 0, []byte(`["a","b"]`), func() time.Time { return time.Unix(90, 0) }); err != nil {
		t.Fatal(err)
	}
	return st
}
