package store

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	stdmail "net/mail"
	"strings"

	"gorm.io/gorm"
)

const maxEmailAddressBytes = 50

// NormalizeVerifiedEmail validates and canonicalizes an email address, and
// returns the collation-independent key used for verified-email ownership.
// A nullable unique key lets any number of unverified/empty addresses coexist
// while guaranteeing one durable owner for every verified recovery address.
func NormalizeVerifiedEmail(raw string) (normalized, key string, err error) {
	normalized = strings.ToLower(strings.TrimSpace(raw))
	if normalized == "" || len(normalized) > maxEmailAddressBytes ||
		strings.ContainsAny(normalized, "\x00\r\n") {
		return "", "", invalidIdentifier("user", "email")
	}
	parsed, parseErr := stdmail.ParseAddress(normalized)
	if parseErr != nil || parsed.Address != normalized {
		return "", "", invalidIdentifier("user", "email")
	}
	sum := sha256.Sum256([]byte(normalized))
	return normalized, hex.EncodeToString(sum[:]), nil
}

// initializeVerifiedEmailKeys backfills the nullable unique recovery-email
// key after AutoMigrate has added the column and index. It includes deleted
// users so deleting an account cannot silently transfer its recovery identity.
func initializeVerifiedEmailKeys() error {
	return DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Unscoped().Model(&User{}).
			Where("email_verified = ?", false).
			Where("verified_email_key IS NOT NULL").
			Update("verified_email_key", nil).Error; err != nil {
			return err
		}

		var users []User
		if err := tx.Unscoped().Where("email_verified = ?", true).Order("id").Find(&users).Error; err != nil {
			return err
		}
		ownerByKey := make(map[string]int, len(users))
		type normalizedUser struct {
			id, owner int
			email     string
			key       string
		}
		normalizedUsers := make([]normalizedUser, 0, len(users))
		for _, user := range users {
			email, key, err := NormalizeVerifiedEmail(user.Email)
			if err != nil {
				return fmt.Errorf("%w: user %d has an invalid verified email", ErrAmbiguousPersistentIdentity, user.Id)
			}
			if previous, exists := ownerByKey[key]; exists && previous != user.Id {
				return fmt.Errorf("%w: verified email is owned by users %d and %d", ErrAmbiguousPersistentIdentity, previous, user.Id)
			}
			ownerByKey[key] = user.Id
			normalizedUsers = append(normalizedUsers, normalizedUser{id: user.Id, owner: user.Id, email: email, key: key})
		}
		for _, user := range normalizedUsers {
			key := user.key
			result := tx.Unscoped().Model(&User{}).Where("id = ? AND email_verified = ?", user.id, true).
				Updates(map[string]any{"email": user.email, "verified_email_key": &key})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return fmt.Errorf("%w: verified email owner %d changed during migration", ErrAmbiguousPersistentIdentity, user.owner)
			}
		}
		return nil
	})
}
