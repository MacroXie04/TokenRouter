package auth

import (
	"errors"
	"github.com/tokenrouter/tokenrouter/internal/platform/cryptoutil"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"strings"
)

// LoginSessionView is the bounded, non-secret representation returned to a
// signed-in browser. Internal version counters and refresh-token state never
// cross the API boundary.
type LoginSessionView struct {
	SID          string `json:"sid"`
	Current      bool   `json:"current"`
	LoginMethod  string `json:"login_method"`
	IP           string `json:"ip"`
	UserAgent    string `json:"user_agent"`
	CreatedAt    int64  `json:"created_at"`
	LastActiveAt int64  `json:"last_active_at"`
	ExpiresAt    int64  `json:"expires_at"`
}

func revokeActiveSession(userId int, sid, reason string) (bool, error) {
	now, err := model.DatabaseUnixTimestamp(model.DB)
	if err != nil {
		return false, err
	}
	return revokeActiveSessionAt(userId, sid, reason, now)
}

func revokeActiveSessionAt(userId int, sid, reason string, now int64) (bool, error) {
	if model.DB == nil || now <= 0 {
		return false, ErrSessionRevoked
	}
	result := model.DB.Model(&model.UserSession{}).
		Where("user_id = ? AND sid = ? AND status = ? AND revoked_at = 0 AND expires_at > ?",
			userId, sid, SessionStatusActive, now).
		Updates(map[string]any{
			"status":         SessionStatusRevoked,
			"revoked_at":     now,
			"revoked_reason": reason,
		})
	return result.RowsAffected == 1, result.Error
}

// RevokeUserSession revokes one active session only when it belongs to the
// acting user. A missing, already-revoked, or foreign SID is reported
// identically through the false result.
func RevokeUserSession(userId int, sid, reason string) (bool, error) {
	if userId <= 0 || sid == "" {
		return false, nil
	}
	return revokeActiveSession(userId, sid, reason)
}

// RevokeSessionByRefreshToken authenticates logout with possession of the
// current refresh token (or its immediately previous value during the rotation
// grace window). Possession of a SID alone never authorizes revocation.
func RevokeSessionByRefreshToken(sid, refreshToken string) (bool, error) {
	if sid == "" || refreshToken == "" {
		return false, nil
	}
	now, err := model.DatabaseUnixTimestamp(model.DB)
	if err != nil {
		return false, err
	}
	presentedHash := refreshTokenHash(refreshToken)
	legacyPresentedHash := cryptoutil.SHA256Hex(refreshToken)
	result := model.DB.Model(&model.UserSession{}).
		Where("sid = ? AND status = ? AND revoked_at = 0 AND expires_at > ?", sid, SessionStatusActive, now).
		Where(
			"refresh_hash IN ? OR (previous_refresh_hash IN ? AND previous_valid_until >= ?)",
			[]string{presentedHash, legacyPresentedHash},
			[]string{presentedHash, legacyPresentedHash},
			now,
		).
		Updates(map[string]any{
			"status":         SessionStatusRevoked,
			"revoked_at":     now,
			"revoked_reason": "logout",
		})
	return result.RowsAffected == 1, result.Error
}

// RevokeAllUserSessions revokes every active session for a user.
func RevokeAllUserSessions(userId int) error {
	return revokeAllUserSessions(model.DB, userId, "revoke_all")
}

func revokeAllUserSessions(db *gorm.DB, userId int, reason string) error {
	if userId <= 0 {
		return nil
	}
	_, err := revokeUserSessionsInBatches(db, userId, "", reason)
	return err
}

// GetUserSessions lists at most the newest bounded set of currently usable
// sessions. Callers without a current browser identity use this compatibility
// wrapper; HTTP handlers use GetUserSessionsForCurrent so the present session
// is never displaced by newer rows.
func GetUserSessions(userId int) ([]LoginSessionView, error) {
	return GetUserSessionsForCurrent(userId, "")
}

func GetUserSessionsForCurrent(userId int, currentSID string) ([]LoginSessionView, error) {
	if userId <= 0 || model.DB == nil {
		return nil, ErrSessionRevoked
	}
	var user model.User
	if err := model.DB.Select("id", "status", "auth_version").First(&user, userId).Error; err != nil {
		return nil, err
	}
	if user.Status != model.UserStatusEnabled || user.AuthVersion <= 0 {
		return nil, ErrSessionRevoked
	}
	currentSID = strings.TrimSpace(currentSID)
	if len(currentSID) > 64 {
		return nil, ErrSessionRevoked
	}
	now, err := model.DatabaseUnixTimestamp(model.DB)
	if err != nil {
		return nil, err
	}
	sessions := make([]model.UserSession, 0, userSessionListLimit)
	if currentSID != "" {
		var current []model.UserSession
		if err := model.DB.Where(
			"user_id = ? AND user_auth_version = ? AND status = ? AND revoked_at = 0 AND expires_at > ? AND sid = ?",
			userId, user.AuthVersion, SessionStatusActive, now, currentSID,
		).Limit(1).Find(&current).Error; err != nil {
			return nil, err
		}
		if len(current) == 1 {
			sessions = append(sessions, current[0])
		}
	}

	remaining := userSessionListLimit - len(sessions)
	query := model.DB.Where(
		"user_id = ? AND user_auth_version = ? AND status = ? AND revoked_at = 0 AND expires_at > ?",
		userId, user.AuthVersion, SessionStatusActive, now,
	)
	if currentSID != "" {
		query = query.Where("sid <> ?", currentSID)
	}
	var others []model.UserSession
	if err := query.Order("last_active_at desc").Order("created_at desc").Limit(remaining).Find(&others).Error; err != nil {
		return nil, err
	}
	sessions = append(sessions, others...)

	views := make([]LoginSessionView, 0, len(sessions))
	for i := range sessions {
		views = append(views, LoginSessionView{
			SID:          sessions[i].SID,
			Current:      currentSID != "" && sessions[i].SID == currentSID,
			LoginMethod:  sessions[i].LoginMethod,
			IP:           sessions[i].IP,
			UserAgent:    sessions[i].UserAgent,
			CreatedAt:    sessions[i].CreatedAt.Unix(),
			LastActiveAt: sessions[i].LastActiveAt,
			ExpiresAt:    sessions[i].ExpiresAt,
		})
	}
	return views, nil
}

// RevokeOtherSessions marks every other active session revoked while retaining
// tombstones for immediate access-token denial and auditability.
func RevokeOtherSessions(userId int, keepSid string) error {
	_, err := RevokeOtherSessionsWithCount(userId, keepSid)
	return err
}

// RevokeOtherSessionsWithCount returns the exact number of other sessions
// changed by this request while retaining the original error-only helper for
// service callers that do not need response metadata.
func RevokeOtherSessionsWithCount(userId int, keepSid string) (int64, error) {
	if userId <= 0 || keepSid == "" {
		return 0, ErrSessionRevoked
	}
	var current model.UserSession
	now, err := model.DatabaseUnixTimestamp(model.DB)
	if err != nil {
		return 0, err
	}
	if err := model.DB.Where("user_id = ? AND sid = ? AND status = ? AND revoked_at = 0 AND expires_at > ?",
		userId, keepSid, SessionStatusActive, now).First(&current).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, ErrSessionRevoked
		}
		return 0, err
	}
	var user model.User
	if err := model.DB.First(&user, userId).Error; err != nil {
		return 0, err
	}
	if user.Status != model.UserStatusEnabled || user.AuthVersion != current.UserAuthVersion {
		return 0, ErrSessionRevoked
	}
	return revokeUserSessionsInBatchesAt(model.DB, userId, keepSid, "revoke_others", now)
}

func revokeUserSessionsInBatches(db *gorm.DB, userId int, excludedSID, reason string) (int64, error) {
	if db == nil || userId <= 0 {
		return 0, ErrSessionRevoked
	}
	now, err := model.DatabaseUnixTimestamp(db)
	if err != nil {
		return 0, err
	}
	return revokeUserSessionsInBatchesAt(db, userId, excludedSID, reason, now)
}

func revokeUserSessionsInBatchesAt(db *gorm.DB, userId int, excludedSID, reason string, now int64) (int64, error) {
	if db == nil || userId <= 0 || now <= 0 {
		return 0, ErrSessionRevoked
	}
	var total int64
	for {
		query := db.Model(&model.UserSession{}).
			Where("user_id = ? AND status = ? AND revoked_at = 0 AND expires_at > ?",
				userId, SessionStatusActive, now)
		if excludedSID != "" {
			query = query.Where("sid <> ?", excludedSID)
		}
		var sids []string
		if err := query.Order("sid asc").Limit(userSessionRevokeBatchSize).Pluck("sid", &sids).Error; err != nil {
			return total, err
		}
		if len(sids) == 0 {
			return total, nil
		}
		result := db.Model(&model.UserSession{}).
			Where("user_id = ? AND sid IN ? AND status = ? AND revoked_at = 0 AND expires_at > ?",
				userId, sids, SessionStatusActive, now).
			Updates(map[string]any{
				"status":         SessionStatusRevoked,
				"revoked_at":     now,
				"revoked_reason": reason,
			})
		if result.Error != nil {
			return total, result.Error
		}
		total += result.RowsAffected
	}
}
