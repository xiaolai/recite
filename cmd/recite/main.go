package main

import (
	"fmt"
	"os"
	"runtime/debug"

	"github.com/xiaolai/recite/internal/cli"
	"github.com/xiaolai/recite/internal/harden"
	"github.com/xiaolai/recite/internal/pipeline"
)

func main() {
	// Before anything can hold a secret: no core dumps (AUTH-05).
	if err := harden.Process(); err != nil {
		fmt.Fprintf(os.Stderr, "recite: %v\n", err)
		os.Exit(1)
	}
	// The peak-heap target (design §14.4) assumes this soft limit; GOMEMLIMIT overrides it.
	if os.Getenv("GOMEMLIMIT") == "" {
		debug.SetMemoryLimit(pipeline.MemoryLimit)
	}
	os.Exit(cli.New(os.Stdout, os.Stderr).Run(os.Args[1:]))
}
