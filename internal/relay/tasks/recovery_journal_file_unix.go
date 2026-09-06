//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package tasks

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func openRecoveryJournalFile(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), filepath.Base(path))
	if file == nil {
		_ = unix.Close(fd)
		return nil, errors.New("open recovery journal file")
	}
	info, statErr := file.Stat()
	if statErr == nil && info.Mode().IsRegular() {
		return file, nil
	}
	closeErr := file.Close()
	return nil, errors.Join(errors.New("recovery journal is not a regular file"), statErr, closeErr)
}
