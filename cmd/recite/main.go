package main

import (
	"fmt"
	"os"

	"github.com/xiaolai/recite/internal/cli"
	"github.com/xiaolai/recite/internal/harden"
)

func main() {
	// Before anything can hold a secret: no core dumps (AUTH-05).
	if err := harden.Process(); err != nil {
		fmt.Fprintf(os.Stderr, "recite: %v\n", err)
		os.Exit(1)
	}
	os.Exit(cli.New(os.Stdout, os.Stderr).Run(os.Args[1:]))
}
