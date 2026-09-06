package tasks

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	// The ordinary journal promoters process at most 1,000 records per pass.
	// Keeping the scan ceiling independently bounded allows some non-journal
	// entries without permitting an accidental caller-controlled allocation.
	maximumRecoveryJournalDirectoryReadEntries = 65_536
	maximumRecoveryJournalDirectoryPathBytes   = 4_096
	recoveryJournalDirectoryReadBatch          = 128
)

var (
	errRecoveryJournalDirectoryOverflow = errors.New("recovery journal directory entry limit exceeded")
	errRecoveryJournalDirectoryChanged  = errors.New("recovery journal directory changed during read")
	errRecoveryJournalDirectoryUnsafe   = errors.New("unsafe recovery journal directory")
	errRecoveryJournalDirectoryLimit    = errors.New("invalid recovery journal directory entry limit")
)

// recoveryJournalDirectoryEntry is an immutable snapshot. In particular,
// Info does not perform a path lookup after the anchored directory is closed.
type recoveryJournalDirectoryEntry struct {
	name string
	info os.FileInfo
}

var _ os.DirEntry = recoveryJournalDirectoryEntry{}

func (entry recoveryJournalDirectoryEntry) Name() string               { return entry.name }
func (entry recoveryJournalDirectoryEntry) IsDir() bool                { return entry.info.IsDir() }
func (entry recoveryJournalDirectoryEntry) Type() os.FileMode          { return entry.info.Mode().Type() }
func (entry recoveryJournalDirectoryEntry) Info() (os.FileInfo, error) { return entry.info, nil }

// readBoundedRecoveryJournalDirectory is a bounded, deterministic replacement
// for os.ReadDir for recovery journals. It reads at most maximumEntries+1
// directory records, returns an explicit overflow error instead of a partial
// result, and snapshots metadata through an os.Root anchored to the opened
// directory. Returned entries are sorted by filename, matching os.ReadDir.
func readBoundedRecoveryJournalDirectory(
	ctx context.Context,
	directory string,
	maximumEntries int,
) (entries []os.DirEntry, returnErr error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if maximumEntries <= 0 || maximumEntries > maximumRecoveryJournalDirectoryReadEntries {
		return nil, errRecoveryJournalDirectoryLimit
	}

	root, openedInfo, absoluteDirectory, err := openRecoveryJournalDirectoryRoot(ctx, directory)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil {
			entries = nil
			returnErr = errors.Join(returnErr, fmt.Errorf("close recovery journal directory root: %w", closeErr))
		}
	}()

	directoryFile, err := root.Open(".")
	if err != nil {
		return nil, fmt.Errorf("open recovery journal directory: %w", err)
	}
	fileInfo, err := directoryFile.Stat()
	if err != nil {
		return nil, errors.Join(
			fmt.Errorf("inspect opened recovery journal directory: %w", err),
			directoryFile.Close(),
		)
	}
	if !os.SameFile(openedInfo, fileInfo) {
		return nil, errors.Join(errRecoveryJournalDirectoryChanged, directoryFile.Close())
	}

	rawEntries, readErr := readRecoveryJournalDirectoryEntryBatch(ctx, directoryFile, maximumEntries)
	afterReadInfo, statErr := directoryFile.Stat()
	closeErr := directoryFile.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return nil, errors.Join(fmt.Errorf("read recovery journal directory: %w", readErr), statErr, closeErr)
	}
	if statErr != nil || closeErr != nil {
		return nil, errors.Join(statErr, closeErr)
	}
	if !os.SameFile(openedInfo, afterReadInfo) {
		return nil, errRecoveryJournalDirectoryChanged
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(rawEntries) > maximumEntries {
		return nil, errRecoveryJournalDirectoryOverflow
	}

	entries = make([]os.DirEntry, 0, len(rawEntries))
	for _, rawEntry := range rawEntries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		name := rawEntry.Name()
		info, infoErr := root.Lstat(name)
		if infoErr != nil {
			return nil, errors.Join(errRecoveryJournalDirectoryChanged,
				fmt.Errorf("inspect recovery journal directory entry: %w", infoErr))
		}
		entries = append(entries, recoveryJournalDirectoryEntry{name: name, info: info})
	}
	sort.Slice(entries, func(left, right int) bool {
		return entries[left].Name() < entries[right].Name()
	})

	if err := verifyRecoveryJournalDirectoryIdentity(ctx, absoluteDirectory, openedInfo); err != nil {
		return nil, err
	}
	return entries, nil
}

func readRecoveryJournalDirectoryEntryBatch(
	ctx context.Context,
	directory *os.File,
	maximumEntries int,
) ([]os.DirEntry, error) {
	entries := make([]os.DirEntry, 0, min(maximumEntries, recoveryJournalDirectoryReadBatch))
	for len(entries) <= maximumEntries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		remaining := maximumEntries + 1 - len(entries)
		batchSize := min(remaining, recoveryJournalDirectoryReadBatch)
		batch, err := directory.ReadDir(batchSize)
		entries = append(entries, batch...)
		if errors.Is(err, io.EOF) {
			return entries, nil
		}
		if err != nil {
			return nil, err
		}
		if len(entries) > maximumEntries {
			return entries, nil
		}
	}
	return entries, nil
}

func verifyRecoveryJournalDirectoryIdentity(
	ctx context.Context,
	directory string,
	expected os.FileInfo,
) error {
	verificationRoot, verificationInfo, _, err := openRecoveryJournalDirectoryRoot(ctx, directory)
	if err != nil {
		return errors.Join(errRecoveryJournalDirectoryChanged, err)
	}
	if closeErr := verificationRoot.Close(); closeErr != nil {
		return fmt.Errorf("close recovery journal verification root: %w", closeErr)
	}
	if expected == nil || !os.SameFile(expected, verificationInfo) {
		return errRecoveryJournalDirectoryChanged
	}
	return nil
}

// openRecoveryJournalDirectoryRoot opens every absolute path component one at
// a time. Lstat rejects symlinks, while the identity comparison catches a
// component being swapped between Lstat and OpenRoot. os.Root then anchors all
// entry metadata reads to the opened directory on supported platforms.
func openRecoveryJournalDirectoryRoot(
	ctx context.Context,
	directory string,
) (*os.Root, os.FileInfo, string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, "", err
	}
	if directory == "" || len(directory) > maximumRecoveryJournalDirectoryPathBytes ||
		strings.TrimSpace(directory) != directory || !utf8.ValidString(directory) {
		return nil, nil, "", errRecoveryJournalDirectoryUnsafe
	}
	absoluteDirectory, err := filepath.Abs(filepath.Clean(directory))
	if err != nil || len(absoluteDirectory) > maximumRecoveryJournalDirectoryPathBytes {
		return nil, nil, "", errors.Join(errRecoveryJournalDirectoryUnsafe, err)
	}
	volumeRoot := filepath.VolumeName(absoluteDirectory) + string(filepath.Separator)
	relative, err := filepath.Rel(volumeRoot, absoluteDirectory)
	if err != nil || relative == "." || relative == "" || filepath.IsAbs(relative) ||
		relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil, nil, "", errors.Join(errRecoveryJournalDirectoryUnsafe, err)
	}
	components := strings.Split(relative, string(filepath.Separator))

	current, err := os.OpenRoot(volumeRoot)
	if err != nil {
		return nil, nil, "", fmt.Errorf("open recovery journal filesystem root: %w", err)
	}
	var finalInfo os.FileInfo
	for _, component := range components {
		if err := ctx.Err(); err != nil {
			_ = current.Close()
			return nil, nil, "", err
		}
		if component == "" || component == "." || component == ".." {
			_ = current.Close()
			return nil, nil, "", errRecoveryJournalDirectoryUnsafe
		}
		before, lstatErr := current.Lstat(component)
		if lstatErr != nil {
			_ = current.Close()
			return nil, nil, "", lstatErr
		}
		if !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
			_ = current.Close()
			return nil, nil, "", errRecoveryJournalDirectoryUnsafe
		}
		next, openErr := current.OpenRoot(component)
		if openErr != nil {
			_ = current.Close()
			return nil, nil, "", openErr
		}
		after, statErr := next.Stat(".")
		if statErr != nil || !os.SameFile(before, after) {
			_ = next.Close()
			_ = current.Close()
			return nil, nil, "", errors.Join(errRecoveryJournalDirectoryChanged, statErr)
		}
		if closeErr := current.Close(); closeErr != nil {
			_ = next.Close()
			return nil, nil, "", fmt.Errorf("close recovery journal path root: %w", closeErr)
		}
		current = next
		finalInfo = after
	}
	return current, finalInfo, absoluteDirectory, nil
}
