package service

import (
	"errors"
	"fmt"
	"net/url"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
)

// ImportLegacyOneAPI imports users, tokens, channels, and options from a legacy
// one-api SQLite database into TokenRouter. It is idempotent: rows whose unique
// key already exists are skipped. Returns per-entity import counts.
func ImportLegacyOneAPI(legacyPath string) (map[string]int, error) {
	legacyURI := (&url.URL{Scheme: "file", Path: legacyPath, RawQuery: "mode=ro"}).String()
	legacyDB, err := gorm.Open(sqlite.Open(legacyURI), &gorm.Config{})
	if err != nil {
		return nil, err
	}
	if sqlDB, sqlErr := legacyDB.DB(); sqlErr == nil {
		defer sqlDB.Close()
	}

	// Read the complete source snapshot before opening the target transaction.
	// Import must never turn a source read failure into an apparently successful
	// partial migration.
	var users []model.User
	if err := legacyDB.Unscoped().Find(&users).Error; err != nil {
		return nil, fmt.Errorf("read legacy users: %w", err)
	}
	var tokens []model.Token
	if err := legacyDB.Unscoped().Find(&tokens).Error; err != nil {
		return nil, fmt.Errorf("read legacy tokens: %w", err)
	}
	var channels []model.Channel
	if err := legacyDB.Find(&channels).Error; err != nil {
		return nil, fmt.Errorf("read legacy channels: %w", err)
	}
	var options []model.Option
	if err := legacyDB.Find(&options).Error; err != nil {
		return nil, fmt.Errorf("read legacy options: %w", err)
	}

	counts := map[string]int{"users": 0, "tokens": 0, "channels": 0, "options": 0}
	if err := validateLegacyPasswordHashes(users); err != nil {
		return counts, err
	}
	err = model.DB.Transaction(func(tx *gorm.DB) error {
		userIDs := make(map[int]int, len(users))
		createdUserIDs := make(map[int]int, len(users))

		// Users are matched by their durable username. New rows receive target
		// IDs so an import into a populated installation cannot collide with an
		// unrelated local account. Token ownership is remapped below.
		for i := range users {
			legacyUser := &users[i]
			var existing model.User
			lookupErr := tx.Unscoped().Where("username = ?", legacyUser.Username).First(&existing).Error
			if lookupErr == nil {
				userIDs[legacyUser.Id] = existing.Id
				continue
			}
			if !errors.Is(lookupErr, gorm.ErrRecordNotFound) {
				return fmt.Errorf("look up target user %q: %w", legacyUser.Username, lookupErr)
			}

			legacyID := legacyUser.Id
			inserted := *legacyUser
			inserted.Id = 0
			// Inviter IDs refer to the source primary-key space. Restore them only
			// after every source user has a target mapping.
			inserted.InviterId = 0
			if err := tx.Create(&inserted).Error; err != nil {
				return fmt.Errorf("import user %q: %w", legacyUser.Username, err)
			}
			if err := model.ClaimUserExternalIdentitiesWithTx(tx, &inserted); err != nil {
				return fmt.Errorf("import user %q external identities: %w", legacyUser.Username, err)
			}
			userIDs[legacyID] = inserted.Id
			createdUserIDs[legacyID] = inserted.Id
			counts["users"]++
		}

		for i := range users {
			legacyUser := &users[i]
			createdID, created := createdUserIDs[legacyUser.Id]
			if !created || legacyUser.InviterId == 0 {
				continue
			}
			inviterID, exists := userIDs[legacyUser.InviterId]
			if !exists {
				return fmt.Errorf("import user %q: inviter %d is missing from the source user set", legacyUser.Username, legacyUser.InviterId)
			}
			result := tx.Unscoped().Model(&model.User{}).Where("id = ?", createdID).Update("inviter_id", inviterID)
			if result.Error != nil {
				return fmt.Errorf("import user %q inviter: %w", legacyUser.Username, result.Error)
			}
			if result.RowsAffected != 1 {
				return fmt.Errorf("import user %q inviter: target user disappeared", legacyUser.Username)
			}
		}

		for i := range tokens {
			legacyToken := &tokens[i]
			ownerID, exists := userIDs[legacyToken.UserId]
			if !exists {
				return fmt.Errorf("import token %q: source user %d is missing", legacyToken.Name, legacyToken.UserId)
			}
			var existing model.Token
			lookupErr := tx.Unscoped().Where("key = ?", legacyToken.Key).First(&existing).Error
			if lookupErr == nil {
				if existing.UserId != ownerID {
					return fmt.Errorf("import token %q: key is already owned by a different target user", legacyToken.Name)
				}
				continue
			}
			if !errors.Is(lookupErr, gorm.ErrRecordNotFound) {
				return fmt.Errorf("look up target token %q: %w", legacyToken.Name, lookupErr)
			}
			inserted := *legacyToken
			inserted.Id = 0
			inserted.UserId = ownerID
			if err := tx.Create(&inserted).Error; err != nil {
				return fmt.Errorf("import token %q: %w", legacyToken.Name, err)
			}
			counts["tokens"]++
		}

		for i := range channels {
			legacyChannel := &channels[i]
			var existing model.Channel
			lookupErr := tx.Where("name = ?", legacyChannel.Name).First(&existing).Error
			if lookupErr == nil {
				continue
			}
			if !errors.Is(lookupErr, gorm.ErrRecordNotFound) {
				return fmt.Errorf("look up target channel %q: %w", legacyChannel.Name, lookupErr)
			}
			inserted := *legacyChannel
			inserted.Id = 0
			if err := tx.Create(&inserted).Error; err != nil {
				return fmt.Errorf("import channel %q: %w", legacyChannel.Name, err)
			}
			counts["channels"]++
		}

		for i := range options {
			legacyOption := &options[i]
			var existing model.Option
			lookupErr := tx.Where("key = ?", legacyOption.Key).First(&existing).Error
			if lookupErr == nil {
				continue
			}
			if !errors.Is(lookupErr, gorm.ErrRecordNotFound) {
				return fmt.Errorf("look up target option %q: %w", legacyOption.Key, lookupErr)
			}
			inserted := *legacyOption
			if err := tx.Create(&inserted).Error; err != nil {
				return fmt.Errorf("import option %q: %w", legacyOption.Key, err)
			}
			counts["options"]++
		}
		return nil
	})
	if err != nil {
		return map[string]int{"users": 0, "tokens": 0, "channels": 0, "options": 0}, err
	}

	return counts, nil
}

func validateLegacyPasswordHashes(users []model.User) error {
	for index := range users {
		user := &users[index]
		if user.Password == "" {
			// OAuth-only accounts deliberately have no online password verifier.
			continue
		}
		if _, err := common.ValidatePasswordBcryptHash(user.Password); err != nil {
			return fmt.Errorf("validate legacy user %q password credential: %w", user.Username, err)
		}
	}
	return nil
}
