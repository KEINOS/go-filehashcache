//go:build windows

package fhc

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows"
)

const metadataName = "com.github.keinos.filehashcache.v1"

var errUnsupportedADS = errors.New("alternate data stream paths are not supported")

func openObject(path string) (*os.File, error) {
	pathPointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, fmt.Errorf("encode Windows path: %w", err)
	}
	handle, err := windows.CreateFile(
		pathPointer,
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL|
			windows.FILE_FLAG_BACKUP_SEMANTICS|
			windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return nil, fmt.Errorf("open object without following reparse points: %w", err)
	}

	return os.NewFile(uintptr(handle), path), nil
}

func readMetadata(_ *os.File, path string) ([]byte, error) {
	stream, err := openWindowsPath(path+":"+metadataName, windows.GENERIC_READ, windows.OPEN_EXISTING)
	if err != nil {
		return nil, fmt.Errorf("open metadata stream: %w", err)
	}
	defer func() { _ = stream.Close() }()

	data, err := io.ReadAll(stream)
	if err != nil {
		return nil, fmt.Errorf("read metadata stream: %w", err)
	}

	return data, nil
}

func writeMetadata(file *os.File, path string, data []byte, mtime time.Time) error {
	stream, err := openWindowsPath(path+":"+metadataName, windows.GENERIC_WRITE, windows.CREATE_ALWAYS)
	if err != nil {
		return fmt.Errorf("open metadata stream: %w", err)
	}
	written, err := stream.Write(data)
	if err != nil {
		_ = stream.Close()
		return fmt.Errorf("write metadata stream: %w", err)
	}
	if written != len(data) {
		_ = stream.Close()
		return io.ErrShortWrite
	}
	if err = stream.Close(); err != nil {
		return fmt.Errorf("close metadata stream: %w", err)
	}

	baseHandle, err := openWindowsHandle(path, windows.FILE_WRITE_ATTRIBUTES, windows.OPEN_EXISTING)
	if err != nil {
		return fmt.Errorf("open base object to restore LastWriteTime: %w", err)
	}
	defer func() { _ = windows.CloseHandle(baseHandle) }()

	fileTime := windows.NsecToFiletime(mtime.UnixNano())
	if err = windows.SetFileTime(baseHandle, nil, nil, &fileTime); err != nil {
		return fmt.Errorf("restore LastWriteTime: %w", err)
	}
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("verify base object time: %w", err)
	}
	if !info.ModTime().Equal(mtime) {
		return errMetadataVerification
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

func openWindowsPath(path string, access, creation uint32) (*os.File, error) {
	handle, err := openWindowsHandle(path, access, creation)
	if err != nil {
		return nil, fmt.Errorf("open Windows path: %w", err)
	}

	return os.NewFile(uintptr(handle), path), nil
}

func openWindowsHandle(path string, access, creation uint32) (windows.Handle, error) {
	pathPointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return windows.InvalidHandle, fmt.Errorf("encode Windows path: %w", err)
	}

	return windows.CreateFile(
		pathPointer,
		access,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		creation,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_BACKUP_SEMANTICS,
		0,
	)
}

func rejectPlatformPath(path string) error {
	volume := filepath.VolumeName(path)
	remainder := strings.TrimPrefix(path, volume)
	if strings.Contains(remainder, ":") {
		return fmt.Errorf("%w: %s", errUnsupportedADS, path)
	}

	return nil
}
