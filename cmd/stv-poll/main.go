// ABOUTME: Composes the STV Poll command and foundation HTTP service.
// ABOUTME: Help and version are side-effect-free; serve owns startup lifecycle.
package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/tigger-developer/Instant-Runoff-Polls-PR-STV-/internal/config"
	"github.com/tigger-developer/Instant-Runoff-Polls-PR-STV-/internal/store"
	"github.com/tigger-developer/Instant-Runoff-Polls-PR-STV-/internal/web"
	"github.com/tigger-developer/Instant-Runoff-Polls-PR-STV-/internal/workflow"
	"html/template"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

var version = "dev"

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
		if err := writeHelp(stdout); err != nil {
			fmt.Fprintf(stderr, "read help: %v\n", err)
			return 1
		}
		return 0
	}
	if len(args) == 1 && args[0] == "--version" {
		fmt.Fprintln(stdout, version)
		return 0
	}
	if len(args) == 1 && args[0] == "create-poll" {
		if err := createPollCommand(stdin, stdout); err != nil {
			fmt.Fprintf(stderr, "create poll failed: %v\n", err)
			return 1
		}
		return 0
	}
	if len(args) != 1 || args[0] != "serve" && args[0] != "process-due-work" {
		fmt.Fprintln(stderr, "invalid invocation: expected serve, process-due-work, or create-poll")
		return 2
	}
	if args[0] == "process-due-work" {
		summary, err := processDueWork()
		if encodeErr := json.NewEncoder(stdout).Encode(summary); encodeErr != nil {
			fmt.Fprintf(stderr, "write worker summary: %v\n", encodeErr)
			return 1
		}
		if err != nil {
			fmt.Fprintf(stderr, "process due work failed: %v\n", err)
			return 1
		}
		return 0
	}
	if err := serve(); err != nil {
		fmt.Fprintf(stderr, "serve failed: %v\n", err)
		return 1
	}
	return 0
}

func processDueWork() (workflow.Summary, error) {
	defaults := os.Getenv("DEFAULT_CONFIG_PATH")
	if defaults == "" {
		return workflow.Summary{Version: 1, Failed: 1}, errors.New("DEFAULT_CONFIG_PATH is required")
	}
	state := os.Getenv("STATE_DIRECTORY")
	if state == "" {
		return workflow.Summary{Version: 1, Failed: 1}, errors.New("STATE_DIRECTORY is required")
	}
	cfg, err := config.Load(defaults, os.Getenv("CONFIG_PATH"), os.Getenv("SECRETS_PATH"))
	if err != nil {
		return workflow.Summary{Version: 1, Failed: 1}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	st, err := store.Open(ctx, state)
	if err != nil {
		return workflow.Summary{Version: 1, Failed: 1}, err
	}
	defer st.Close()
	now := time.Now
	activeModerators := make(map[string]string, len(cfg.Moderators))
	for _, moderator := range cfg.Moderators {
		activeModerators[moderator.ID] = strings.ToLower(moderator.Email)
	}
	messageBuilder := workflow.NewInvitationMessageBuilder(st, cfg.BaseURL, cfg.Auth.KeyID, cfg.Auth.SigningKey, activeModerators, rand.Reader, now)
	handlers := map[string]workflow.WorkHandler{
		"close":    workflow.NewCloseHandler(st, rand.Reader, now),
		"count":    workflow.NewCountHandler(st, rand.Reader, now),
		"delivery": workflow.NewDeliveryHandler(st, workflow.SMTPTransport{Settings: cfg.SMTP}, messageBuilder, secureJitter, now),
	}
	return workflow.ProcessDueWork(ctx, st, handlers, secureToken, now)
}

func secureToken() string {
	value := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, value); err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(value)
}

func secureJitter() (float64, error) {
	value, err := rand.Int(rand.Reader, big.NewInt(1001))
	if err != nil {
		return 0, err
	}
	return float64(value.Int64()) / 10000, nil
}

func writeHelp(output io.Writer) error {
	help, err := os.ReadFile(filepath.Join("docs", "stv-poll-help.md"))
	if err != nil {
		return err
	}
	_, err = output.Write(help)
	return err
}

func serve() error {
	defaults := os.Getenv("DEFAULT_CONFIG_PATH")
	if defaults == "" {
		return fmt.Errorf("DEFAULT_CONFIG_PATH is required")
	}
	state := os.Getenv("STATE_DIRECTORY")
	if state == "" {
		return fmt.Errorf("STATE_DIRECTORY is required")
	}
	addr := os.Getenv("ADDR")
	if addr == "" {
		return fmt.Errorf("ADDR is required")
	}
	cfg, err := config.Load(defaults, os.Getenv("CONFIG_PATH"), os.Getenv("SECRETS_PATH"))
	if err != nil {
		return err
	}
	st, err := store.Open(context.Background(), state)
	if err != nil {
		return err
	}
	defer st.Close()
	templateDirectory, staticDirectory, err := assetDirectories(cfg.DataDirs)
	if err != nil {
		return err
	}
	tmpl, err := template.ParseGlob(filepath.Join(templateDirectory, "*.html"))
	if err != nil {
		return fmt.Errorf("parse templates: %w", err)
	}
	moderators := make([]store.ConfiguredModerator, 0, len(cfg.Moderators))
	for _, moderator := range cfg.Moderators {
		moderators = append(moderators, store.ConfiguredModerator{ID: moderator.ID, NormalizedEmail: strings.ToLower(moderator.Email)})
	}
	if err := st.SyncModerators(context.Background(), moderators); err != nil {
		return err
	}
	server := newHTTPServer(addr, web.WorkflowHandler(cfg, st, tmpl, http.FileServer(http.Dir(staticDirectory)), rand.Reader, time.Now), cfg.HTTP)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen on configured address: %w", err)
	}
	return runServer(ctx, server, listener, cfg.HTTP.ShutdownTimeout)
}

func runServer(ctx context.Context, server *http.Server, listener net.Listener, shutdownTimeout time.Duration) error {
	shutdownErr := make(chan error, 1)
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		shutdownErr <- server.Shutdown(shutdownCtx)
	}()
	err := server.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		if shutdownErr := <-shutdownErr; shutdownErr != nil {
			return fmt.Errorf("shutdown deadline exceeded: %w", shutdownErr)
		}
		return nil
	}
	return err
}

func newHTTPServer(addr string, handler http.Handler, settings config.HTTP) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: settings.ReadHeaderTimeout,
		ReadTimeout:       settings.ReadTimeout,
		WriteTimeout:      settings.WriteTimeout,
		IdleTimeout:       settings.IdleTimeout,
		MaxHeaderBytes:    64 * 1024,
	}
}

func assetDirectories(dataDirectories []string) (string, string, error) {
	var templates, static string
	for _, directory := range dataDirectories {
		info, err := os.Stat(directory)
		if err != nil || !info.IsDir() {
			return "", "", fmt.Errorf("asset directory %q is unavailable", directory)
		}
		if err := rejectSymlinks(directory); err != nil {
			return "", "", err
		}
		switch filepath.Base(directory) {
		case "templates":
			templates = directory
		case "static":
			static = directory
		}
	}
	if templates == "" || static == "" {
		return "", "", fmt.Errorf("configuration data_dirs must include templates and static directories")
	}
	return templates, static, nil
}

func rejectSymlinks(directory string) error {
	return filepath.WalkDir(directory, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("asset path %q must not be a symlink", path)
		}
		return nil
	})
}
