package testutil

import (
	"errors"
)

type EntropyFailureReader struct{}

func (EntropyFailureReader) Read([]byte) (int, error) {
	return 0, errors.New("injected entropy failure")
}
