package main

import (
	"os"

	"github.com/xiaolai/recite/internal/cli"
)

func main() {
	os.Exit(cli.New(os.Stdout, os.Stderr).Run(os.Args[1:]))
}
