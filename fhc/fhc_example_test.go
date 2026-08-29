package fhc_test

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/KEINOS/go-filehashcache/fhc"
)

func Example() {
	dir, err := os.MkdirTemp("", "fhc-example-")
	if err != nil {
		panic(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	path := filepath.Join(dir, "hello.txt")

	err = os.WriteFile(path, []byte("hello\n"), 0o600)
	if err != nil {
		panic(err)
	}

	fixedTime := time.Unix(1_700_000_000, 123_456_700)

	err = os.Chtimes(path, fixedTime, fixedTime)
	if err != nil {
		panic(err)
	}

	result, err := fhc.GetFileHashWithCache(path)

	fmt.Println(err == nil)
	fmt.Println(len(result.Hash))
	fmt.Println(result.Status == fhc.StatusMiss || result.Status == fhc.StatusUncached)

	// Output:
	// true
	// 16
	// true
}
