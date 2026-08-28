// Program basic prints the change-detection hash and cache status of one path.
package main

import (
	"fmt"
	"os"

	"github.com/KEINOS/go-filehashcache/fhc"
)

const (
	expectedArgumentCount = 2
	usageExitCode         = 2
)

func main() {
	if len(os.Args) != expectedArgumentCount {
		fmt.Fprintln(os.Stderr, "usage: go run . PATH")
		os.Exit(usageExitCode)
	}

	result, err := fhc.GetFileHashWithCache(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	_, _ = fmt.Fprintln(os.Stdout, result.Hash, result.Status)

	if result.CacheError != nil {
		fmt.Fprintln(os.Stderr, result.CacheError)
	}
}
