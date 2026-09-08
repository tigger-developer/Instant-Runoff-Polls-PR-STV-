// ABOUTME: Composes the STV Poll command and foundation HTTP service.
// ABOUTME: Help and version are side-effect-free; serve owns startup lifecycle.
package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/tigger-developer/Instant-Runoff-Polls-PR-STV-/internal/config"
	"github.com/tigger-developer/Instant-Runoff-Polls-PR-STV-/internal/store"
	"github.com/tigger-developer/Instant-Runoff-Polls-PR-STV-/internal/web"
	"html/template"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

var version = "dev"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
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
	if len(args) != 1 || args[0] != "serve" {
		fmt.Fprintln(stderr, "invalid invocation: expected serve")
		return 2
	}
	if err := serve(); err != nil {
		fmt.Fprintf(stderr, "serve failed: %v\n", err)
		return 1
	}
	return 0
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
	server := newHTTPServer(addr, web.Handler(cfg.BaseURL, st, tmpl, http.FileServer(http.Dir(staticDirectory))), cfg.HTTP)
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
