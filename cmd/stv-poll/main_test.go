package main

import (
	"bytes"
	"errors"
	"github.com/tigger-developer/Instant-Runoff-Polls-PR-STV-/internal/config"
	"html/template"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCLIOutcomesWithoutRuntimeConfiguration(t *testing.T) {
	binary := buildBinary(t)
	cases := []struct {
		name string
		args []string
		code int
		want string
	}{
		{name: "short help", args: []string{"-h"}, code: 0, want: "stv-poll serve"},
		{name: "long help", args: []string{"--help"}, code: 0, want: "stv-poll serve"},
		{name: "version", args: []string{"--version"}, code: 0, want: "dev"},
		{name: "unknown command", args: []string{"unknown"}, code: 2, want: "invalid invocation"},
		{name: "missing serve configuration", args: []string{"serve"}, code: 1, want: "DEFAULT_CONFIG_PATH is required"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			command := exec.Command(binary, tc.args...)
			command.Dir = projectRoot(t)
			var stdout, stderr bytes.Buffer
			command.Stdout = &stdout
			command.Stderr = &stderr
			err := command.Run()
			if exitCode(err) != tc.code {
				t.Fatalf("exit code = %d, stderr: %s", exitCode(err), stderr.String())
			}
			combined := stdout.String() + stderr.String()
			if !strings.Contains(combined, tc.want) {
				t.Fatalf("output = %q, want %q", combined, tc.want)
			}
		})
	}
}

func TestCLIHelpFormsAreEquivalent(t *testing.T) {
	binary := buildBinary(t)
	short := exec.Command(binary, "-h")
	long := exec.Command(binary, "--help")
	short.Dir = projectRoot(t)
	long.Dir = projectRoot(t)
	shortOutput, err := short.Output()
	if err != nil {
		t.Fatal(err)
	}
	longOutput, err := long.Output()
	if err != nil {
		t.Fatal(err)
	}
	if string(shortOutput) != string(longOutput) {
		t.Fatalf("help output differs: short=%q long=%q", shortOutput, longOutput)
	}
}

func TestRenderedLandingPassesTidy(t *testing.T) {
	tidy := os.Getenv("STV_POLL_TIDY")
	if tidy == "" {
		t.Skip("tidy is run through make lint")
	}
	tmpl, err := template.ParseGlob("templates/*.html")
	if err != nil {
		t.Fatal(err)
	}
	var rendered bytes.Buffer
	if err := tmpl.Execute(&rendered, struct{ BaseURL string }{"https://poll.example"}); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(tidy, "-errors", "-quiet", "-")
	command.Stdin = &rendered
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("tidy rendered landing page: %v\n%s", err, output)
	}
}

func TestAssetDirectoriesRejectMissingAndSymlinkedAssets(t *testing.T) {
	root := t.TempDir()
	templates := filepath.Join(root, "templates")
	static := filepath.Join(root, "static")
	if err := os.Mkdir(templates, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(static, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := assetDirectories([]string{templates, static}); err != nil {
		t.Fatalf("assetDirectories() error: %v", err)
	}
	if err := os.Symlink(filepath.Join(root, "outside"), filepath.Join(static, "escape")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := assetDirectories([]string{templates, static}); err == nil {
		t.Fatal("symlinked static asset unexpectedly accepted")
	}
}

func TestExecutableServesLandingStaticAssetAndHealth(t *testing.T) {
	binary := buildBinary(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	state := t.TempDir()
	defaults := filepath.Join(projectRoot(t), "config", "defaults.yaml")
	host := filepath.Join(t.TempDir(), "host.yaml")
	if err := os.WriteFile(host, []byte("base_url: https://poll.example\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(binary, "serve")
	command.Dir = filepath.Join(projectRoot(t), "cmd", "stv-poll")
	command.Env = append(os.Environ(), "DEFAULT_CONFIG_PATH="+defaults, "CONFIG_PATH="+host, "STATE_DIRECTORY="+state, "ADDR="+address)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if command.ProcessState == nil || !command.ProcessState.Exited() {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	})

	client := &http.Client{Timeout: 200 * time.Millisecond}
	deadline := time.Now().Add(3 * time.Second)
	var response *http.Response
	for time.Now().Before(deadline) {
		response, err = client.Get("http://" + address + "/healthz")
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("service did not become ready: %v; stderr: %s", err, stderr.String())
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		t.Fatalf("health status = %d", response.StatusCode)
	}
	response.Body.Close()

	page, err := client.Get("http://" + address + "/")
	if err != nil {
		t.Fatal(err)
	}
	pageBody := new(bytes.Buffer)
	_, _ = pageBody.ReadFrom(page.Body)
	page.Body.Close()
	if page.Header.Get("Content-Type") != "text/html; charset=utf-8" || !strings.Contains(pageBody.String(), "https://poll.example") {
		t.Fatalf("landing response = %s %q", page.Header.Get("Content-Type"), pageBody.String())
	}
	asset, err := client.Get("http://" + address + "/static/site.css")
	if err != nil {
		t.Fatal(err)
	}
	asset.Body.Close()
	if asset.StatusCode != http.StatusOK || !strings.HasPrefix(asset.Header.Get("Content-Type"), "text/css") {
		t.Fatalf("asset response = %d %q", asset.StatusCode, asset.Header.Get("Content-Type"))
	}
	if err := command.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err != nil {
		t.Fatalf("graceful shutdown: %v; stderr: %s", err, stderr.String())
	}
}

func TestExecutableRejectsMalformedTemplateBeforeListening(t *testing.T) {
	root := t.TempDir()
	templates := filepath.Join(root, "templates")
	static := filepath.Join(root, "static")
	state := filepath.Join(root, "state")
	for _, directory := range []string{templates, static, state} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(templates, "index.html"), []byte("{{ if }}"), 0o600); err != nil {
		t.Fatal(err)
	}
	defaults := filepath.Join(root, "defaults.yaml")
	contents := "base_url: https://poll.example\ndata_dirs: [" + templates + ", " + static + "]\nhttp:\n  read_header_timeout: 5s\n  read_timeout: 15s\n  write_timeout: 15s\n  idle_timeout: 60s\n  shutdown_timeout: 10s\n"
	if err := os.WriteFile(defaults, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(buildBinary(t), "serve")
	command.Dir = root
	command.Env = append(os.Environ(), "DEFAULT_CONFIG_PATH="+defaults, "STATE_DIRECTORY="+state, "ADDR=127.0.0.1:0")
	output, err := command.CombinedOutput()
	if exitCode(err) != 1 || !strings.Contains(string(output), "parse templates") {
		t.Fatalf("malformed template outcome = %v %q", err, output)
	}
}

func TestHTTPServerRejectsOversizedHeaders(t *testing.T) {
	server := newHTTPServer("127.0.0.1:0", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}), config.HTTP{ReadHeaderTimeout: time.Second, ReadTimeout: time.Second, WriteTimeout: time.Second, IdleTimeout: time.Second, ShutdownTimeout: time.Second})
	listener, err := net.Listen("tcp", server.Addr)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() { _ = server.Serve(listener) }()
	connection, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	request := "GET / HTTP/1.1\r\nHost: poll.example\r\nX-Long: " + strings.Repeat("x", 70*1024) + "\r\n\r\n"
	if _, err := connection.Write([]byte(request)); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 256)
	count, err := connection.Read(buffer)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(buffer[:count]), "431") && !strings.Contains(string(buffer[:count]), "400") {
		t.Fatalf("oversized header response = %q", buffer[:count])
	}
}

func TestHTTPServerClosesIncompleteHeadersAtDeadline(t *testing.T) {
	server := newHTTPServer("127.0.0.1:0", http.NotFoundHandler(), config.HTTP{ReadHeaderTimeout: 30 * time.Millisecond, ReadTimeout: time.Second, WriteTimeout: time.Second, IdleTimeout: time.Second, ShutdownTimeout: time.Second})
	listener, err := net.Listen("tcp", server.Addr)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() { _ = server.Serve(listener) }()
	connection, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if err := connection.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 1)
	count, err := connection.Read(buffer)
	if count != 0 || err == nil {
		t.Fatalf("incomplete header read = %d, %v", count, err)
	}
}

func buildBinary(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "stv-poll")
	command := exec.Command("go", "build", "-o", binary, ".")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build binary: %v\n%s", err, output)
	}
	return binary
}

func projectRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return exitError.ExitCode()
	}
	return -1
}
