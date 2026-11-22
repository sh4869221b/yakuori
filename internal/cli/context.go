package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/sh4869221b/yakuori/internal/contextpack"
	"golang.org/x/sys/unix"
)

const contextUsage = `Usage: yakuori context <command>

Commands:
  build     Generate a Context Pack candidate from a local Generic JSON v1 file
  validate  Validate a Context Pack v1 JSON file and print its fingerprint

Use context <command> --help for options.
`

const contextValidateUsage = "Usage: yakuori context validate [--] FILE\n\nValidate a local Context Pack v1 JSON file and print its fingerprint.\n"

func runContext(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		return writeOutput(stdout, stderr, contextUsage, nil)
	}
	if len(args) > 0 {
		switch args[0] {
		case "build":
			return runContextBuild(ctx, args[1:], stdout, stderr)
		case "validate":
			return runContextValidate(args[1:], stdout, stderr)
		}
	}
	return reportFailure(stderr, ExitUsage, "invalid context arguments; use context --help")
}

func runContextValidate(args []string, stdout, stderr io.Writer) int {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		return writeOutput(stdout, stderr, contextValidateUsage, nil)
	}
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	} else if len(args) > 0 && strings.HasPrefix(args[0], "-") {
		return reportFailure(stderr, ExitUsage, "invalid context validate arguments; use context --help")
	}
	if len(args) != 1 || args[0] == "" {
		return reportFailure(stderr, ExitUsage, "invalid context validate arguments; use context --help")
	}

	// Inspect the opened descriptor. O_NONBLOCK also keeps an input FIFO
	// from blocking before Stat can reject it.
	file, err := os.OpenFile(args[0], os.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW, 0)
	if err != nil {
		return reportFailure(stderr, ExitFailure, "context validate: cannot open input file")
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		return reportFailure(stderr, ExitFailure, "context validate: input must be a regular file")
	}
	doc, err := contextpack.Read(file)
	closeErr := file.Close()
	if err != nil {
		return reportFailure(stderr, ExitFailure, "context validate: "+err.Error())
	}
	if closeErr != nil {
		return reportFailure(stderr, ExitFailure, "context validate: cannot close input file")
	}
	return writeOutput(stdout, stderr, fmt.Sprintf("fingerprint: %x\n", doc.Fingerprint), nil)
}
