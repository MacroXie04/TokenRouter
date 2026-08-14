package service

import (
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/model"
)

// ImportLegacyOneAPI imports users, tokens, channels, and options from a legacy
// one-api SQLite database into TokenRouter. It is idempotent: rows whose unique
// key already exists are skipped. Returns per-entity import counts.
func ImportLegacyOneAPI(legacyPath string) (map[string]int, error) {
	legacyDB, err := gorm.Open(sqlite.Open(legacyPath), &gorm.Config{})
	if err != nil {
		return nil, err
	}
	counts := map[string]int{}

	// Users (unique key: username).
	var users []model.User
	legacyDB.Unscoped().Find(&users)
	for i := range users {
		u := &users[i]
		var existing model.User
		if model.DB.Where("username = ?", u.Username).First(&existing).Error == nil {
			continue
		}
		if model.DB.Create(u).Error == nil {
			counts["users"]++
		}
	}

	// Tokens (unique key: key).
	var tokens []model.Token
	legacyDB.Unscoped().Find(&tokens)
	for i := range tokens {
		t := &tokens[i]
		var existing model.Token
		if model.DB.Where("key = ?", t.Key).First(&existing).Error == nil {
			continue
		}
		if model.DB.Create(t).Error == nil {
			counts["tokens"]++
		}
	}

	// Channels (dedup by name).
	var channels []model.Channel
	legacyDB.Find(&channels)
	for i := range channels {
		ch := &channels[i]
		var existing model.Channel
		if model.DB.Where("name = ?", ch.Name).First(&existing).Error == nil {
			continue
		}
		if model.DB.Create(ch).Error == nil {
			counts["channels"]++
		}
	}

	// Options (unique key: key).
	var options []model.Option
	legacyDB.Find(&options)
	for i := range options {
		o := &options[i]
		var existing model.Option
		if model.DB.Where("key = ?", o.Key).First(&existing).Error == nil {
			continue
		}
		if model.DB.Create(o).Error == nil {
			counts["options"]++
		}
	}

	return counts, nil
}
