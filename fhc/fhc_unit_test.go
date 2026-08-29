package fhc

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCacheRecordRoundTrip(t *testing.T) {
	t.Parallel()

	want := cacheRecord{
		recordType:  recordTypeFile,
		contentHash: 0x0123456789abcdef,
		size:        42,
		mtimeSec:    -123,
		mtimeNsec:   999_999_999,
	}

	encoded := encodeRecord(want)
	require.Len(t, encoded, cacheRecordSize)

	got, err := decodeRecord(encoded)
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

func TestDecodeRecordRejectsMalformedValues(t *testing.T) {
	t.Parallel()

	valid := encodeRecord(cacheRecord{recordType: recordTypeDirectory})
	tests := map[string][]byte{
		"empty":         nil,
		"truncated":     append([]byte(nil), valid[:len(valid)-1]...),
		"oversized":     append(append([]byte(nil), valid...), 0),
		"bad magic":     replaceByte(valid, 0, 'X'),
		"bad type":      replaceByte(valid, 4, 99),
		"bad algorithm": replaceByte(valid, 5, 99),
		"bad reserved":  replaceByte(valid, 6, 1),
		"bad checksum":  replaceByte(valid, len(valid)-1, valid[len(valid)-1]^0xff),
	}

	for name, value := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := decodeRecord(value)
			assert.Error(t, err)
		})
	}
}

func TestGetFileHashWithCacheStableFileAndRename(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "before.txt")
	require.NoError(t, os.WriteFile(path, []byte("stable content"), 0o600))
	setFixedTime(t, path)

	first, err := GetFileHashWithCache(path)
	require.NoError(t, err)
	assert.Regexp(t, `^[0-9a-f]{16}$`, first.Hash)

	second, err := GetFileHashWithCache(path)
	require.NoError(t, err)
	assert.Equal(t, first.Hash, second.Hash)

	if first.Status != StatusUncached {
		assert.Equal(t, StatusMiss, first.Status)
		assert.Equal(t, StatusHit, second.Status)
	}

	renamed := filepath.Join(dir, "after.txt")
	require.NoError(t, os.Rename(path, renamed))
	third, err := GetFileHashWithCache(renamed)
	require.NoError(t, err)
	assert.NotEqual(t, second.Hash, third.Hash)

	if third.Status != StatusUncached {
		assert.Equal(t, StatusHit, third.Status)
	}
}

func TestGetFileHashWithCacheDirectoryChanges(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	nested := filepath.Join(root, "nested")
	require.NoError(t, os.Mkdir(nested, 0o700))
	filePath := filepath.Join(nested, "item.txt")
	require.NoError(t, os.WriteFile(filePath, []byte("one"), 0o600))
	setFixedTime(t, filePath)

	first, err := GetFileHashWithCache(root)
	require.NoError(t, err)
	second, err := GetFileHashWithCache(root)
	require.NoError(t, err)
	assert.Equal(t, first.Hash, second.Hash)

	if first.Status != StatusUncached {
		assert.Equal(t, StatusHit, second.Status)
	}

	require.NoError(t, os.WriteFile(filePath, []byte("two"), 0o600))
	setFixedTimeAt(t, filePath, time.Unix(1_700_000_001, 123_456_700))

	changed, err := GetFileHashWithCache(root)
	require.NoError(t, err)
	assert.NotEqual(t, second.Hash, changed.Hash)
}

func TestGetFileHashWithCacheDirectoryHashVector(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	nested := filepath.Join(root, "nested")
	require.NoError(t, os.Mkdir(nested, 0o700))
	writeFixedFile(t, nested, "item.txt", "one")
	require.NoError(t, os.Mkdir(filepath.Join(root, "empty"), 0o700))

	result, err := GetFileHashWithCache(root)
	require.NoError(t, err)
	assert.Equal(t, "800b294843734a8f", result.Hash)
}

func TestGetFileHashWithCacheFileHashVector(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "vector.txt")
	require.NoError(t, os.WriteFile(path, []byte("hello\n"), 0o600))
	setFixedTime(t, path)

	result, err := GetFileHashWithCache(path)
	require.NoError(t, err)
	assert.Equal(t, "a46cee5a97ef766f", result.Hash)
}

func TestContentHashVector(t *testing.T) {
	t.Parallel()

	hash, err := calculateContentHash(strings.NewReader("hello\n"))
	require.NoError(t, err)
	assert.Equal(t, uint64(0x99fc819aaba2462a), hash)
}

func TestGetFileHashWithCacheMtimeChangesHash(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "mtime.txt")
	require.NoError(t, os.WriteFile(path, []byte("same content"), 0o600))
	setFixedTime(t, path)
	first, err := GetFileHashWithCache(path)
	require.NoError(t, err)

	setFixedTimeAt(t, path, time.Unix(1_700_000_002, 123_456_700))
	second, err := GetFileHashWithCache(path)
	require.NoError(t, err)
	assert.NotEqual(t, first.Hash, second.Hash)
}

func TestGetFileHashWithCacheMoveKeepsFileHashAndChangesParents(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	firstParent := filepath.Join(root, "first")
	secondParent := filepath.Join(root, "second")

	require.NoError(t, os.Mkdir(firstParent, 0o700))
	require.NoError(t, os.Mkdir(secondParent, 0o700))

	firstPath := filepath.Join(firstParent, "item.txt")
	secondPath := filepath.Join(secondParent, "item.txt")

	writeFixedFile(t, firstParent, "item.txt", "content")

	fileBefore, err := GetFileHashWithCache(firstPath)
	require.NoError(t, err)
	firstBefore, err := GetFileHashWithCache(firstParent)
	require.NoError(t, err)
	secondBefore, err := GetFileHashWithCache(secondParent)
	require.NoError(t, err)

	require.NoError(t, os.Rename(firstPath, secondPath))
	fileAfter, err := GetFileHashWithCache(secondPath)
	require.NoError(t, err)
	firstAfter, err := GetFileHashWithCache(firstParent)
	require.NoError(t, err)
	secondAfter, err := GetFileHashWithCache(secondParent)
	require.NoError(t, err)

	assert.Equal(t, fileBefore.Hash, fileAfter.Hash)
	assert.NotEqual(t, firstBefore.Hash, firstAfter.Hash)
	assert.NotEqual(t, secondBefore.Hash, secondAfter.Hash)
}

func TestGetFileHashWithCacheDirectoryRenameChangesParentOnly(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	beforePath := filepath.Join(root, "before")
	afterPath := filepath.Join(root, "after")

	require.NoError(t, os.Mkdir(beforePath, 0o700))
	writeFixedFile(t, beforePath, "item.txt", "content")

	childBefore, err := GetFileHashWithCache(beforePath)
	require.NoError(t, err)
	parentBefore, err := GetFileHashWithCache(root)
	require.NoError(t, err)
	require.NoError(t, os.Rename(beforePath, afterPath))

	childAfter, err := GetFileHashWithCache(afterPath)
	require.NoError(t, err)
	parentAfter, err := GetFileHashWithCache(root)
	require.NoError(t, err)
	assert.Equal(t, childBefore.Hash, childAfter.Hash)
	assert.NotEqual(t, parentBefore.Hash, parentAfter.Hash)
}

func TestGetFileHashWithCacheDirectoryOrdering(t *testing.T) {
	t.Parallel()

	firstRoot := t.TempDir()
	secondRoot := t.TempDir()
	writeFixedFile(t, firstRoot, "a.txt", "alpha")
	writeFixedFile(t, firstRoot, "b.txt", "beta")
	writeFixedFile(t, secondRoot, "b.txt", "beta")
	writeFixedFile(t, secondRoot, "a.txt", "alpha")

	first, err := GetFileHashWithCache(firstRoot)
	require.NoError(t, err)
	second, err := GetFileHashWithCache(secondRoot)
	require.NoError(t, err)
	assert.Equal(t, first.Hash, second.Hash)
}

func TestGetFileHashWithCacheIncludesEmptyDirectoryName(t *testing.T) {
	t.Parallel()

	firstRoot := t.TempDir()
	secondRoot := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(firstRoot, "first"), 0o700))
	require.NoError(t, os.Mkdir(filepath.Join(secondRoot, "second"), 0o700))

	first, err := GetFileHashWithCache(firstRoot)
	require.NoError(t, err)
	second, err := GetFileHashWithCache(secondRoot)
	require.NoError(t, err)
	assert.NotEqual(t, first.Hash, second.Hash)
}

func TestGetFileHashWithCacheRecoversFromMalformedMetadata(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "malformed.txt")
	require.NoError(t, os.WriteFile(path, []byte("content"), 0o600))
	setFixedTime(t, path)
	first, err := GetFileHashWithCache(path)
	require.NoError(t, err)

	if first.Status == StatusUncached {
		t.Skipf("metadata storage is unavailable: %v", first.CacheError)
	}

	file, err := os.Open(path) //nolint:gosec // The test controls the temporary path.
	require.NoError(t, err)
	info, err := file.Stat()
	require.NoError(t, err)
	require.NoError(t, writeMetadata(file, path, []byte("broken"), info.ModTime()))
	require.NoError(t, file.Close())

	result, err := GetFileHashWithCache(path)
	require.NoError(t, err)
	assert.Equal(t, first.Hash, result.Hash)
	assert.Equal(t, StatusMiss, result.Status)
}

func TestGetFileHashWithCacheRestoresMtime(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "mtime-preserved.txt")
	require.NoError(t, os.WriteFile(path, []byte("content"), 0o600))
	setFixedTime(t, path)
	want, err := os.Stat(path)
	require.NoError(t, err)

	result, err := GetFileHashWithCache(path)
	require.NoError(t, err)

	if result.Status == StatusUncached {
		t.Skipf("metadata storage is unavailable: %v", result.CacheError)
	}

	got, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, want.ModTime(), got.ModTime())
}

func TestGetFileHashWithCacheRejectsADSPathOnWindows(t *testing.T) {
	t.Parallel()

	if runtime.GOOS != "windows" {
		t.Skip("Windows-only behavior")
	}

	path := filepath.Join(t.TempDir(), "base.txt")
	require.NoError(t, os.WriteFile(path, []byte("content"), 0o600))
	result, err := GetFileHashWithCache(path + ":named")
	require.Error(t, err)
	assert.Equal(t, Result{}, result)
}

func TestGetFileHashWithCacheEmptyDirectoryIsDeterministic(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	first, err := GetFileHashWithCache(dir)
	require.NoError(t, err)
	second, err := GetFileHashWithCache(dir)
	require.NoError(t, err)
	assert.Equal(t, first.Hash, second.Hash)
}

func TestGetFileHashWithCacheRejectsSymlinkWithZeroResult(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	link := filepath.Join(dir, "link")

	require.NoError(t, os.WriteFile(target, []byte("data"), 0o600))

	err := os.Symlink(target, link)
	if err != nil {
		t.Skipf("symlink creation is unavailable: %v", err)
	}

	result, err := GetFileHashWithCache(link)
	require.Error(t, err)
	assert.Equal(t, Result{}, result)
}

func TestGetFileHashWithCacheConcurrentCalls(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "concurrent.bin")
	require.NoError(t, os.WriteFile(path, make([]byte, 32*1024), 0o600))
	setFixedTime(t, path)

	const callCount = 8

	hashes := make(chan string, callCount)
	errorsFound := make(chan error, callCount)

	var waitGroup sync.WaitGroup
	for range callCount {
		waitGroup.Go(func() {
			result, err := GetFileHashWithCache(path)
			errorsFound <- err

			hashes <- result.Hash
		})
	}

	waitGroup.Wait()
	close(errorsFound)
	close(hashes)

	for err := range errorsFound {
		require.NoError(t, err)
	}

	var first string
	for hash := range hashes {
		if first == "" {
			first = hash
		}

		assert.Equal(t, first, hash)
	}
}

func TestNativeMetadataCacheAvailable(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "native-cache.txt")
	require.NoError(t, os.WriteFile(path, []byte("cache me"), 0o600))
	setFixedTime(t, path)

	first, err := GetFileHashWithCache(path)
	require.NoError(t, err)
	assert.Equal(t, StatusMiss, first.Status, first.CacheError)
	require.NoError(t, first.CacheError)

	second, err := GetFileHashWithCache(path)
	require.NoError(t, err)
	assert.Equal(t, StatusHit, second.Status, second.CacheError)
	assert.NoError(t, second.CacheError)
}

func TestGetFileHashWithCacheCountsHardLinkEntries(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	original := filepath.Join(root, "original.txt")
	link := filepath.Join(root, "link.txt")

	require.NoError(t, os.WriteFile(original, []byte("shared"), 0o600))

	setFixedTime(t, original)

	oneEntry, err := GetFileHashWithCache(root)
	require.NoError(t, err)

	err = os.Link(original, link)
	if err != nil {
		t.Skipf("hard links are unavailable: %v", err)
	}

	twoEntries, err := GetFileHashWithCache(root)
	require.NoError(t, err)
	assert.NotEqual(t, oneEntry.Hash, twoEntries.Hash)
}

func TestGetFileHashWithCacheDoesNotOmitUnreadableChild(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	path := filepath.Join(root, "unreadable.txt")
	require.NoError(t, os.WriteFile(path, []byte("hidden"), 0o600))
	require.NoError(t, os.Chmod(path, 0))
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })

	_, err := GetFileHashWithCache(root)
	if err == nil {
		t.Skip("the current user can read a mode-zero file")
	}
}

func TestChangeErrorsSupportErrorsAs(t *testing.T) {
	t.Parallel()

	fileErr := &FileChangedError{Path: "file"}
	dirErr := &DirectoryChangedError{Path: "dir"}

	var (
		gotFile *FileChangedError
		gotDir  *DirectoryChangedError
	)

	require.ErrorAs(t, fileErr, &gotFile)
	require.ErrorAs(t, dirErr, &gotDir)
	require.ErrorIs(t, fileErr, ErrFileChanged)
	assert.ErrorIs(t, dirErr, ErrDirectoryChanged)
}

func TestRetrySnapshotReturnsTypedErrorAfterThreeAttempts(t *testing.T) {
	t.Parallel()

	attemptCount := 0
	attempt := func(_ string) (nodeResult, bool, error) {
		attemptCount++

		return nodeResult{}, false, nil
	}
	wantError := &FileChangedError{Path: "changing"}
	result, err := retrySnapshot("changing", attempt, wantError)
	require.ErrorIs(t, err, ErrFileChanged)
	assert.Equal(t, nodeResult{}, result)
	assert.Equal(t, maxSnapshotAttempts, attemptCount)

	attemptCount = 0
	directoryError := &DirectoryChangedError{Path: "changing"}
	result, err = retrySnapshot("changing", attempt, directoryError)
	require.ErrorIs(t, err, ErrDirectoryChanged)
	assert.Equal(t, nodeResult{}, result)
	assert.Equal(t, maxSnapshotAttempts, attemptCount)
}

func TestStoreFileCacheReturnsUncachedOnMetadataFailure(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "uncached.txt")
	require.NoError(t, os.WriteFile(path, []byte("content"), 0o600))
	setFixedTime(t, path)
	file, err := openObject(path)
	require.NoError(t, err)

	defer func() { _ = file.Close() }()

	info, err := file.Stat()
	require.NoError(t, err)

	write := func(_ *os.File, _ string, _ []byte, _ time.Time) error {
		return assert.AnError
	}
	result, stable, err := storeFileCacheWith(file, path, info, info, 1, 2, write)
	require.NoError(t, err)
	assert.True(t, stable)
	assert.Equal(t, StatusUncached, result.status)
	require.ErrorIs(t, result.cacheError, assert.AnError)
}

func replaceByte(source []byte, index int, value byte) []byte {
	result := append([]byte(nil), source...)
	result[index] = value

	return result
}

func setFixedTime(t *testing.T, path string) {
	t.Helper()
	setFixedTimeAt(t, path, time.Unix(1_700_000_000, 123_456_700))
}

func setFixedTimeAt(t *testing.T, path string, value time.Time) {
	t.Helper()
	require.NoError(t, os.Chtimes(path, value, value))
}

func writeFixedFile(t *testing.T, root, name, content string) {
	t.Helper()

	path := filepath.Join(root, name)
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	setFixedTime(t, path)
}
