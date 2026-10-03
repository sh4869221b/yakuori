package validate_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestAcceptedCannotBeForged(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	module := "module github.com/sh4869221b/yakuori/consumer\n\ngo 1.27.0\n\nrequire github.com/sh4869221b/yakuori v0.0.0\nreplace github.com/sh4869221b/yakuori => " + root + "\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(module), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct{ name, code, diagnostic string }{
		{"positive consumer", `package consumer
import (
 "github.com/sh4869221b/yakuori/internal/unit"
 "github.com/sh4869221b/yakuori/internal/validate"
)
func consume() (string, error) {
 id, err := unit.NewUnitID("fixture", "v1", "first"); if err != nil { return "", err }
 u, err := unit.NewTranslationUnit(id, []byte("source"), "en", "ja", nil); if err != nil { return "", err }
 session, err := unit.NewSession([]byte("artifact"), []unit.TranslationUnit{u}); if err != nil { return "", err }
 profile, err := validate.NewProfile([32]byte{1}); if err != nil { return "", err }
 accepted, err := validate.Validate(session, id, profile, "訳文"); if err != nil { return "", err }
 if err := validate.CheckBinding(session, accepted.UnitID(), profile, accepted); err != nil { return "", err }
 return accepted.Text(), nil
}`, ""},
		{"direct text assignment", `package consumer
import "github.com/sh4869221b/yakuori/internal/validate"
func forge() { var a validate.AcceptedTranslation; a.text = "unchecked" }
`, "a.text undefined (type validate.AcceptedTranslation has no field or method text, but does have method Text)"},
		{"text literal", `package consumer
import "github.com/sh4869221b/yakuori/internal/validate"
var a = validate.AcceptedTranslation{text: "unchecked"}
`, "cannot refer to unexported field text in struct literal"},
		{"binding literal", `package consumer
import "github.com/sh4869221b/yakuori/internal/validate"
var a = validate.AcceptedTranslation{binding: struct{}{}}
`, "cannot refer to unexported field binding in struct literal"},
		{"validated literal", `package consumer
import "github.com/sh4869221b/yakuori/internal/validate"
var a = validate.AcceptedTranslation{validated: true}
`, "cannot refer to unexported field validated in struct literal"},
	} {
		passed := t.Run(tt.name, func(t *testing.T) {
			if err := os.WriteFile(filepath.Join(dir, "consumer.go"), []byte(tt.code), 0600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("go", "test", "-count=1", ".")
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOTOOLCHAIN=local", "GOPROXY=off", "GOSUMDB=off")
			output, err := cmd.CombinedOutput()
			t.Logf("go test -count=1 .:\n%s", output)
			if tt.diagnostic == "" {
				if err != nil {
					t.Fatalf("positive consumer failed: %v", err)
				}
				return
			}
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) || !strings.Contains(string(output), tt.diagnostic) {
				t.Fatalf("expected private-field compiler diagnostic %q, got %v:\n%s", tt.diagnostic, err, output)
			}
		})
		if tt.diagnostic == "" && !passed {
			t.Fatal("positive consumer failed; negative compiler checks cannot establish field privacy")
		}
	}
}
