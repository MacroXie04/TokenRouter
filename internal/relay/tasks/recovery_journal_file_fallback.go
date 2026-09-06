//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd

package tasks

import (
	"errors"
	"os"
)

func openRecoveryJournalFile(path string) (*os.File, error) {
	pathInfo, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !pathInfo.Mode().IsRegular() {
		return nil, errors.New("recovery journal is not a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	openedInfo, statErr := file.Stat()
	if statErr == nil && openedInfo.Mode().IsRegular() && os.SameFile(pathInfo, openedInfo) {
		return file, nil
	}
	closeErr := file.Close()
	return nil, errors.Join(errors.New("recovery journal changed while opening"), statErr, closeErr)
}
