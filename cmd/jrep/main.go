// Command jrep searches code by what it does, using TypeSafe's Jev model.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
)

const (
	exitMatch   = 0
	exitNoMatch = 1
	exitError   = 2
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	_, err := parseArgs(args)
	if errors.Is(err, errHelp) {
		fmt.Fprint(stdout, usage)
		return exitMatch
	}
	if err != nil {
		fmt.Fprintf(stderr, "jrep: %v\n\n%s", err, usage)
		return exitError
	}
	return exitNoMatch
}
