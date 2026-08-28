//go:build darwin || linux

package fhc

import (
	"bytes"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

const metadataName = "com.github.keinos.filehashcache.v1"

func openObject(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("open object without following symlinks: %w", err)
	}

	return os.NewFile(uintptr(fd), path), nil
}

func readMetadata(file *os.File, _ string) ([]byte, error) {
	name := metadataName
	if isLinux {
		name = "user." + name
	}
	size, err := unix.Fgetxattr(int(file.Fd()), name, nil)
	if err != nil {
		return nil, fmt.Errorf("get metadata size: %w", err)
	}
	data := make([]byte, size)
	size, err = unix.Fgetxattr(int(file.Fd()), name, data)
	if err != nil {
		return nil, fmt.Errorf("get metadata value: %w", err)
	}

	return data[:size], nil
}

func writeMetadata(file *os.File, path string, data []byte, _ time.Time) error {
	name := metadataName
	if isLinux {
		name = "user." + name
	}
	err := unix.Fsetxattr(int(file.Fd()), name, data, 0)
	if err != nil {
		return fmt.Errorf("set metadata value: %w", err)
	}
	stored, err := readMetadata(file, path)
	if err != nil {
		return err
	}
	if !bytes.Equal(stored, data) {
		return errMetadataVerification
	}

	return nil
}

func rejectPlatformPath(_ string) error { return nil }
