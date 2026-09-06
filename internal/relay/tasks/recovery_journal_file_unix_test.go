//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package tasks

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestOpenRecoveryJournalFileRejectsSpecialFilesWithoutBlocking(t *testing.T) {
	directory := t.TempDir()
	regular := filepath.Join(directory, "regular.json")
	require.NoError(t, os.WriteFile(regular, []byte("{}"), 0o600))
	file, err := openRecoveryJournalFile(regular)
	require.NoError(t, err)
	require.NoError(t, file.Close())

	symlink := filepath.Join(directory, "symlink.json")
	require.NoError(t, os.Symlink(regular, symlink))
	_, err = openRecoveryJournalFile(symlink)
	assert.Error(t, err)

	fifo := filepath.Join(directory, "journal.fifo")
	require.NoError(t, unix.Mkfifo(fifo, 0o600))
	result := make(chan error, 1)
	go func() {
		file, err := openRecoveryJournalFile(fifo)
		if file != nil {
			_ = file.Close()
		}
		result <- err
	}()
	select {
	case err := <-result:
		assert.Error(t, err)
	case <-time.After(time.Second):
		t.Fatal("opening a recovery FIFO blocked")
	}
}
