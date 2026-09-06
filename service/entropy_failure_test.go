package service

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/model"
)

type entropyFailureReader struct{}

func (entropyFailureReader) Read([]byte) (int, error) {
	return 0, errors.New("injected entropy failure")
}

func TestSecurityIssuanceEntropyFailureLeavesNoMutation(t *testing.T) {
	db := initAuthFlowTestDB(t,
		&model.User{}, &model.UserSession{}, &model.AuthFlow{}, &model.Redemption{},
		&model.TwoFA{}, &model.TwoFABackupCode{}, &model.TopUp{}, &model.Log{}, &model.AuditLogOutbox{},
	)
	existingPAT := "existing-pat"
	user := model.User{
		Username: "entropy-user", Password: "hash", Role: constant.RoleCommonUser,
		Status: model.UserStatusEnabled, Group: GroupDefault, AuthVersion: 1, Quota: 1000,
		Email: "entropy@example.com", EmailVerified: true, AccessToken: &existingPAT,
	}
	require.NoError(t, db.Create(&user).Error)
	sid, _, err := CreateSession(&user, "127.0.0.1", "test", "password")
	require.NoError(t, err)
	var originalSession model.UserSession
	require.NoError(t, db.Where("sid = ?", sid).First(&originalSession).Error)
	require.NoError(t, db.Create(&model.TwoFA{
		UserId: user.Id, Secret: "secret", IsEnabled: true,
	}).Error)
	originalBackup := model.TwoFABackupCode{
		UserId: user.Id, CodeHash: common.SHA256Hex("existing-backup"), CreatedAt: time.Now(),
	}
	require.NoError(t, db.Create(&originalBackup).Error)

	previousMailer := Mail
	mailer := &mockMailer{}
	Mail = mailer
	t.Cleanup(func() { Mail = previousMailer })
	restoreEntropy := common.SetSecureRandomReaderForTesting(entropyFailureReader{})
	t.Cleanup(restoreEntropy)

	newSID, newRefresh, err := CreateSession(&user, "127.0.0.2", "test", "password")
	assert.ErrorIs(t, err, common.ErrSecureRandomUnavailable)
	assert.Empty(t, newSID)
	assert.Empty(t, newRefresh)

	flowToken, err := CreateAuthFlow(AuthFlowPurposeOAuth, "github", "login", user.Id, sid, "", time.Minute)
	assert.ErrorIs(t, err, common.ErrSecureRandomUnavailable)
	assert.Empty(t, flowToken)

	assert.ErrorIs(t, SendPasswordResetEmail(user.Email), common.ErrSecureRandomUnavailable)
	assert.ErrorIs(t, SendEmailVerificationCode("new@example.com"), common.ErrSecureRandomUnavailable)
	assert.Empty(t, mailer.sent)

	proof, expiresAt, err := IssueSecurityProof(SessionIdentity{
		UserID: user.Id, SessionID: sid, UserAuthVersion: 1, SessionVersion: 1,
	}, SecurityProofMethod2FA, []string{SecurityProofScopeBackupCodeReset})
	assert.ErrorIs(t, err, common.ErrSecureRandomUnavailable)
	assert.Empty(t, proof)
	assert.Zero(t, expiresAt)

	pat, err := GenerateUserAccessToken(user.Id)
	assert.ErrorIs(t, err, common.ErrSecureRandomUnavailable)
	assert.Empty(t, pat)

	redemptionKeys, err := CreateRedemptionBatch(user.Id, "entropy", 100, 0, 2)
	assert.ErrorIs(t, err, common.ErrSecureRandomUnavailable)
	assert.Empty(t, redemptionKeys)

	backupCodes, err := RegenerateBackupCodes(user.Id)
	assert.ErrorIs(t, err, common.ErrSecureRandomUnavailable)
	assert.Empty(t, backupCodes)

	topUp, err := CreateTopUp(user.Id, 100, 1, "stripe", PaymentProviderStripe)
	assert.ErrorIs(t, err, common.ErrSecureRandomUnavailable)
	assert.Nil(t, topUp)
	assert.ErrorIs(t, PurchaseSubscriptionWithBalance(user.Id, 1), common.ErrSecureRandomUnavailable)
	assert.ErrorIs(t, persistAuditLogWithOutbox(&model.Log{UserId: user.Id, Content: "entropy audit"}), common.ErrSecureRandomUnavailable)

	username, err := uniqueUsername(user.Username)
	assert.ErrorIs(t, err, common.ErrSecureRandomUnavailable)
	assert.Empty(t, username)

	var sessionCount int64
	require.NoError(t, db.Model(&model.UserSession{}).Where("user_id = ?", user.Id).Count(&sessionCount).Error)
	assert.EqualValues(t, 1, sessionCount)
	var storedSession model.UserSession
	require.NoError(t, db.Where("sid = ?", sid).First(&storedSession).Error)
	assert.Equal(t, originalSession.Version, storedSession.Version)
	assert.Equal(t, originalSession.RefreshHash, storedSession.RefreshHash)
	assert.Empty(t, storedSession.PreviousRefreshHash)

	var flowCount, redemptionCount, topUpCount int64
	require.NoError(t, db.Model(&model.AuthFlow{}).Count(&flowCount).Error)
	require.NoError(t, db.Model(&model.Redemption{}).Count(&redemptionCount).Error)
	require.NoError(t, db.Model(&model.TopUp{}).Count(&topUpCount).Error)
	assert.Zero(t, flowCount)
	assert.Zero(t, redemptionCount)
	assert.Zero(t, topUpCount)

	var storedUser model.User
	require.NoError(t, db.First(&storedUser, user.Id).Error)
	require.NotNil(t, storedUser.AccessToken)
	assert.Equal(t, existingPAT, *storedUser.AccessToken)
	assert.Equal(t, 1000, storedUser.Quota)
	var logCount, outboxCount int64
	require.NoError(t, db.Model(&model.Log{}).Count(&logCount).Error)
	require.NoError(t, db.Model(&model.AuditLogOutbox{}).Count(&outboxCount).Error)
	assert.Zero(t, logCount)
	assert.Zero(t, outboxCount)
	var backups []model.TwoFABackupCode
	require.NoError(t, db.Where("user_id = ?", user.Id).Find(&backups).Error)
	require.Len(t, backups, 1)
	assert.Equal(t, originalBackup.CodeHash, backups[0].CodeHash)
}
