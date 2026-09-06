package tasks

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// validateRecoveryJournalDirectory prevents a recovery journal from treating
// the process working directory, one of its ancestors, or a filesystem root as
// a private data directory. Journal writers chmod their directory and some
// cleanup jobs remove matching files, so accepting those broad targets would
// put unrelated application data at risk after a configuration mistake.
func validateRecoveryJournalDirectory(directory, family string) (string, error) {
	directory = filepath.Clean(strings.TrimSpace(directory))
	unsafeDirectory := fmt.Errorf("unsafe %s recovery journal directory", family)
	if directory == "." || filepath.Base(directory) == ".." || filepath.Dir(directory) == directory {
		return "", unsafeDirectory
	}
	absoluteDirectory, err := filepath.Abs(directory)
	if err != nil {
		return "", unsafeDirectory
	}
	var existingInfo os.FileInfo
	if info, lstatErr := os.Lstat(absoluteDirectory); lstatErr == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", unsafeDirectory
		}
		existingInfo = info
	} else if !errors.Is(lstatErr, os.ErrNotExist) {
		return "", fmt.Errorf("inspect %s recovery journal directory", family)
	}
	absoluteDirectory, err = resolveRecoveryJournalDirectoryPath(absoluteDirectory)
	if err != nil {
		return "", fmt.Errorf("inspect %s recovery journal directory", family)
	}
	workingDirectory, err := os.Getwd()
	if err != nil {
		return "", errors.New("inspect recovery journal working directory")
	}
	if resolvedWorkingDirectory, resolveErr := filepath.EvalSymlinks(workingDirectory); resolveErr == nil {
		workingDirectory = resolvedWorkingDirectory
	}
	for _, protectedDirectory := range []string{os.TempDir(), userHomeDirectory()} {
		if protectedDirectory == "" {
			continue
		}
		protectedDirectory, resolveErr := filepath.EvalSymlinks(protectedDirectory)
		if resolveErr == nil && absoluteDirectory == filepath.Clean(protectedDirectory) {
			return "", unsafeDirectory
		}
	}
	for candidate := filepath.Clean(workingDirectory); ; candidate = filepath.Dir(candidate) {
		if absoluteDirectory == candidate {
			return "", unsafeDirectory
		}
		parent := filepath.Dir(candidate)
		if parent == candidate {
			break
		}
	}
	if existingInfo != nil && existingInfo.Mode().Perm() != 0o700 &&
		!emptyRecoveryJournalDirectory(absoluteDirectory, existingInfo) {
		return "", unsafeDirectory
	}
	return absoluteDirectory, nil
}

func userHomeDirectory() string {
	directory, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return directory
}

func emptyRecoveryJournalDirectory(directory string, expected os.FileInfo) bool {
	file, err := os.Open(directory)
	if err != nil {
		return false
	}
	openedInfo, statErr := file.Stat()
	names, readErr := file.Readdirnames(1)
	closeErr := file.Close()
	return statErr == nil && os.SameFile(expected, openedInfo) && len(names) == 0 &&
		errors.Is(readErr, io.EOF) && closeErr == nil
}

// resolveRecoveryJournalDirectoryPath resolves symlinks in the deepest
// existing ancestor and appends any not-yet-created leaf components. Returning
// an absolute resolved path means later operations cannot be redirected merely
// by changing the process working directory.
func resolveRecoveryJournalDirectoryPath(absoluteDirectory string) (string, error) {
	candidate := filepath.Clean(absoluteDirectory)
	missing := make([]string, 0, 2)
	for {
		info, err := os.Lstat(candidate)
		if err == nil {
			if len(missing) > 0 && !info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
				return "", errors.New("recovery journal parent is not a directory")
			}
			resolved, err := filepath.EvalSymlinks(candidate)
			if err != nil {
				return "", err
			}
			if len(missing) > 0 {
				resolvedInfo, err := os.Stat(resolved)
				if err != nil || !resolvedInfo.IsDir() {
					return "", errors.Join(errors.New("recovery journal parent is not a directory"), err)
				}
			}
			for index := len(missing) - 1; index >= 0; index-- {
				resolved = filepath.Join(resolved, missing[index])
			}
			return filepath.Clean(resolved), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(candidate)
		if parent == candidate {
			return "", err
		}
		missing = append(missing, filepath.Base(candidate))
		candidate = parent
	}
}
