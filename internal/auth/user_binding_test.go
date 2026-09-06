package auth

import (
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"path/filepath"
	"testing"
)

func TestClearUserIdentityBindingRollsBackOwnershipRelease(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+filepath.Join(t.TempDir(), "binding-rollback.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.ExternalIdentityClaim{}))
	previousDB := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = previousDB })

	user := model.User{
		Username: "binding-rollback", Password: "password8", Status: model.UserStatusEnabled,
		Role: 1, AuthVersion: 1, GitHubId: "durable-subject",
	}
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&user).Error; err != nil {
			return err
		}
		return model.ClaimUserExternalIdentitiesWithTx(tx, &user)
	}))
	require.NoError(t, db.Exec(`
		CREATE TRIGGER fail_binding_clear
		BEFORE UPDATE OF github_id ON users
		BEGIN SELECT RAISE(ABORT, 'injected binding update failure'); END
	`).Error)

	err = ClearUserIdentityBinding(user.Id, model.ExternalIdentityProviderGitHub)
	require.Error(t, err)

	var refreshed model.User
	require.NoError(t, db.First(&refreshed, user.Id).Error)
	assert.Equal(t, "durable-subject", refreshed.GitHubId)
	var claimCount int64
	require.NoError(t, db.Model(&model.ExternalIdentityClaim{}).
		Where("provider = ? AND user_id = ?", model.ExternalIdentityProviderGitHub, user.Id).
		Count(&claimCount).Error)
	assert.EqualValues(t, 1, claimCount)
}
