package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/sh4869221b/yakuori/internal/contextpack"
	"golang.org/x/sys/unix"
)

const contextApproveUsage = `Usage: yakuori context approve --base BASE --output NEW --accept ID [--accept ID ...] REVIEWED

Merge reviewed Context Pack v1 records into BASE and confirm only explicitly selected IDs from REVIEWED.
BASE and REVIEWED must be local regular files. NEW must not exist.
`

func runContextApprove(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	var basePath, outputPath string
	var accept []string
	flags := flag.NewFlagSet("context approve", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&basePath, "base", "", "existing trusted Context Pack")
	flags.StringVar(&outputPath, "output", "", "new Context Pack file")
	flags.Func("accept", "record ID to confirm; repeat for multiple IDs", func(id string) error {
		if strings.TrimSpace(id) == "" {
			return errors.New("empty record ID")
		}
		accept = append(accept, id)
		return nil
	})
	flagErr := flags.Parse(args)
	if flagErr == flag.ErrHelp {
		return writeOutput(stdout, stderr, contextApproveUsage, nil)
	}
	if flagErr != nil || flags.NArg() != 1 || flags.Arg(0) == "" || basePath == "" || outputPath == "" || len(accept) == 0 {
		return reportFailure(stderr, ExitUsage, "invalid context approve arguments; use context approve --help")
	}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	if ctx.Err() != nil {
		return reportFailure(stderr, ExitFailure, "context approve: canceled")
	}
	base, err := readContextPackFile(basePath)
	if err != nil {
		return reportFailure(stderr, ExitFailure, "context approve: base: "+err.Error())
	}
	reviewed, err := readContextPackFile(flags.Arg(0))
	if err != nil {
		return reportFailure(stderr, ExitFailure, "context approve: reviewed: "+err.Error())
	}
	merged, err := contextpack.Approve(base.Pack, reviewed.Pack, accept)
	if err != nil {
		return reportFailure(stderr, ExitFailure, "context approve: "+err.Error())
	}
	if err := publishContextApproval(ctx, outputPath, merged.Canonical); err != nil {
		return reportFailure(stderr, ExitFailure, "context approve: "+err.Error())
	}
	result := fmt.Sprintf("fingerprint: %x\n", merged.Fingerprint)
	if n, err := io.WriteString(stdout, result); err != nil || n != len(result) {
		return reportFailure(stderr, ExitFailure, "context approve: approved pack saved, but write stdout failed")
	}
	return ExitOK
}

func publishContextApproval(ctx context.Context, path string, canonical []byte) (err error) {
	// Keep the raw directory path to preserve filesystem resolution of symlink/...
	dir, name := filepath.Split(path)
	if name == "" {
		return errors.New("output must name a new file")
	}
	if dir == "" {
		dir = "."
	}
	stage, err := os.CreateTemp(dir, ".context-approve-*")
	if err != nil {
		return errors.New("cannot create output stage")
	}
	defer func() {
		if err != nil {
			if removeErr := os.Remove(stage.Name()); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
				err = errors.Join(err, errors.New("cannot remove output stage"))
			}
		}
	}()
	_, writeErr := stage.Write(canonical)
	closeErr := stage.Close()
	if writeErr != nil || closeErr != nil {
		return errors.New("cannot write output stage")
	}
	// Rename is the commit point. A later cancellation leaves the pack visible.
	if ctx.Err() != nil {
		return errors.New("canceled")
	}
	err = unix.Renameat2(unix.AT_FDCWD, stage.Name(), unix.AT_FDCWD, path, unix.RENAME_NOREPLACE)
	if errors.Is(err, unix.EEXIST) {
		return errors.New("output file already exists")
	}
	if err != nil {
		return errors.New("cannot publish approved pack")
	}
	return nil
}
