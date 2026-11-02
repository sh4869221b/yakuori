//go:build linux

// Command run executes the explicit W3 GPU acceptance workflow.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/sh4869221b/yakuori/tools/acceptance"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "acceptance run failed")
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) > 0 && args[0] == "worker" {
		if len(args) != 1 {
			return errors.New("invalid worker arguments")
		}
		var req acceptance.WorkerRequest
		dec := json.NewDecoder(io.LimitReader(os.Stdin, 1<<20))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			return err
		}
		fd := os.NewFile(3, "acceptance-result")
		if fd == nil {
			return errors.New("worker result descriptor missing")
		}
		defer fd.Close()
		result, err := acceptance.RunWorker(ctx, req, acceptance.OpenRegistered(req.ModelPaths))
		if encodeErr := json.NewEncoder(fd).Encode(result); encodeErr != nil {
			return encodeErr
		}
		return err
	}
	flags := flag.NewFlagSet("run", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	var opts acceptance.RunOptions
	flags.StringVar(&opts.Manifest, "manifest", "", "fixture manifest")
	flags.StringVar(&opts.Criteria, "criteria", "", "confirmed criteria")
	flags.StringVar(&opts.Root, "root", "", "fixture root")
	flags.StringVar(&opts.Repository, "repository", "", "Yakuori checkout containing the oracle")
	flags.StringVar(&opts.Evidence, "evidence", "", "empty private evidence directory")
	flags.StringVar(&opts.ModelID, "model-id", "", "registered selected model ID")
	flags.StringVar(&opts.ModelSHA256, "model-sha256", "", "expected model SHA-256")
	flags.StringVar(&opts.Game, "game", "", "game namespace")
	flags.StringVar(&opts.Artifact, "artifact", "", "artifact namespace")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected arguments")
	}
	_, err := acceptance.Run(ctx, opts)
	return err
}
