package importer

import (
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	"github.com/tokenrouter/tokenrouter/internal/platform/cryptoutil"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"path/filepath"
	"strings"
	"testing"
)

const legacyImportCost10PasswordHash = "$2y$10$WGIVWvPB3dHTKpgaZwRTAuLd.wsNOOvf1vW2ZwYuho14bIlqDCElW"

func TestImportLegacyOneAPI(t *testing.T) {
	// Build a legacy one-api SQLite DB.
	legacyPath := filepath.Join(t.TempDir(), "legacy.db")
	legacyDB, err := gorm.Open(sqlite.Open(legacyPath), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, legacyDB.AutoMigrate(&model.User{}, &model.Token{}, &model.Channel{}, &model.Option{}))
	require.NoError(t, legacyDB.Create(&model.User{
		Username: "legacy-user", Password: legacyImportCost10PasswordHash, Role: 1, Status: 1, Quota: 100, Group: "default",
		GitHubId: "legacy-github-subject",
	}).Error)
	require.NoError(t, legacyDB.Create(&model.Token{UserId: 1, Key: "sk-legacy", Name: "legacy", Status: billingsvc.TokenStatusEnabled}).Error)
	require.NoError(t, legacyDB.Create(&model.Channel{Name: "legacy-chan", Type: 1, Key: "k", Status: 1}).Error)
	require.NoError(t, legacyDB.Create(&model.Option{Key: "SystemName", Value: "Legacy"}).Error)

	// TokenRouter target DB.
	targetPath := filepath.Join(t.TempDir(), "target.db")
	targetDB, err := gorm.Open(sqlite.Open(targetPath), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, targetDB.AutoMigrate(&model.User{}, &model.Token{}, &model.Channel{}, &model.Option{}, &model.ExternalIdentityClaim{}))
	previousDB, previousLogDB := model.DB, model.LOG_DB
	model.DB = targetDB
	model.LOG_DB = targetDB
	t.Cleanup(func() { model.DB, model.LOG_DB = previousDB, previousLogDB })

	counts, err := ImportLegacyOneAPI(legacyPath)
	require.NoError(t, err)
	assert.Equal(t, 1, counts["users"])
	assert.Equal(t, 1, counts["tokens"])
	assert.Equal(t, 1, counts["channels"])
	assert.Equal(t, 1, counts["options"])

	var user model.User
	require.NoError(t, model.DB.Where("username = ?", "legacy-user").First(&user).Error)
	assert.Equal(t, 100, user.Quota)
	claim, err := model.FindExternalIdentityClaimWithTx(model.DB, "github", "legacy-github-subject")
	require.NoError(t, err)
	assert.Equal(t, user.Id, claim.UserId)

	// Idempotent: a second import skips existing rows.
	counts2, err := ImportLegacyOneAPI(legacyPath)
	require.NoError(t, err)
	assert.Equal(t, 0, counts2["users"])
	assert.Equal(t, 0, counts2["tokens"])
	assert.Equal(t, 0, counts2["channels"])
	assert.Equal(t, 0, counts2["options"])
}

func TestImportLegacyOneAPIRemapsOwnershipAndInviters(t *testing.T) {
	legacyPath := filepath.Join(t.TempDir(), "legacy-remap.db")
	legacyDB, err := gorm.Open(sqlite.Open(legacyPath), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, legacyDB.AutoMigrate(&model.User{}, &model.Token{}, &model.Channel{}, &model.Option{}))
	inviter := model.User{Username: "legacy-inviter", Password: legacyImportCost10PasswordHash, Status: 1, Group: "default", AffCode: "legacy-inviter-code"}
	require.NoError(t, legacyDB.Create(&inviter).Error)
	invitee := model.User{Username: "legacy-invitee", Password: legacyImportCost10PasswordHash, Status: 1, Group: "default", AffCode: "legacy-invitee-code", InviterId: inviter.Id}
	require.NoError(t, legacyDB.Create(&invitee).Error)
	require.NoError(t, legacyDB.Create(&model.Token{UserId: invitee.Id, Key: "sk-remapped-owner", Name: "remapped", Status: billingsvc.TokenStatusEnabled}).Error)

	targetPath := filepath.Join(t.TempDir(), "target-remap.db")
	targetDB, err := gorm.Open(sqlite.Open(targetPath), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, targetDB.AutoMigrate(&model.User{}, &model.Token{}, &model.Channel{}, &model.Option{}, &model.ExternalIdentityClaim{}))
	// Occupy the source primary-key range. A safe merge must allocate target
	// IDs and rewrite every imported relationship instead of attaching the
	// token to this unrelated local account.
	local := model.User{Username: "local-owner", Password: legacyImportCost10PasswordHash, Status: 1, Group: "default", AffCode: "local-owner-code"}
	require.NoError(t, targetDB.Create(&local).Error)
	require.Equal(t, inviter.Id, local.Id)

	previousDB, previousLogDB := model.DB, model.LOG_DB
	model.DB, model.LOG_DB = targetDB, targetDB
	t.Cleanup(func() { model.DB, model.LOG_DB = previousDB, previousLogDB })

	counts, err := ImportLegacyOneAPI(legacyPath)
	require.NoError(t, err)
	assert.Equal(t, 2, counts["users"])
	assert.Equal(t, 1, counts["tokens"])

	var importedInviter, importedInvitee model.User
	require.NoError(t, targetDB.Where("username = ?", inviter.Username).First(&importedInviter).Error)
	require.NoError(t, targetDB.Where("username = ?", invitee.Username).First(&importedInvitee).Error)
	assert.NotEqual(t, inviter.Id, importedInviter.Id)
	assert.Equal(t, importedInviter.Id, importedInvitee.InviterId)
	var importedToken model.Token
	require.NoError(t, targetDB.Where("key = ?", "sk-remapped-owner").First(&importedToken).Error)
	assert.Equal(t, importedInvitee.Id, importedToken.UserId)
	assert.NotEqual(t, local.Id, importedToken.UserId)
}

func TestImportLegacyOneAPIRollsBackEveryEntityOnTargetWriteFailure(t *testing.T) {
	legacyPath := filepath.Join(t.TempDir(), "legacy-rollback.db")
	legacyDB, err := gorm.Open(sqlite.Open(legacyPath), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, legacyDB.AutoMigrate(&model.User{}, &model.Token{}, &model.Channel{}, &model.Option{}))
	legacyUser := model.User{Username: "rollback-user", Password: legacyImportCost10PasswordHash, Status: 1, Group: "default", AffCode: "rollback-user-code"}
	require.NoError(t, legacyDB.Create(&legacyUser).Error)
	require.NoError(t, legacyDB.Create(&model.Token{UserId: legacyUser.Id, Key: "sk-rollback", Name: "rollback", Status: billingsvc.TokenStatusEnabled}).Error)
	require.NoError(t, legacyDB.Create(&model.Channel{Name: "fail-import-channel", Type: 1, Key: "secret", Status: 1}).Error)
	require.NoError(t, legacyDB.Create(&model.Option{Key: "RollbackOption", Value: "value"}).Error)

	targetPath := filepath.Join(t.TempDir(), "target-rollback.db")
	targetDB, err := gorm.Open(sqlite.Open(targetPath), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, targetDB.AutoMigrate(&model.User{}, &model.Token{}, &model.Channel{}, &model.Option{}, &model.ExternalIdentityClaim{}))
	require.NoError(t, targetDB.Exec(`CREATE TRIGGER fail_import_channel
		BEFORE INSERT ON channels WHEN NEW.name = 'fail-import-channel'
		BEGIN SELECT RAISE(ABORT, 'injected import failure'); END`).Error)

	previousDB, previousLogDB := model.DB, model.LOG_DB
	model.DB, model.LOG_DB = targetDB, targetDB
	t.Cleanup(func() { model.DB, model.LOG_DB = previousDB, previousLogDB })

	counts, err := ImportLegacyOneAPI(legacyPath)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "injected import failure")
	assert.Equal(t, map[string]int{"users": 0, "tokens": 0, "channels": 0, "options": 0}, counts)

	for table, predicate := range map[string]string{
		"users": "username = 'rollback-user'", "tokens": "key = 'sk-rollback'",
		"channels": "name = 'fail-import-channel'", "options": "key = 'RollbackOption'",
	} {
		var count int64
		require.NoError(t, targetDB.Table(table).Where(predicate).Count(&count).Error)
		assert.Zero(t, count, "%s import must roll back", table)
	}
}

func TestImportLegacyOneAPIPreflightsPasswordHashesBeforeTargetMutation(t *testing.T) {
	legacyPath := filepath.Join(t.TempDir(), "legacy-password-cost.db")
	legacyDB, err := gorm.Open(sqlite.Open(legacyPath), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, legacyDB.AutoMigrate(&model.User{}, &model.Token{}, &model.Channel{}, &model.Option{}))
	valid := model.User{
		Username: "legacy-valid-password", Password: legacyImportCost10PasswordHash,
		Status: 1, Group: "default", AffCode: "legacy-valid-password-code",
	}
	require.NoError(t, legacyDB.Create(&valid).Error)
	highCostHash := strings.Replace(legacyImportCost10PasswordHash, "$10$", "$13$", 1)
	require.NoError(t, legacyDB.Create(&model.User{
		Username: "legacy-high-cost-password", Password: highCostHash,
		Status: 1, Group: "default", AffCode: "legacy-high-cost-password-code",
	}).Error)
	require.NoError(t, legacyDB.Create(&model.Token{
		UserId: valid.Id, Key: "sk-password-preflight", Name: "password-preflight", Status: billingsvc.TokenStatusEnabled,
	}).Error)
	require.NoError(t, legacyDB.Create(&model.Channel{
		Name: "password-preflight-channel", Type: 1, Key: "secret", Status: 1,
	}).Error)
	require.NoError(t, legacyDB.Create(&model.Option{Key: "PasswordPreflightOption", Value: "value"}).Error)

	targetPath := filepath.Join(t.TempDir(), "target-password-cost.db")
	targetDB, err := gorm.Open(sqlite.Open(targetPath), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, targetDB.AutoMigrate(
		&model.User{}, &model.Token{}, &model.Channel{}, &model.Option{}, &model.ExternalIdentityClaim{},
	))
	previousDB, previousLogDB := model.DB, model.LOG_DB
	model.DB, model.LOG_DB = targetDB, targetDB
	t.Cleanup(func() { model.DB, model.LOG_DB = previousDB, previousLogDB })

	counts, err := ImportLegacyOneAPI(legacyPath)
	require.ErrorIs(t, err, cryptoutil.ErrPasswordHashCostUnsupported)
	assert.Contains(t, err.Error(), "legacy-high-cost-password")
	assert.NotContains(t, err.Error(), highCostHash)
	assert.Equal(t, map[string]int{"users": 0, "tokens": 0, "channels": 0, "options": 0}, counts)

	for _, table := range []string{"users", "tokens", "channels", "options"} {
		var count int64
		require.NoError(t, targetDB.Table(table).Count(&count).Error)
		assert.Zero(t, count, table)
	}
}

func TestLegacyPasswordHashPreflightAllowsOAuthOnlyAndBoundedBcrypt(t *testing.T) {
	require.NoError(t, validateLegacyPasswordHashes([]model.User{
		{Username: "oauth-only", Password: ""},
		{Username: "legacy-cost-10", Password: legacyImportCost10PasswordHash},
	}))

	malformedCredential := "not-a-valid-secret-value"
	err := validateLegacyPasswordHashes([]model.User{{Username: "malformed-password", Password: malformedCredential}})
	require.ErrorIs(t, err, cryptoutil.ErrPasswordHashInvalid)
	assert.Contains(t, err.Error(), "malformed-password")
	assert.NotContains(t, err.Error(), malformedCredential)
}
