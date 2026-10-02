// Package cli owns stdout/stderr and process exit status at the application edge.
package cli

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/sh4869221b/yakuori/internal/config"
)

const (
	ExitOK      = 0
	ExitFailure = 1
	ExitUsage   = 2
)

const usage = `Usage: yakuori <command>

Commands:
  help       Show this help
  doctor     Show resolved Yakuori storage directories (read-only)

Options:
  -h, --help Show this help

Foundation only: translation, models and storage are not implemented yet.
`

// Run never exits the process. Only main calls os.Exit.
func Run(args []string, stdout, stderr io.Writer) int {
	return run(args, stdout, stderr, func() (config.Paths, error) { return config.Resolve(os.Getenv) })
}

// resolve is the small fake-test seam; no engine or dependency framework yet.
func run(args []string, stdout, stderr io.Writer, resolve func() (config.Paths, error)) int {
	fail := func(code int, message string) int {
		// A failed diagnostic write must still return failure.
		_, _ = fmt.Fprintf(stderr, "yakuori: %s\n", message)
		return code
	}
	if len(args) == 0 {
		return fail(ExitUsage, "missing command; use --help")
	}
	if len(args) != 1 {
		return fail(ExitUsage, "unexpected arguments; use --help")
	}
	var result string
	switch args[0] {
	case "help", "-h", "--help":
		result = usage
	case "doctor":
		paths, err := resolve()
		if err != nil {
			return fail(ExitFailure, "resolve storage paths: "+err.Error())
		}
		result = fmt.Sprintf("config: %s\ndata: %s\ncache: %s\nstate: %s\n", paths.Config, paths.Data, paths.Cache, paths.State)
	default:
		// Do not echo user-supplied text into ordinary diagnostics.
		if strings.HasPrefix(args[0], "-") {
			return fail(ExitUsage, "unknown option; use --help")
		}
		return fail(ExitUsage, "unknown command; use --help")
	}
	n, err := io.WriteString(stdout, result)
	if err != nil || n != len(result) {
		return fail(ExitFailure, "write stdout failed")
	}
	return ExitOK
}
