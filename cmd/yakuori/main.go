package main

import (
	"github.com/sh4869221b/yakuori/internal/cli"
	"os"
)

func main() { os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr)) }
