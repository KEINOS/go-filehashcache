// Package fhc calculates change-detection hashes for files and directories.
//
// The package stores reusable content hashes in file-system metadata. It uses
// extended attributes on macOS and Linux and alternate data streams on
// Windows. A metadata failure does not prevent hash calculation.
package fhc

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/zeebo/xxh3"
)

const maxSnapshotAttempts = 3

var (
	// ErrDirectoryChanged identifies a directory that changed during all attempts.
	ErrDirectoryChanged = errors.New("directory changed during hashing")
	// ErrFileChanged identifies a regular file that changed during all attempts.
	ErrFileChanged = errors.New("file changed during hashing")

	errDirectoryNameTooLong = errors.New("directory name is too long")
	errFileNameTooLong      = errors.New("file name is too long")
	errMetadataVerification = errors.New("metadata verification failed")
	errNotDirectory         = errors.New("object is not a directory")
	errNotRegularFile       = errors.New("object is not a regular file")
	errUnsupportedObject    = errors.New("unsupported object type")
	errUnsupportedSymlink   = errors.New("unsupported symbolic link")
)

// CacheStatus reports if the package reused or stored all required cache data.
type CacheStatus string

const (
	// StatusHit means that all required cache records were valid.
	StatusHit CacheStatus = "HIT"
	// StatusMiss means that the package calculated and stored new cache data.
	StatusMiss CacheStatus = "MISS"
	// StatusUncached means that the hash is valid but some cache data was not stored.
	StatusUncached CacheStatus = "UNCACHED"
)

// Result contains a file or directory hash and its cache state.
type Result struct {
	CacheError error
	Hash       string
	Status     CacheStatus
}

// FileChangedError reports that a regular file changed during all attempts.
type FileChangedError struct{ Path string }

// Error returns the change error text.
func (err *FileChangedError) Error() string {
	return ErrFileChanged.Error() + ": " + err.Path
}

// Unwrap supports errors.Is with ErrFileChanged.
func (err *FileChangedError) Unwrap() error { return ErrFileChanged }

// DirectoryChangedError reports that a directory changed during all attempts.
type DirectoryChangedError struct{ Path string }

// Error returns the change error text.
func (err *DirectoryChangedError) Error() string {
	return ErrDirectoryChanged.Error() + ": " + err.Path
}

// Unwrap supports errors.Is with ErrDirectoryChanged.
func (err *DirectoryChangedError) Unwrap() error { return ErrDirectoryChanged }

// GetFileHashWithCache calculates a change-detection hash for a file or directory.
//
// The returned hash has 16 lowercase hexadecimal characters. A non-nil error
// means that no valid hash is available and the returned Result is zero-valued.
func GetFileHashWithCache(path string) (Result, error) {
	err := rejectPlatformPath(path)
	if err != nil {
		return Result{}, err
	}

	node, err := hashPath(path)
	if err != nil {
		return Result{}, err
	}

	return Result{
		Hash:       fmt.Sprintf("%016x", node.hash),
		Status:     node.status,
		CacheError: node.cacheError,
	}, nil
}

type nodeResult struct {
	cacheError    error
	status        CacheStatus
	hash          uint64
	fileCount     uint64
	directoryNode bool
}

type snapshotAttempt func(string) (nodeResult, bool, error)

func hashPath(path string) (nodeResult, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nodeResult{}, fmt.Errorf("inspect path: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nodeResult{}, fmt.Errorf("%w: %s", errUnsupportedSymlink, path)
	}

	switch {
	case info.Mode().IsRegular():
		return hashRegularFile(path)
	case info.IsDir():
		return hashDirectory(path)
	default:
		return nodeResult{}, fmt.Errorf("%w %s: %s", errUnsupportedObject, info.Mode().Type(), path)
	}
}

func hashRegularFile(path string) (nodeResult, error) {
	return retrySnapshot(path, hashRegularFileAttempt, &FileChangedError{Path: path})
}

func retrySnapshot(path string, attempt snapshotAttempt, changedError error) (nodeResult, error) {
	for range maxSnapshotAttempts {
		result, stable, err := attempt(path)
		if err != nil {
			return nodeResult{}, err
		}
		if stable {
			return result, nil
		}
	}

	return nodeResult{}, changedError
}

// hashRegularFileAttempt implements one complete stable-snapshot state transition.
func hashRegularFileAttempt(path string) (nodeResult, bool, error) { //nolint:cyclop
	file, err := openObject(path)
	if err != nil {
		return nodeResult{}, false, fmt.Errorf("open file: %w", err)
	}
	defer func() { _ = file.Close() }()

	before, err := file.Stat()
	if err != nil {
		return nodeResult{}, false, fmt.Errorf("inspect open file: %w", err)
	}
	if !before.Mode().IsRegular() {
		return nodeResult{}, false, fmt.Errorf("%w: %s", errNotRegularFile, path)
	}
	if !pathIdentifiesOpenObject(path, before) {
		return nodeResult{}, false, nil
	}

	contentHash, cacheHit, err := loadFileContentHash(file, path, before)
	if err != nil {
		return nodeResult{}, false, err
	}
	after, err := file.Stat()
	if err != nil {
		return nodeResult{}, false, fmt.Errorf("reinspect open file: %w", err)
	}
	if !sameFingerprint(before, after) || !pathIdentifiesOpenObject(path, before) {
		return nodeResult{}, false, nil
	}

	hash, err := deriveFileHash(contentHash, after, filepath.Base(path))
	if err != nil {
		return nodeResult{}, false, err
	}
	if cacheHit {
		return nodeResult{hash: hash, fileCount: 1, status: StatusHit}, true, nil
	}

	return storeFileCache(file, path, before, after, contentHash, hash)
}

func loadFileContentHash(file *os.File, path string, info os.FileInfo) (uint64, bool, error) {
	recordData, readErr := readMetadata(file, path)
	record, decodeErr := decodeRecord(recordData)
	cacheHit := readErr == nil && decodeErr == nil && record.recordType == recordTypeFile &&
		record.size == uint64(info.Size()) && //nolint:gosec // A regular file size is nonnegative.
		record.mtimeSec == info.ModTime().Unix() &&
		record.mtimeNsec == uint32(info.ModTime().Nanosecond()) //nolint:gosec // Nanoseconds fit uint32.

	if cacheHit {
		return record.contentHash, true, nil
	}

	_, err := file.Seek(0, io.SeekStart)
	if err != nil {
		return 0, false, fmt.Errorf("seek file: %w", err)
	}
	contentHash, err := calculateContentHash(file)
	if err != nil {
		return 0, false, fmt.Errorf("hash file content: %w", err)
	}

	return contentHash, false, nil
}

func calculateContentHash(reader io.Reader) (uint64, error) {
	hasher := xxh3.New()
	_, err := io.Copy(hasher, reader)
	if err != nil {
		return 0, fmt.Errorf("read content: %w", err)
	}

	return hasher.Sum64(), nil
}

func storeFileCache(
	file *os.File,
	path string,
	before os.FileInfo,
	after os.FileInfo,
	contentHash uint64,
	hash uint64,
) (nodeResult, bool, error) {
	return storeFileCacheWith(file, path, before, after, contentHash, hash, writeMetadata)
}

type metadataWriter func(*os.File, string, []byte, time.Time) error

func storeFileCacheWith(
	file *os.File,
	path string,
	before os.FileInfo,
	after os.FileInfo,
	contentHash uint64,
	hash uint64,
	write metadataWriter,
) (nodeResult, bool, error) {
	newRecord := cacheRecord{
		recordType:  recordTypeFile,
		contentHash: contentHash,
		size:        uint64(after.Size()), //nolint:gosec // A regular file size is nonnegative.
		mtimeSec:    after.ModTime().Unix(),
		mtimeNsec:   uint32(after.ModTime().Nanosecond()), //nolint:gosec // Nanoseconds fit uint32.
	}
	writeErr := write(file, path, encodeRecord(newRecord), after.ModTime())
	finalInfo, statErr := file.Stat()
	if statErr != nil {
		return nodeResult{}, false, fmt.Errorf("verify open file: %w", statErr)
	}
	if !sameFingerprint(after, finalInfo) || !pathIdentifiesOpenObject(path, before) {
		return nodeResult{}, false, nil
	}
	if writeErr != nil {
		return nodeResult{
			hash: hash, fileCount: 1, status: StatusUncached,
			cacheError: fmt.Errorf("store file cache: %w", writeErr),
		}, true, nil
	}

	return nodeResult{hash: hash, fileCount: 1, status: StatusMiss}, true, nil
}

func hashDirectory(path string) (nodeResult, error) {
	return retrySnapshot(path, hashDirectoryAttempt, &DirectoryChangedError{Path: path})
}

// hashDirectoryAttempt implements one complete recursive snapshot state transition.
func hashDirectoryAttempt(path string) (nodeResult, bool, error) { //nolint:cyclop,funlen
	directory, err := openObject(path)
	if err != nil {
		return nodeResult{}, false, fmt.Errorf("open directory: %w", err)
	}
	defer func() { _ = directory.Close() }()

	before, err := directory.Stat()
	if err != nil {
		return nodeResult{}, false, fmt.Errorf("inspect open directory: %w", err)
	}
	if !before.IsDir() {
		return nodeResult{}, false, fmt.Errorf("%w: %s", errNotDirectory, path)
	}
	if !pathIdentifiesOpenObject(path, before) {
		return nodeResult{}, false, nil
	}

	recordData, readErr := readMetadata(directory, path)
	cachedRecord, decodeErr := decodeRecord(recordData)
	entries, err := directory.ReadDir(-1)
	if err != nil {
		return nodeResult{}, false, fmt.Errorf("read directory: %w", err)
	}

	children, err := collectDirectoryChildren(path, entries)
	if err != nil {
		return nodeResult{}, false, err
	}

	slices.Sort(children.hashes)
	directoryHash := deriveDirectoryHash(children.fileCount, uint64(len(entries)), children.hashes)
	after, err := directory.Stat()
	if err != nil {
		return nodeResult{}, false, fmt.Errorf("reinspect open directory: %w", err)
	}
	if !before.ModTime().Equal(after.ModTime()) ||
		!os.SameFile(before, after) ||
		!pathIdentifiesOpenObject(path, before) {
		return nodeResult{}, false, nil
	}

	recordMatches := readErr == nil && decodeErr == nil &&
		cachedRecord.recordType == recordTypeDirectory &&
		cachedRecord.directoryHash == directoryHash &&
		cachedRecord.recursiveFileCount == children.fileCount &&
		cachedRecord.directEntryCount == uint64(len(entries))
	if recordMatches && children.allHit && len(children.cacheErrors) == 0 {
		return nodeResult{
			hash: directoryHash, fileCount: children.fileCount,
			status: StatusHit, directoryNode: true,
		}, true, nil
	}

	if !recordMatches {
		newRecord := cacheRecord{
			recordType:         recordTypeDirectory,
			directoryHash:      directoryHash,
			recursiveFileCount: children.fileCount,
			directEntryCount:   uint64(len(entries)),
		}

		writeErr := writeMetadata(directory, path, encodeRecord(newRecord), after.ModTime())
		if writeErr != nil {
			children.cacheErrors = append(children.cacheErrors, fmt.Errorf("store directory cache: %w", writeErr))
		}
	}

	finalInfo, statErr := directory.Stat()
	if statErr != nil {
		return nodeResult{}, false, fmt.Errorf("verify open directory: %w", statErr)
	}
	if !after.ModTime().Equal(finalInfo.ModTime()) ||
		!os.SameFile(after, finalInfo) ||
		!pathIdentifiesOpenObject(path, before) {
		return nodeResult{}, false, nil
	}

	status := StatusMiss
	if len(children.cacheErrors) != 0 {
		status = StatusUncached
	}

	return nodeResult{
		hash: directoryHash, fileCount: children.fileCount, status: status,
		cacheError: errors.Join(children.cacheErrors...), directoryNode: true,
	}, true, nil
}

type directoryChildren struct {
	hashes      []uint64
	cacheErrors []error
	fileCount   uint64
	allHit      bool
}

func collectDirectoryChildren(path string, entries []os.DirEntry) (directoryChildren, error) {
	result := directoryChildren{hashes: make([]uint64, 0, len(entries)), allHit: true}
	for _, entry := range entries {
		childPath := filepath.Join(path, entry.Name())
		if entry.Type()&os.ModeSymlink != 0 {
			return directoryChildren{}, fmt.Errorf("%w: %s", errUnsupportedSymlink, childPath)
		}
		child, err := hashPath(childPath)
		if err != nil {
			return directoryChildren{}, err
		}

		result.fileCount += child.fileCount
		result.allHit = result.allHit && child.status == StatusHit
		if child.cacheError != nil {
			result.cacheErrors = append(result.cacheErrors, child.cacheError)
		}

		childHash := child.hash
		if child.directoryNode {
			childHash, err = deriveDirectoryNodeHash(entry.Name(), child.hash, child.fileCount)
			if err != nil {
				return directoryChildren{}, err
			}
		}
		result.hashes = append(result.hashes, childHash)
	}

	return result, nil
}

func deriveFileHash(contentHash uint64, info os.FileInfo, name string) (uint64, error) {
	if uint64(len(name)) > math.MaxUint32 {
		return 0, errFileNameTooLong
	}
	hasher := xxh3.New()
	_, _ = hasher.Write([]byte("FHC-FILE-v1"))
	writeUint64(hasher, contentHash)
	writeUint64(hasher, uint64(info.ModTime().Unix()))       //nolint:gosec // The format requires two's complement.
	writeUint32(hasher, uint32(info.ModTime().Nanosecond())) //nolint:gosec // Nanoseconds fit uint32.
	writeUint32(hasher, uint32(len(name)))                   //nolint:gosec // The length was bounded above.
	_, _ = hasher.Write([]byte(name))
	writeUint64(hasher, uint64(info.Size())) //nolint:gosec // A regular file size is nonnegative.

	return hasher.Sum64(), nil
}

func deriveDirectoryNodeHash(name string, directoryHash, fileCount uint64) (uint64, error) {
	if uint64(len(name)) > math.MaxUint32 {
		return 0, errDirectoryNameTooLong
	}
	hasher := xxh3.New()
	_, _ = hasher.Write([]byte("FHC-DIR-NODE-v1"))
	writeUint32(hasher, uint32(len(name))) //nolint:gosec // The length was bounded above.
	_, _ = hasher.Write([]byte(name))
	writeUint64(hasher, directoryHash)
	writeUint64(hasher, fileCount)

	return hasher.Sum64(), nil
}

func deriveDirectoryHash(fileCount, entryCount uint64, childHashes []uint64) uint64 {
	hasher := xxh3.New()
	_, _ = hasher.Write([]byte("FHC-DIR-v1"))
	writeUint64(hasher, fileCount)
	writeUint64(hasher, entryCount)
	for _, hash := range childHashes {
		writeUint64(hasher, hash)
	}

	return hasher.Sum64()
}

func pathIdentifiesOpenObject(path string, openInfo os.FileInfo) bool {
	pathInfo, err := os.Lstat(path)

	return err == nil && pathInfo.Mode()&os.ModeSymlink == 0 && os.SameFile(openInfo, pathInfo)
}

func sameFingerprint(first, second os.FileInfo) bool {
	return first.Size() == second.Size() && first.ModTime().Equal(second.ModTime()) && os.SameFile(first, second)
}

func writeUint32(writer io.Writer, value uint32) {
	var data [4]byte
	binary.BigEndian.PutUint32(data[:], value)
	_, _ = writer.Write(data[:])
}

func writeUint64(writer io.Writer, value uint64) {
	var data [8]byte
	binary.BigEndian.PutUint64(data[:], value)
	_, _ = writer.Write(data[:])
}
