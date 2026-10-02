package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestProcessContract(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "yakuori")
	build := exec.Command("go", "build", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, output)
	}
	for _, tt := range []struct {
		arg    string
		code   int
		stdout bool
	}{{"--help", 0, true}, {"--invalid", 2, false}, {"doctor", 1, false}} {
		t.Run(tt.arg, func(t *testing.T) {
			cmd := exec.Command(binary, tt.arg)
			cmd.Env = []string{"HOME=", "XDG_CONFIG_HOME=", "XDG_DATA_HOME=", "XDG_CACHE_HOME=", "XDG_STATE_HOME="}
			var out, diag bytes.Buffer
			cmd.Stdout = &out
			cmd.Stderr = &diag
			err := cmd.Run()
			code := 0
			if err != nil {
				if e, ok := err.(*exec.ExitError); ok {
					code = e.ExitCode()
				} else {
					t.Fatal(err)
				}
			}
			if code != tt.code || (out.Len() > 0) != tt.stdout || (diag.Len() > 0) == tt.stdout {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), diag.String())
			}
		})
	}
	// doctor is read-only: resolving empty directories must not create them.
	home := t.TempDir()
	cmd := exec.Command(binary, "doctor")
	cmd.Env = []string{"HOME=" + home}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("doctor: %v %s", err, out)
	}
	entries, err := os.ReadDir(home)
	if err != nil || len(entries) != 0 {
		t.Fatalf("unexpected writes: %v %v", entries, err)
	}
}
