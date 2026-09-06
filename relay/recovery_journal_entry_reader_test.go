package relay

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func canonicalRecoveryJournalTestDirectory(t *testing.T) string {
	t.Helper()
	directory, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	return directory
}

func TestReadBoundedRecoveryJournalDirectoryReturnsDeterministicSnapshots(t *testing.T) {
	directory := canonicalRecoveryJournalTestDirectory(t)
	require.NoError(t, os.WriteFile(filepath.Join(directory, "z.json"), []byte("z-data"), 0o600))
	require.NoError(t, os.Mkdir(filepath.Join(directory, "middle"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "a.json"), []byte("a-data"), 0o600))

	wantNames := []string{"a.json", "middle", "z.json"}
	if runtime.GOOS != "windows" {
		outside := filepath.Join(filepath.Dir(directory), "outside-recovery-journal-entry")
		require.NoError(t, os.WriteFile(outside, []byte("outside"), 0o600))
		t.Cleanup(func() { _ = os.Remove(outside) })
		require.NoError(t, os.Symlink(outside, filepath.Join(directory, "link")))
		wantNames = []string{"a.json", "link", "middle", "z.json"}
	}

	entries, err := readBoundedRecoveryJournalDirectory(context.Background(), directory, len(wantNames))
	require.NoError(t, err)
	require.Len(t, entries, len(wantNames))
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	assert.Equal(t, wantNames, names)

	entryByName := make(map[string]os.DirEntry, len(entries))
	for _, entry := range entries {
		entryByName[entry.Name()] = entry
	}
	assert.True(t, entryByName["middle"].IsDir())
	assert.True(t, entryByName["a.json"].Type().IsRegular())
	if runtime.GOOS != "windows" {
		assert.NotZero(t, entryByName["link"].Type()&os.ModeSymlink)
		info, infoErr := entryByName["link"].Info()
		require.NoError(t, infoErr)
		assert.NotZero(t, info.Mode()&os.ModeSymlink)
	}

	beforeRemoval, err := entryByName["a.json"].Info()
	require.NoError(t, err)
	require.NoError(t, os.Remove(filepath.Join(directory, "a.json")))
	afterRemoval, err := entryByName["a.json"].Info()
	require.NoError(t, err)
	assert.Equal(t, beforeRemoval.Name(), afterRemoval.Name())
	assert.Equal(t, beforeRemoval.Size(), afterRemoval.Size())
	assert.Equal(t, beforeRemoval.Mode(), afterRemoval.Mode())
}

func TestReadBoundedRecoveryJournalDirectoryReportsOverflowWithoutPartialResults(t *testing.T) {
	directory := canonicalRecoveryJournalTestDirectory(t)
	entryCount := recoveryJournalDirectoryReadBatch + 2
	for index := 0; index < entryCount; index++ {
		require.NoError(t, os.WriteFile(
			filepath.Join(directory, fmt.Sprintf("%03d.json", index)), []byte("record"), 0o600,
		))
	}

	entries, err := readBoundedRecoveryJournalDirectory(context.Background(), directory, entryCount)
	require.NoError(t, err)
	assert.Len(t, entries, entryCount)

	entries, err = readBoundedRecoveryJournalDirectory(context.Background(), directory, entryCount-1)
	assert.ErrorIs(t, err, errRecoveryJournalDirectoryOverflow)
	assert.Nil(t, entries)
}

type cancelRecoveryJournalReadContext struct {
	context.Context
	checksRemaining int
}

func (ctx *cancelRecoveryJournalReadContext) Err() error {
	if ctx.checksRemaining == 0 {
		return context.Canceled
	}
	ctx.checksRemaining--
	return nil
}

func TestReadRecoveryJournalDirectoryEntryBatchCancelsBetweenBoundedReads(t *testing.T) {
	directory := canonicalRecoveryJournalTestDirectory(t)
	for index := 0; index <= recoveryJournalDirectoryReadBatch; index++ {
		require.NoError(t, os.WriteFile(
			filepath.Join(directory, fmt.Sprintf("%03d.json", index)), []byte("record"), 0o600,
		))
	}
	directoryFile, err := os.Open(directory)
	require.NoError(t, err)
	t.Cleanup(func() { _ = directoryFile.Close() })
	ctx := &cancelRecoveryJournalReadContext{Context: context.Background(), checksRemaining: 1}

	entries, err := readRecoveryJournalDirectoryEntryBatch(
		ctx, directoryFile, recoveryJournalDirectoryReadBatch+1,
	)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Nil(t, entries)
}

func TestReadBoundedRecoveryJournalDirectoryRejectsInvalidLimitsBeforeIO(t *testing.T) {
	missing := filepath.Join(canonicalRecoveryJournalTestDirectory(t), "missing")
	for _, limit := range []int{-1, 0, maximumRecoveryJournalDirectoryReadEntries + 1} {
		entries, err := readBoundedRecoveryJournalDirectory(context.Background(), missing, limit)
		assert.ErrorIs(t, err, errRecoveryJournalDirectoryLimit)
		assert.Nil(t, entries)
		assert.False(t, errors.Is(err, os.ErrNotExist))
	}
}

func TestReadBoundedRecoveryJournalDirectoryHonorsCancellationAndMissingDirectory(t *testing.T) {
	directory := canonicalRecoveryJournalTestDirectory(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	entries, err := readBoundedRecoveryJournalDirectory(ctx, directory, 1)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Nil(t, entries)

	entries, err = readBoundedRecoveryJournalDirectory(
		context.Background(), filepath.Join(directory, "missing"), 1,
	)
	assert.ErrorIs(t, err, os.ErrNotExist)
	assert.Nil(t, entries)
}

func TestReadBoundedRecoveryJournalDirectoryRejectsUnsafePaths(t *testing.T) {
	directory := canonicalRecoveryJournalTestDirectory(t)
	regularFile := filepath.Join(directory, "regular-file")
	require.NoError(t, os.WriteFile(regularFile, []byte("record"), 0o600))

	unsafe := []string{
		"",
		" " + directory,
		regularFile,
		strings.Repeat("x", maximumRecoveryJournalDirectoryPathBytes+1),
		string([]byte{0xff}),
		string(filepath.Separator),
	}
	for _, path := range unsafe {
		entries, err := readBoundedRecoveryJournalDirectory(context.Background(), path, 1)
		assert.ErrorIs(t, err, errRecoveryJournalDirectoryUnsafe, "path length: %d", len(path))
		assert.Nil(t, entries)
	}
}

func TestReadBoundedRecoveryJournalDirectoryRejectsSymlinkComponents(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires privileges on some Windows configurations")
	}
	root := canonicalRecoveryJournalTestDirectory(t)
	actualParent := filepath.Join(root, "actual-parent")
	actualLeaf := filepath.Join(actualParent, "journal")
	require.NoError(t, os.MkdirAll(actualLeaf, 0o700))

	leafLink := filepath.Join(root, "leaf-link")
	require.NoError(t, os.Symlink(actualLeaf, leafLink))
	entries, err := readBoundedRecoveryJournalDirectory(context.Background(), leafLink, 1)
	assert.ErrorIs(t, err, errRecoveryJournalDirectoryUnsafe)
	assert.Nil(t, entries)

	parentLink := filepath.Join(root, "parent-link")
	require.NoError(t, os.Symlink(actualParent, parentLink))
	entries, err = readBoundedRecoveryJournalDirectory(
		context.Background(), filepath.Join(parentLink, "journal"), 1,
	)
	assert.ErrorIs(t, err, errRecoveryJournalDirectoryUnsafe)
	assert.Nil(t, entries)
}

func TestVerifyRecoveryJournalDirectoryIdentityRejectsPathReplacement(t *testing.T) {
	directory := canonicalRecoveryJournalTestDirectory(t)
	openedRoot, openedInfo, absoluteDirectory, err := openRecoveryJournalDirectoryRoot(
		context.Background(), directory,
	)
	require.NoError(t, err)
	require.NoError(t, openedRoot.Close())

	moved := directory + "-moved"
	require.NoError(t, os.Rename(directory, moved))
	t.Cleanup(func() { _ = os.RemoveAll(moved) })
	require.NoError(t, os.Mkdir(directory, 0o700))

	err = verifyRecoveryJournalDirectoryIdentity(context.Background(), absoluteDirectory, openedInfo)
	assert.ErrorIs(t, err, errRecoveryJournalDirectoryChanged)
}
