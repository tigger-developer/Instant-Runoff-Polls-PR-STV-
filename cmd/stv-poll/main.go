// ABOUTME: Composes the STV Poll command and foundation HTTP service.
// ABOUTME: Help and version are side-effect-free; serve owns startup lifecycle.
package main

import (
	"context"
	"embed"
	"fmt"
	"github.com/tigger-developer/Instant-Runoff-Polls-PR-STV-/internal/config"
	"github.com/tigger-developer/Instant-Runoff-Polls-PR-STV-/internal/store"
	"github.com/tigger-developer/Instant-Runoff-Polls-PR-STV-/internal/web"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
)

//go:embed templates static
var assets embed.FS
var version = "dev"

func main() {
	if len(os.Args) == 2 && (os.Args[1] == "-h" || os.Args[1] == "--help") {
		fmt.Println("Usage: stv-poll serve\n       stv-poll --version")
		return
	}
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		fmt.Println(version)
		return
	}
	if len(os.Args) < 2 || os.Args[1] != "serve" {
		fmt.Fprintln(os.Stderr, "invalid invocation: expected serve")
		os.Exit(2)
	}
	if err := serve(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
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
	tmpl, err := template.ParseFS(assets, "templates/*.html")
	if err != nil {
		return fmt.Errorf("parse templates: %w", err)
	}
	staticFS, err := fs.Sub(assets, "static")
	if err != nil {
		return err
	}
	server := &http.Server{Addr: addr, Handler: web.Handler(cfg.BaseURL, st, tmpl, http.FileServer(http.FS(staticFS))), ReadHeaderTimeout: cfg.HTTP.ReadHeaderTimeout, ReadTimeout: cfg.HTTP.ReadTimeout, WriteTimeout: cfg.HTTP.WriteTimeout, IdleTimeout: cfg.HTTP.IdleTimeout}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.HTTP.ShutdownTimeout)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	return server.ListenAndServe()
}
