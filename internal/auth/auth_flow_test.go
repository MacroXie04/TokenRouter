package auth

import (
	"errors"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tokenrouter/tokenrouter/internal/platform/cryptoutil"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func initAuthFlowTestDB(t *testing.T, models ...any) *gorm.DB {
	t.Helper()
	dsn := "file:" + filepath.Join(t.TempDir(), "auth-flow.db") +
		"?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	// A single connection makes the intended select/update interleaving
	// deterministic while goroutines still race at the application boundary.
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, db.AutoMigrate(models...))
	model.DB = db
	model.LOG_DB = db
	return db
}

func TestConsumeAuthFlowConcurrentSingleWinner(t *testing.T) {
	initAuthFlowTestDB(t, &model.AuthFlow{})
	token, err := CreateAuthFlow(AuthFlowPurposeOAuth, "github", "login", 7, "", "", time.Minute)
	require.NoError(t, err)

	const attempts = 16
	start := make(chan struct{})
	results := make(chan error, attempts)
	var ready sync.WaitGroup
	ready.Add(attempts)
	for range attempts {
		go func() {
			ready.Done()
			<-start
			_, consumeErr := ConsumeAuthFlow(token, AuthFlowPurposeOAuth)
			results <- consumeErr
		}()
	}
	ready.Wait()
	close(start)

	successes := 0
	for range attempts {
		consumeErr := <-results
		if consumeErr == nil {
			successes++
			continue
		}
		assert.ErrorIs(t, consumeErr, ErrInvalidFlowToken)
	}
	assert.Equal(t, 1, successes, "a one-time flow must have exactly one consumer")
}

func TestPeekAndExactConsumeAuthFlow(t *testing.T) {
	initAuthFlowTestDB(t, &model.AuthFlow{})
	token, err := CreateAuthFlow(AuthFlowPurposeOAuth, "github", "bind", 7, "session-7", "payload", time.Minute)
	require.NoError(t, err)

	peeked, err := PeekAuthFlow(token, AuthFlowPurposeOAuth)
	require.NoError(t, err)
	assert.Equal(t, "github", peeked.Provider)
	assert.Equal(t, "bind", peeked.Intent)
	assert.Equal(t, 7, peeked.UserId)
	assert.Equal(t, "session-7", peeked.SessionId)
	assert.Nil(t, peeked.ConsumedAt)

	validMatch := AuthFlowMatch{
		Purpose: AuthFlowPurposeOAuth, Provider: "github", Intent: "bind", UserId: 7, SessionId: "session-7",
	}
	mismatches := []AuthFlowMatch{
		{Purpose: "other", Provider: "github", Intent: "bind", UserId: 7, SessionId: "session-7"},
		{Purpose: AuthFlowPurposeOAuth, Provider: "discord", Intent: "bind", UserId: 7, SessionId: "session-7"},
		{Purpose: AuthFlowPurposeOAuth, Provider: "github", Intent: "login", UserId: 7, SessionId: "session-7"},
		{Purpose: AuthFlowPurposeOAuth, Provider: "github", Intent: "bind", UserId: 8, SessionId: "session-7"},
		{Purpose: AuthFlowPurposeOAuth, Provider: "github", Intent: "bind", UserId: 7, SessionId: "session-8"},
	}
	for _, mismatch := range mismatches {
		_, consumeErr := ConsumeAuthFlowExact(token, mismatch)
		assert.ErrorIs(t, consumeErr, ErrInvalidFlowToken)
		stillPending, peekErr := PeekAuthFlow(token, AuthFlowPurposeOAuth)
		require.NoError(t, peekErr)
		assert.Nil(t, stillPending.ConsumedAt, "a mismatched consumer must not burn the flow")
	}

	consumed, err := ConsumeAuthFlowExact(token, validMatch)
	require.NoError(t, err)
	assert.NotNil(t, consumed.ConsumedAt)
	_, err = PeekAuthFlow(token, AuthFlowPurposeOAuth)
	assert.ErrorIs(t, err, ErrInvalidFlowToken)
	_, err = ConsumeAuthFlowExact(token, validMatch)
	assert.ErrorIs(t, err, ErrInvalidFlowToken)
}

func TestConsumeAuthFlowExactConcurrentSingleWinner(t *testing.T) {
	initAuthFlowTestDB(t, &model.AuthFlow{})
	token, err := CreateAuthFlow(AuthFlowPurposeOAuth, "github", "login", 0, "", "", time.Minute)
	require.NoError(t, err)
	match := AuthFlowMatch{Purpose: AuthFlowPurposeOAuth, Provider: "github", Intent: "login"}

	const attempts = 16
	start := make(chan struct{})
	results := make(chan error, attempts)
	var ready sync.WaitGroup
	ready.Add(attempts)
	for range attempts {
		go func() {
			ready.Done()
			<-start
			_, consumeErr := ConsumeAuthFlowExact(token, match)
			results <- consumeErr
		}()
	}
	ready.Wait()
	close(start)

	successes := 0
	for range attempts {
		consumeErr := <-results
		if consumeErr == nil {
			successes++
			continue
		}
		assert.ErrorIs(t, consumeErr, ErrInvalidFlowToken)
	}
	assert.Equal(t, 1, successes)
}

func TestPeekAuthFlowRejectsExpiredFlowWithoutConsuming(t *testing.T) {
	db := initAuthFlowTestDB(t, &model.AuthFlow{})
	token, err := CreateAuthFlow(AuthFlowPurposeOAuth, "github", "login", 0, "", "", time.Minute)
	require.NoError(t, err)
	require.NoError(t, db.Model(&model.AuthFlow{}).
		Where("token_hash = ?", cryptoutil.SHA256Hex(token)).Update("expires_at", time.Now().UTC().Add(-time.Second)).Error)

	_, err = PeekAuthFlow(token, AuthFlowPurposeOAuth)
	assert.ErrorIs(t, err, ErrInvalidFlowToken)
	var stored model.AuthFlow
	require.NoError(t, db.Where("token_hash = ?", cryptoutil.SHA256Hex(token)).First(&stored).Error)
	assert.Nil(t, stored.ConsumedAt)
}

func TestConsumeAuthFlowActionFailureRollsBackConsumption(t *testing.T) {
	db := initAuthFlowTestDB(t, &model.AuthFlow{}, &model.Option{})
	token, err := CreateAuthFlow(AuthFlowPurposeOAuth, "github", "login", 7, "", "", time.Minute)
	require.NoError(t, err)

	injected := errors.New("injected action failure")
	_, err = ConsumeAuthFlowWithAction(token, AuthFlowPurposeOAuth, func(tx *gorm.DB, _ *model.AuthFlow) error {
		require.NoError(t, tx.Create(&model.Option{Key: "should-roll-back", Value: "1"}).Error)
		return injected
	})
	require.ErrorIs(t, err, injected)

	var stored model.AuthFlow
	require.NoError(t, db.Where("token_hash = ?", cryptoutil.SHA256Hex(token)).First(&stored).Error)
	assert.Nil(t, stored.ConsumedAt)
	var optionCount int64
	require.NoError(t, db.Model(&model.Option{}).Where("key = ?", "should-roll-back").Count(&optionCount).Error)
	assert.Zero(t, optionCount, "the action mutation must roll back with flow consumption")

	_, err = ConsumeAuthFlow(token, AuthFlowPurposeOAuth)
	require.NoError(t, err, "a rolled-back action must leave the flow retryable")
}

func TestResetPasswordConsumesAndMutatesAtomicallyUnderConcurrency(t *testing.T) {
	db := initAuthFlowTestDB(t, &model.User{}, &model.AuthFlow{}, &model.UserSession{})
	oldHash, err := cryptoutil.PasswordHash("old-password")
	require.NoError(t, err)
	user := model.User{
		Username:      "reset-race",
		Password:      oldHash,
		Email:         "reset-race@example.com",
		EmailVerified: true,
		Status:        model.UserStatusEnabled,
		AuthVersion:   9,
	}
	require.NoError(t, db.Create(&user).Error)
	for _, sid := range []string{"session-a", "session-b"} {
		require.NoError(t, db.Create(&model.UserSession{
			SID: sid, UserID: user.Id, Status: SessionStatusActive,
			RefreshHash: "refresh-" + sid, UserAuthVersion: user.AuthVersion,
		}).Error)
	}
	const code = "724913"
	_, err = CreateAuthFlow(PasswordResetPurpose, "email", "", user.Id, "", code, time.Minute)
	require.NoError(t, err)

	const attempts = 8
	start := make(chan struct{})
	results := make(chan error, attempts)
	var ready sync.WaitGroup
	ready.Add(attempts)
	for range attempts {
		go func() {
			ready.Done()
			<-start
			results <- ResetPassword(user.Email, code, "new-password")
		}()
	}
	ready.Wait()
	close(start)

	successes := 0
	for range attempts {
		if resetErr := <-results; resetErr == nil {
			successes++
		} else {
			assert.Contains(t, resetErr.Error(), "验证码错误或已过期")
		}
	}
	assert.Equal(t, 1, successes, "a reset code must update the account exactly once")

	var updated model.User
	require.NoError(t, db.First(&updated, user.Id).Error)
	assert.True(t, cryptoutil.PasswordVerify("new-password", updated.Password))
	assert.EqualValues(t, 10, updated.AuthVersion)

	var sessions []model.UserSession
	require.NoError(t, db.Where("user_id = ?", user.Id).Find(&sessions).Error)
	require.Len(t, sessions, 2)
	for _, session := range sessions {
		assert.Equal(t, SessionStatusRevoked, session.Status)
		assert.Equal(t, "password_reset", session.RevokedReason)
		assert.NotZero(t, session.RevokedAt)
	}
}
