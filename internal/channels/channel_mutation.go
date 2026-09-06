package channels

import (
	"fmt"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// CreateChannelWithAbilities persists a channel and all of its routing
// abilities atomically, then refreshes the in-memory routing cache. A cache
// refresh cannot share the database transaction, so its failure is returned
// explicitly after the durable rows have committed.
func CreateChannelWithAbilities(channel *model.Channel) error {
	if err := validateChannelForCreate(channel); err != nil {
		return err
	}
	if err := model.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(channel).Error; err != nil {
			return err
		}
		return createChannelAbilitiesTx(tx, channel)
	}); err != nil {
		return fmt.Errorf("create channel and abilities: %w", err)
	}
	if err := SyncAbilityCache(); err != nil {
		return fmt.Errorf("refresh ability cache after channel create: %w", err)
	}
	return nil
}

// CreateChannelsWithAbilities persists a bounded batch as one unit. This is
// used by the reference-compatible batch-create envelope so an ability error
// cannot leave only a prefix of the requested channels behind.
func CreateChannelsWithAbilities(channels []*model.Channel) error {
	if len(channels) == 0 {
		return invalidChannelInput("channels", "must not be empty")
	}
	for _, channel := range channels {
		if err := validateChannelForCreate(channel); err != nil {
			return err
		}
	}
	if err := model.DB.Transaction(func(tx *gorm.DB) error {
		for _, channel := range channels {
			if err := tx.Create(channel).Error; err != nil {
				return err
			}
			if err := createChannelAbilitiesTx(tx, channel); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return fmt.Errorf("create channel batch and abilities: %w", err)
	}
	if err := SyncAbilityCache(); err != nil {
		return fmt.Errorf("refresh ability cache after channel batch create: %w", err)
	}
	return nil
}

// UpdateChannelWithAbilities updates a channel and rebuilds its routing
// abilities in the same transaction, preventing a partially updated routing
// catalog when an ability write fails.
func UpdateChannelWithAbilities(channelID int, updates map[string]any) error {
	if channelID <= 0 {
		return gorm.ErrRecordNotFound
	}
	if len(updates) == 0 {
		return nil
	}
	if err := validateChannelUpdateFields(updates); err != nil {
		return err
	}
	if err := model.DB.Transaction(func(tx *gorm.DB) error {
		var channel model.Channel
		if err := channelLockForUpdate(tx).First(&channel, channelID).Error; err != nil {
			return err
		}
		if err := tx.Model(&model.Channel{}).Where("id = ?", channelID).Updates(updates).Error; err != nil {
			return err
		}
		if err := tx.First(&channel, channelID).Error; err != nil {
			return err
		}
		if err := tx.Where("channel_id = ?", channelID).Delete(&model.Ability{}).Error; err != nil {
			return err
		}
		return createChannelAbilitiesTx(tx, &channel)
	}); err != nil {
		return fmt.Errorf("update channel and abilities: %w", err)
	}
	if err := SyncAbilityCache(); err != nil {
		return fmt.Errorf("refresh ability cache after channel update: %w", err)
	}
	return nil
}

// DeleteChannelWithAbilities removes a channel and all of its routing
// abilities atomically, then refreshes the in-memory routing cache.
func DeleteChannelWithAbilities(channelID int) error {
	if channelID <= 0 {
		return gorm.ErrRecordNotFound
	}
	if err := model.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("channel_id = ?", channelID).Delete(&model.Ability{}).Error; err != nil {
			return err
		}
		result := tx.Where("id = ?", channelID).Delete(&model.Channel{})
		return result.Error
	}); err != nil {
		return fmt.Errorf("delete channel and abilities: %w", err)
	}
	if err := SyncAbilityCache(); err != nil {
		return fmt.Errorf("refresh ability cache after channel delete: %w", err)
	}
	return nil
}

func createChannelAbilitiesTx(tx *gorm.DB, channel *model.Channel) error {
	abilities := buildChannelAbilities(channel)
	if len(abilities) == 0 {
		return nil
	}
	return tx.Create(&abilities).Error
}

// channelLockForUpdate uses row locks on databases that support SELECT FOR
// UPDATE. SQLite serializes writers at the database level and rejects this
// syntax, so it intentionally uses an ordinary query there.
func channelLockForUpdate(tx *gorm.DB) *gorm.DB {
	if model.UsingPostgreSQL() || model.UsingMySQL() {
		return tx.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	return tx
}
