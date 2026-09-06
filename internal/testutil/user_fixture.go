package testutil

import (
	"fmt"
	"github.com/stretchr/testify/require"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"testing"
)

func NewUser(t *testing.T, quota int) *model.User {
	t.Helper()
	u := &model.User{Username: fmt.Sprintf("u-%d", quota), Password: "x", Role: 1, Status: 1, Quota: quota}
	require.NoError(t, model.DB.Create(u).Error)
	return u
}
