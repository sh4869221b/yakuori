package main

import (
	"fmt"
	"os"

	"github.com/sh4869221b/yakuori/internal/cli"
)

func main() {
	if err := cli.PrepareProcess(os.Args); err != nil {
		fmt.Fprintln(os.Stderr, "yakuori: start WebGPU process:", err)
		os.Exit(cli.ExitFailure)
	}
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
