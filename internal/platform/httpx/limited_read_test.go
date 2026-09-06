package httpx

import (
	"errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func TestReadAllLimited(t *testing.T) {
	for _, test := range []struct {
		name    string
		body    string
		limit   int64
		want    string
		wantErr error
	}{
		{name: "under", body: "abc", limit: 4, want: "abc"},
		{name: "exact", body: "abcd", limit: 4, want: "abcd"},
		{name: "over", body: "abcde", limit: 4, wantErr: ErrBodyTooLarge},
		{name: "invalid limit", body: "a", limit: 0, wantErr: errors.New("invalid")},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := ReadAllLimited(strings.NewReader(test.body), test.limit)
			if test.wantErr != nil {
				require.Error(t, err)
				if errors.Is(test.wantErr, ErrBodyTooLarge) {
					assert.ErrorIs(t, err, ErrBodyTooLarge)
				}
				assert.Nil(t, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, test.want, string(got))
		})
	}
}
