package auth

import (
	"context"
	"errors"
	"fmt"
	"github.com/tokenrouter/tokenrouter/internal/platform/env"
	"github.com/tokenrouter/tokenrouter/internal/platform/logging"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	defaultUserSessionActiveLimit          = 50
	defaultUserSessionIssuanceLimit        = 100
	defaultUserSessionIssuanceWindow       = 24 * time.Hour
	defaultUserSessionRevokedRetention     = 7 * 24 * time.Hour
	defaultUserSessionHourlyAlertThreshold = 5000

	maxUserSessionCountLimit       = 1_000_000
	maxUserSessionPolicyDays       = 100 * 365
	userSessionListLimit           = 100
	userSessionRevokeBatchSize     = 500
	userSessionCleanupScanLimit    = 1000
	userSessionCleanupDeleteBatch  = 500
	userSessionCreationLockStripes = 256
)

type userSessionPolicy struct {
	activeLimit          int64
	issuanceLimit        int64
	issuanceWindow       time.Duration
	revokedRetention     time.Duration
	hourlyAlertThreshold int64
}

var userSessionCreationLocks [userSessionCreationLockStripes]sync.Mutex

// loadUserSessionPolicy validates every session-growth control together. The
// issuance window is capped by tombstone retention so cleanup can never erase
// rows that still contribute to the anti-churn limit.
func LoadUserSessionPolicy() (userSessionPolicy, error) {
	activeLimit, err := env.StrictScheduledIntegerEnv(
		"USER_SESSION_ACTIVE_LIMIT", defaultUserSessionActiveLimit, 1, maxUserSessionCountLimit,
	)
	if err != nil {
		return userSessionPolicy{}, err
	}
	issuanceLimit, err := env.StrictScheduledIntegerEnv(
		"USER_SESSION_ISSUANCE_LIMIT", defaultUserSessionIssuanceLimit, 1, maxUserSessionCountLimit,
	)
	if err != nil {
		return userSessionPolicy{}, err
	}
	maxPolicySeconds := maxUserSessionPolicyDays * 24 * 60 * 60
	windowSeconds, err := env.StrictScheduledIntegerEnv(
		"USER_SESSION_ISSUANCE_WINDOW_SECONDS",
		int(defaultUserSessionIssuanceWindow/time.Second),
		1,
		maxPolicySeconds,
	)
	if err != nil {
		return userSessionPolicy{}, err
	}
	retentionDays, err := env.StrictScheduledIntegerEnv(
		"USER_SESSION_REVOKED_RETENTION_DAYS",
		int(defaultUserSessionRevokedRetention/(24*time.Hour)),
		1,
		maxUserSessionPolicyDays,
	)
	if err != nil {
		return userSessionPolicy{}, err
	}
	alertThreshold, err := env.StrictScheduledIntegerEnv(
		"USER_SESSION_HOURLY_ALERT_THRESHOLD",
		defaultUserSessionHourlyAlertThreshold,
		1,
		maxUserSessionCountLimit,
	)
	if err != nil {
		return userSessionPolicy{}, err
	}

	issuanceWindow := time.Duration(windowSeconds) * time.Second
	revokedRetention := time.Duration(retentionDays) * 24 * time.Hour
	if issuanceWindow > revokedRetention {
		issuanceWindow = revokedRetention
	}
	return userSessionPolicy{
		activeLimit:          int64(activeLimit),
		issuanceLimit:        int64(issuanceLimit),
		issuanceWindow:       issuanceWindow,
		revokedRetention:     revokedRetention,
		hourlyAlertThreshold: int64(alertThreshold),
	}, nil
}

func userSessionCreationLock(userID int) *sync.Mutex {
	index := userID % len(userSessionCreationLocks)
	if index < 0 {
		index = -index
	}
	return &userSessionCreationLocks[index]
}

func normalizeSessionMetadata(value string, maximumBytes int) string {
	value = strings.ToValidUTF8(strings.TrimSpace(value), "")
	if len(value) <= maximumBytes {
		return value
	}
	end := maximumBytes
	for end > 0 && !utf8.ValidString(value[:end]) {
		end--
	}
	return value[:end]
}

// CleanupUserSessions reports unexpectedly high global issuance and removes
// expired or long-revoked rows in bounded pages. Rows inside the issuance
// window are retained regardless of state so logout/revoke churn cannot evade
// the issuance limit.
func CleanupUserSessions(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	policy, err := LoadUserSessionPolicy()
	if err != nil {
		return err
	}
	if model.DB == nil {
		return errors.New("user session cleanup database is unavailable")
	}
	now, err := model.DatabaseUnixTimestamp(model.DB.WithContext(ctx))
	if err != nil {
		return err
	}
	db := model.DB.WithContext(ctx)
	var errs []error
	var hourlyCount int64
	if err := db.Model(&model.UserSession{}).
		Where("created_at > ?", time.Unix(now-int64(time.Hour/time.Second), 0)).
		Count(&hourlyCount).Error; err != nil {
		errs = append(errs, fmt.Errorf("count hourly user session issuance: %w", err))
	} else if hourlyCount > policy.hourlyAlertThreshold {
		logging.SysError(fmt.Sprintf(
			"hourly user session issuance exceeded alert threshold: count=%d threshold=%d window_seconds=%d",
			hourlyCount,
			policy.hourlyAlertThreshold,
			int64(time.Hour/time.Second),
		))
	}

	issuanceCutoff := time.Unix(now-int64(policy.issuanceWindow/time.Second), 0)
	revokedBefore := now - int64(policy.revokedRetention/time.Second)
	if err := deleteExpiredUserSessions(db, now, issuanceCutoff, revokedBefore); err != nil {
		errs = append(errs, fmt.Errorf("delete expired user sessions: %w", err))
	}
	if err := deleteOldRevokedUserSessions(db, revokedBefore, issuanceCutoff); err != nil {
		errs = append(errs, fmt.Errorf("delete old revoked user sessions: %w", err))
	}
	return errors.Join(errs...)
}

func deleteExpiredUserSessions(db *gorm.DB, expiredBefore int64, issuanceCutoff time.Time, revokedBefore int64) error {
	for {
		var sids []string
		query := db.Model(&model.UserSession{}).Where(
			"expires_at < ? AND created_at <= ? AND (status <> ? OR revoked_at <= 0 OR revoked_at < ?)",
			expiredBefore,
			issuanceCutoff,
			SessionStatusRevoked,
			revokedBefore,
		)
		if err := query.Order("expires_at").Limit(userSessionCleanupScanLimit).Pluck("sid", &sids).Error; err != nil {
			return err
		}
		if len(sids) == 0 {
			return nil
		}
		for start := 0; start < len(sids); start += userSessionCleanupDeleteBatch {
			end := min(start+userSessionCleanupDeleteBatch, len(sids))
			if err := db.Where("sid IN ?", sids[start:end]).Where(
				"expires_at < ? AND created_at <= ? AND (status <> ? OR revoked_at <= 0 OR revoked_at < ?)",
				expiredBefore,
				issuanceCutoff,
				SessionStatusRevoked,
				revokedBefore,
			).Delete(&model.UserSession{}).Error; err != nil {
				return err
			}
		}
	}
}

func deleteOldRevokedUserSessions(db *gorm.DB, revokedBefore int64, issuanceCutoff time.Time) error {
	for {
		var sids []string
		query := db.Model(&model.UserSession{}).Where(
			"status = ? AND revoked_at > 0 AND revoked_at < ? AND created_at <= ?",
			SessionStatusRevoked,
			revokedBefore,
			issuanceCutoff,
		)
		if err := query.Order("revoked_at").Limit(userSessionCleanupScanLimit).Pluck("sid", &sids).Error; err != nil {
			return err
		}
		if len(sids) == 0 {
			return nil
		}
		for start := 0; start < len(sids); start += userSessionCleanupDeleteBatch {
			end := min(start+userSessionCleanupDeleteBatch, len(sids))
			if err := db.Where("sid IN ?", sids[start:end]).Where(
				"status = ? AND revoked_at > 0 AND revoked_at < ? AND created_at <= ?",
				SessionStatusRevoked,
				revokedBefore,
				issuanceCutoff,
			).Delete(&model.UserSession{}).Error; err != nil {
				return err
			}
		}
	}
}
