package main

import (
	"fmt"
	"os"
)

// The real router arrives in WP-0.3; until then every invocation is a usage error.
func main() {
	fmt.Fprintln(os.Stderr, "usage: recite <command> [flags] [text]")
	os.Exit(2)
}
