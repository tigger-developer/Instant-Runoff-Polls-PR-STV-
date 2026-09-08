package main

import (
	"bytes"
	"os/exec"
	"testing"
)

func TestCLIHelpAndVersionReturnWithoutRuntimeConfiguration(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{name: "short help", args: []string{"-h"}},
		{name: "long help", args: []string{"--help"}},
		{name: "version", args: []string{"--version"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			command := exec.Command("go", append([]string{"run", "."}, tc.args...)...)
			var stdout, stderr bytes.Buffer
			command.Stdout = &stdout
			command.Stderr = &stderr
			err := command.Run()
			if err != nil {
				t.Fatalf("%s returned an error: %v; stderr: %s", tc.name, err, stderr.String())
			}
			if stdout.Len() == 0 {
				t.Fatalf("%s returned no stdout", tc.name)
			}
		})
	}
}
