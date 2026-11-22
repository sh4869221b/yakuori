package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sh4869221b/yakuori/internal/config"
	"golang.org/x/sys/unix"
)

func TestContextValidate(t *testing.T) {
	const valid = "../contextpack/testdata/minimal-v1.json"
	dir := t.TempDir()
	invalid := filepath.Join(dir, "secret-invalid.json")
	if err := os.WriteFile(invalid, []byte(`{"secret-key":"secret-content"}`), 0600); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(dir, "secret-fifo")
	if err := unix.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "secret-link")
	target, err := filepath.Abs(valid)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name       string
		args       []string
		code       int
		out, error string
	}{
		{"group help", []string{"--help"}, ExitOK, contextUsage, ""},
		{"validate help", []string{"validate", "-h"}, ExitOK, contextValidateUsage, ""},
		{"valid", []string{"validate", "--", valid}, ExitOK, "fingerprint: 3170144643af3dff1ba01bef0ee04a54b4969f408459bfd0bf35126f417208bb\n", ""},
		{"missing action", nil, ExitUsage, "", "invalid context arguments"},
		{"missing file", []string{"validate"}, ExitUsage, "", "invalid context validate arguments"},
		{"unknown flag", []string{"validate", "--secret"}, ExitUsage, "", "invalid context validate arguments"},
		{"missing path", []string{"validate", filepath.Join(dir, "secret-absent")}, ExitFailure, "", "cannot open input file"},
		{"bad JSON member", []string{"validate", invalid}, ExitFailure, "", "unknown JSON member"},
		{"directory", []string{"validate", dir}, ExitFailure, "", "input must be a regular file"},
		{"FIFO", []string{"validate", fifo}, ExitFailure, "", "input must be a regular file"},
		{"symlink", []string{"validate", link}, ExitFailure, "", "cannot open input file"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var out, diag bytes.Buffer
			code := run(append([]string{"context"}, tt.args...), &out, &diag, func() (config.Paths, error) {
				t.Fatal("context validation accessed configuration")
				return config.Paths{}, nil
			})
			if code != tt.code || out.String() != tt.out || (diag.Len() == 0) != (tt.error == "") ||
				!strings.Contains(diag.String(), tt.error) || strings.Contains(diag.String(), "secret") || strings.Contains(diag.String(), dir) {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, out.String(), diag.String())
			}
		})
	}
}
