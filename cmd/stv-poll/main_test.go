package main

import (
	"bytes"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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
