package tasks

import (
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"github.com/tokenrouter/tokenrouter/internal/auth/roles"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	channelssvc "github.com/tokenrouter/tokenrouter/internal/channels"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
)

// relayAccountingFixture is intentionally local to the task package. Engine
// accounting tests exercise private settlement mechanics, while task tests
// reuse only the database and quota setup represented here.
type relayAccountingFixture struct {
	user    model.User
	token   model.Token
	channel model.Channel
}

func newRelayAccountingFixture(t *testing.T, userQuota, tokenQuota int) relayAccountingFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)
	oldDB, oldLogDB := model.DB, model.LOG_DB
	dsn := "file:" + filepath.Join(t.TempDir(), "relay-accounting.db") + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&model.User{}, &model.Token{}, &model.Channel{}, &model.Ability{}, &model.Log{}, &model.AuditLogOutbox{}, &model.PerfMetric{},
		&model.SubscriptionPlan{}, &model.UserSubscription{}, &model.SubscriptionPreConsumeRecord{},
		&model.RelayQuotaReservationRecord{},
		&model.Option{},
	))
	model.DB = db
	model.LOG_DB = db
	require.NoError(t, setting.Init())
	previousPrices := billingsvc.ExportedModelPrices()
	previousRatios := billingsvc.ExportedGroupRatios()
	previousSpecialRatios := billingsvc.ExportedGroupGroupRatios()
	billingsvc.SetGroupRatios(map[string]float64{"default": 1})
	billingsvc.SetGroupGroupRatios(map[string]map[string]float64{})
	t.Cleanup(func() {
		billingsvc.SetModelPriceRegistry(previousPrices)
		billingsvc.SetGroupRatios(previousRatios)
		billingsvc.SetGroupGroupRatios(previousSpecialRatios)
		model.DB = oldDB
		model.LOG_DB = oldLogDB
	})

	user := model.User{
		Username: "accounting-user", Password: "x", Role: roles.RoleCommonUser,
		Status: model.UserStatusEnabled, Group: "default", Quota: userQuota, AuthVersion: 1,
	}
	require.NoError(t, db.Create(&user).Error)
	token := model.Token{
		UserId: user.Id, Key: "sk-accounting", Name: "accounting-token",
		Status: billingsvc.TokenStatusEnabled, RemainQuota: tokenQuota,
	}
	require.NoError(t, db.Create(&token).Error)
	weight := uint(1)
	channel := model.Channel{
		Name: "accounting-channel", Type: int(channelcatalog.ChannelTypeOpenAI), Key: "upstream-key",
		Status: channelcatalog.ChannelStatusEnabled, BaseURL: "https://example.invalid", Models: "accounting-model",
		Group: "default", Weight: &weight,
	}
	require.NoError(t, db.Create(&channel).Error)
	require.NoError(t, db.Create(&model.Ability{
		Group: "default", Model: "accounting-model", ChannelId: channel.Id, Enabled: true, Weight: 1,
	}).Error)
	require.NoError(t, channelssvc.InitAbilityCache())
	return relayAccountingFixture{user: user, token: token, channel: channel}
}
