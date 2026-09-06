package auth

import (
	"context"
	"errors"
	"fmt"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tokenrouter/tokenrouter/internal/platform/cryptoutil"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

func setTestSessionPolicy(t *testing.T, active, issuance, windowSeconds, retentionDays, alert int) {
	t.Helper()
	t.Setenv("USER_SESSION_ACTIVE_LIMIT", fmt.Sprintf("%d", active))
	t.Setenv("USER_SESSION_ISSUANCE_LIMIT", fmt.Sprintf("%d", issuance))
	t.Setenv("USER_SESSION_ISSUANCE_WINDOW_SECONDS", fmt.Sprintf("%d", windowSeconds))
	t.Setenv("USER_SESSION_REVOKED_RETENTION_DAYS", fmt.Sprintf("%d", retentionDays))
	t.Setenv("USER_SESSION_HOURLY_ALERT_THRESHOLD", fmt.Sprintf("%d", alert))
}

func TestUserSessionPolicyDefaultsValidationAndRetentionFloor(t *testing.T) {
	setTestSessionPolicy(t,
		defaultUserSessionActiveLimit,
		defaultUserSessionIssuanceLimit,
		int(defaultUserSessionIssuanceWindow/time.Second),
		int(defaultUserSessionRevokedRetention/(24*time.Hour)),
		defaultUserSessionHourlyAlertThreshold,
	)
	policy, err := LoadUserSessionPolicy()
	require.NoError(t, err)
	assert.EqualValues(t, defaultUserSessionActiveLimit, policy.activeLimit)
	assert.EqualValues(t, defaultUserSessionIssuanceLimit, policy.issuanceLimit)
	assert.Equal(t, defaultUserSessionIssuanceWindow, policy.issuanceWindow)
	assert.Equal(t, defaultUserSessionRevokedRetention, policy.revokedRetention)
	assert.EqualValues(t, defaultUserSessionHourlyAlertThreshold, policy.hourlyAlertThreshold)

	t.Setenv("USER_SESSION_ISSUANCE_WINDOW_SECONDS", "172800")
	t.Setenv("USER_SESSION_REVOKED_RETENTION_DAYS", "1")
	policy, err = LoadUserSessionPolicy()
	require.NoError(t, err)
	assert.Equal(t, 24*time.Hour, policy.issuanceWindow,
		"issuance evidence must never outlive its configured tombstone retention")

	for _, test := range []struct {
		key   string
		value string
	}{
		{key: "USER_SESSION_ACTIVE_LIMIT", value: "0"},
		{key: "USER_SESSION_ACTIVE_LIMIT", value: "01"},
		{key: "USER_SESSION_ISSUANCE_LIMIT", value: "1000001"},
		{key: "USER_SESSION_ISSUANCE_WINDOW_SECONDS", value: "forever"},
		{key: "USER_SESSION_REVOKED_RETENTION_DAYS", value: "-1"},
		{key: "USER_SESSION_HOURLY_ALERT_THRESHOLD", value: "0"},
	} {
		t.Run(test.key+"="+test.value, func(t *testing.T) {
			setTestSessionPolicy(t, 50, 100, 86400, 7, 5000)
			t.Setenv(test.key, test.value)
			_, err := LoadUserSessionPolicy()
			require.Error(t, err)
			assert.Contains(t, err.Error(), test.key)
		})
	}
}

func TestCreateSessionEnforcesActiveAndIssuanceLimits(t *testing.T) {
	t.Run("active includes stale auth versions", func(t *testing.T) {
		setTestSessionPolicy(t, 1, 10, 86400, 7, 5000)
		initAuthDB(t)
		user := seedPasswordUser(t, "session-active-limit", "password123")
		now := time.Now().UTC()
		require.NoError(t, model.DB.Create(&model.UserSession{
			SID:             "stale-auth-version-session",
			UserID:          user.Id,
			Version:         1,
			UserAuthVersion: user.AuthVersion + 10,
			Status:          SessionStatusActive,
			RefreshHash:     fmt.Sprintf("%064x", 1),
			LoginMethod:     "test",
			CreatedAt:       now,
			LastActiveAt:    now.Unix(),
			ExpiresAt:       now.Add(time.Hour).Unix(),
		}).Error)

		_, _, err := CreateSession(user, "127.0.0.1", "agent", "password")
		assert.ErrorIs(t, err, ErrSessionLimit)
		var count int64
		require.NoError(t, model.DB.Model(&model.UserSession{}).Count(&count).Error)
		assert.EqualValues(t, 1, count)
	})

	t.Run("revoked rows still consume issuance budget", func(t *testing.T) {
		setTestSessionPolicy(t, 10, 2, 86400, 7, 5000)
		initAuthDB(t)
		user := seedPasswordUser(t, "session-issuance-limit", "password123")
		for i := range 2 {
			sid, _, err := CreateSession(user, "127.0.0.1", "agent", "password")
			require.NoError(t, err)
			revoked, err := RevokeUserSession(user.Id, sid, "test_churn")
			require.NoError(t, err)
			require.True(t, revoked, i)
		}
		_, _, err := CreateSession(user, "127.0.0.1", "agent", "password")
		assert.ErrorIs(t, err, ErrSessionIssuanceLimit)
	})
}

func TestCreateSessionUsesFreshAccountStateAndBoundsMetadata(t *testing.T) {
	setTestSessionPolicy(t, 10, 20, 86400, 7, 5000)
	initAuthDB(t)
	stale := seedPasswordUser(t, "session-stale-account", "password123")
	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", stale.Id).
		UpdateColumn("auth_version", stale.AuthVersion+1).Error)
	_, _, err := CreateSession(stale, "127.0.0.1", "agent", "password")
	assert.ErrorIs(t, err, ErrSessionRevoked)

	var count int64
	require.NoError(t, model.DB.Model(&model.UserSession{}).Where("user_id = ?", stale.Id).Count(&count).Error)
	assert.Zero(t, count)

	var fresh model.User
	require.NoError(t, model.DB.First(&fresh, stale.Id).Error)
	longAgent := strings.Repeat("界", 300)
	sid, refresh, err := CreateSession(&fresh, "  2001:db8::1  ", longAgent, "   ")
	require.NoError(t, err)
	var session model.UserSession
	require.NoError(t, model.DB.Where("sid = ?", sid).First(&session).Error)
	assert.Equal(t, refreshTokenHash(refresh), session.RefreshHash)
	assert.NotEqual(t, cryptoutil.SHA256Hex(refresh), session.RefreshHash)
	assert.Equal(t, "unknown", session.LoginMethod)
	assert.Equal(t, "2001:db8::1", session.IP)
	assert.LessOrEqual(t, len(session.UserAgent), 512)
	assert.True(t, utf8.ValidString(session.UserAgent))
	assert.Equal(t, session.CreatedAt.Add(30*24*time.Hour).Unix(), session.ExpiresAt)
}

func TestConcurrentSessionCreationCannotExceedLimit(t *testing.T) {
	setTestSessionPolicy(t, 1, 10, 86400, 7, 5000)
	dsn := "file:" + filepath.Join(t.TempDir(), "sessions.db") + "?_pragma=busy_timeout(5000)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.UserSession{}, &model.AuthFlow{}))
	previousDB := model.DB
	model.DB = db
	t.Cleanup(func() { model.DB = previousDB })
	user := seedPasswordUser(t, "session-concurrent-limit", "password123")

	start := make(chan struct{})
	errs := make([]error, 2)
	var wait sync.WaitGroup
	for i := range errs {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			_, _, errs[index] = CreateSession(user, "127.0.0.1", "agent", "password")
		}(i)
	}
	close(start)
	wait.Wait()

	successes := 0
	limitFailures := 0
	for _, err := range errs {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrSessionLimit):
			limitFailures++
		default:
			require.NoError(t, err)
		}
	}
	assert.Equal(t, 1, successes)
	assert.Equal(t, 1, limitFailures)
	var count int64
	require.NoError(t, db.Model(&model.UserSession{}).Count(&count).Error)
	assert.EqualValues(t, 1, count)
}

func TestUserSessionListIsBoundedAndKeepsCurrentFirst(t *testing.T) {
	setTestSessionPolicy(t, 1000, 2000, 86400, 7, 5000)
	initAuthDB(t)
	user := seedPasswordUser(t, "session-list-limit", "password123")
	base := time.Now().UTC().Add(-time.Hour)
	rows := make([]model.UserSession, 0, userSessionListLimit+5)
	for i := 0; i < userSessionListLimit+5; i++ {
		created := base.Add(time.Duration(i) * time.Second)
		rows = append(rows, model.UserSession{
			SID:             fmt.Sprintf("session-%03d", i),
			UserID:          user.Id,
			Version:         1,
			UserAuthVersion: user.AuthVersion,
			Status:          SessionStatusActive,
			RefreshHash:     fmt.Sprintf("%064x", i+1),
			LoginMethod:     "test",
			CreatedAt:       created,
			LastActiveAt:    created.Unix(),
			ExpiresAt:       base.Add(24 * time.Hour).Unix(),
		})
	}
	require.NoError(t, model.DB.CreateInBatches(&rows, 50).Error)

	currentSID := rows[0].SID
	views, err := GetUserSessionsForCurrent(user.Id, currentSID)
	require.NoError(t, err)
	require.Len(t, views, userSessionListLimit)
	assert.Equal(t, currentSID, views[0].SID)
	assert.True(t, views[0].Current)
	for i := 1; i < len(views); i++ {
		assert.False(t, views[i].Current)
		assert.NotEqual(t, currentSID, views[i].SID)
	}
	assert.Equal(t, rows[len(rows)-1].SID, views[1].SID)

	withoutCurrent, err := GetUserSessions(user.Id)
	require.NoError(t, err)
	require.Len(t, withoutCurrent, userSessionListLimit)
	assert.Equal(t, rows[len(rows)-1].SID, withoutCurrent[0].SID)
	assert.False(t, withoutCurrent[0].Current)
}

func TestCleanupUserSessionsPreservesIssuanceEvidenceAndRecentTombstones(t *testing.T) {
	setTestSessionPolicy(t, 50, 100, 86400, 7, 5000)
	initAuthDB(t)
	user := seedPasswordUser(t, "session-cleanup", "password123")
	now := time.Now().UTC()
	old := now.Add(-10 * 24 * time.Hour)
	recent := now.Add(-time.Hour)
	rows := []model.UserSession{
		cleanupSessionRow(user, "expired-old", 1, old, SessionStatusActive, 0, now.Add(-time.Hour).Unix()),
		cleanupSessionRow(user, "revoked-old", 2, old, SessionStatusRevoked, old.Unix(), now.Add(time.Hour).Unix()),
		cleanupSessionRow(user, "revoked-recent-created", 3, recent, SessionStatusRevoked, old.Unix(), now.Add(time.Hour).Unix()),
		cleanupSessionRow(user, "expired-recent-tombstone", 4, old, SessionStatusRevoked, recent.Unix(), now.Add(-time.Hour).Unix()),
		cleanupSessionRow(user, "live-old", 5, old, SessionStatusActive, 0, now.Add(time.Hour).Unix()),
	}
	require.NoError(t, model.DB.Create(&rows).Error)
	require.NoError(t, CleanupUserSessions(context.Background()))

	for sid, shouldExist := range map[string]bool{
		"expired-old":              false,
		"revoked-old":              false,
		"revoked-recent-created":   true,
		"expired-recent-tombstone": true,
		"live-old":                 true,
	} {
		var count int64
		require.NoError(t, model.DB.Model(&model.UserSession{}).Where("sid = ?", sid).Count(&count).Error)
		assert.EqualValues(t, map[bool]int64{true: 1, false: 0}[shouldExist], count, sid)
	}
}

func TestCleanupUserSessionsDrainsMoreThanOneScanPage(t *testing.T) {
	setTestSessionPolicy(t, 50, 100, 86400, 7, 5000)
	initAuthDB(t)
	user := seedPasswordUser(t, "session-cleanup-pages", "password123")
	now := time.Now().UTC()
	old := now.Add(-10 * 24 * time.Hour)
	rows := make([]model.UserSession, 0, userSessionCleanupScanLimit+1)
	for i := 0; i < userSessionCleanupScanLimit+1; i++ {
		rows = append(rows, cleanupSessionRow(
			user,
			fmt.Sprintf("expired-page-%04d", i),
			i+1,
			old,
			SessionStatusActive,
			0,
			now.Add(-time.Hour).Unix(),
		))
	}
	require.NoError(t, model.DB.CreateInBatches(&rows, 100).Error)
	require.NoError(t, CleanupUserSessions(context.Background()))
	var count int64
	require.NoError(t, model.DB.Model(&model.UserSession{}).Count(&count).Error)
	assert.Zero(t, count)
}

func TestRevokeOtherSessionsDrainsBoundedPagesAndSkipsExpiredRows(t *testing.T) {
	setTestSessionPolicy(t, 2000, 3000, 86400, 7, 5000)
	initAuthDB(t)
	user := seedPasswordUser(t, "session-revoke-pages", "password123")
	now := time.Now().UTC()
	rows := make([]model.UserSession, 0, userSessionRevokeBatchSize+3)
	rows = append(rows, cleanupSessionRow(
		user, "keep-current", 1, now, SessionStatusActive, 0, now.Add(time.Hour).Unix(),
	))
	for i := 0; i < userSessionRevokeBatchSize+1; i++ {
		rows = append(rows, cleanupSessionRow(
			user,
			fmt.Sprintf("revoke-page-%04d", i),
			i+2,
			now,
			SessionStatusActive,
			0,
			now.Add(time.Hour).Unix(),
		))
	}
	rows = append(rows, cleanupSessionRow(
		user, "expired-active", userSessionRevokeBatchSize+3, now.Add(-time.Hour),
		SessionStatusActive, 0, now.Add(-time.Minute).Unix(),
	))
	require.NoError(t, model.DB.CreateInBatches(&rows, 100).Error)

	count, err := RevokeOtherSessionsWithCount(user.Id, "keep-current")
	require.NoError(t, err)
	assert.EqualValues(t, userSessionRevokeBatchSize+1, count)

	var current, expired model.UserSession
	require.NoError(t, model.DB.Where("sid = ?", "keep-current").First(&current).Error)
	require.NoError(t, model.DB.Where("sid = ?", "expired-active").First(&expired).Error)
	assert.Equal(t, SessionStatusActive, current.Status)
	assert.Equal(t, SessionStatusActive, expired.Status)
	var revokedCount int64
	require.NoError(t, model.DB.Model(&model.UserSession{}).
		Where("user_id = ? AND status = ?", user.Id, SessionStatusRevoked).
		Count(&revokedCount).Error)
	assert.EqualValues(t, userSessionRevokeBatchSize+1, revokedCount)
}

func cleanupSessionRow(
	user *model.User,
	sid string,
	hashSeed int,
	createdAt time.Time,
	status string,
	revokedAt int64,
	expiresAt int64,
) model.UserSession {
	return model.UserSession{
		SID:             sid,
		UserID:          user.Id,
		Version:         1,
		UserAuthVersion: user.AuthVersion,
		Status:          status,
		RefreshHash:     fmt.Sprintf("%064x", hashSeed),
		LoginMethod:     "test",
		CreatedAt:       createdAt,
		LastActiveAt:    createdAt.Unix(),
		ExpiresAt:       expiresAt,
		RevokedAt:       revokedAt,
		RevokedReason:   "test",
	}
}
