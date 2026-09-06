package catalog

import (
	"context"
	"errors"
	"fmt"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"testing"
	"time"
)

func setupRankingsTestDB(t *testing.T) {
	t.Helper()
	previousDB := model.DB
	previousLogDB := model.LOG_DB
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, db.AutoMigrate(&model.QuotaData{}, &model.Model{}, &model.Vendor{}))
	model.DB = db
	model.LOG_DB = db
	t.Cleanup(func() {
		model.DB = previousDB
		model.LOG_DB = previousLogDB
		require.NoError(t, sqlDB.Close())
	})
}

func insertRankingUsage(t *testing.T, modelName string, tokens int, createdAt time.Time) {
	t.Helper()
	require.NoError(t, model.DB.Create(&model.QuotaData{
		UserID: 1, Username: "ranking-user", ModelName: modelName,
		CreatedAt: createdAt.Unix(), UseGroup: "default", TokenID: 1,
		ChannelID: 1, NodeName: "ranking-node", TokenUsed: tokens,
		Count: 1, Quota: tokens,
	}).Error)
}

func TestBuildRankingsSnapshotMatchesPeriodLeaderboardContract(t *testing.T) {
	setupRankingsTestDB(t)
	now := time.Date(2026, time.January, 15, 12, 30, 0, 0, time.UTC)

	acme := model.Vendor{Name: "Acme", Icon: "acme", Status: 1}
	bee := model.Vendor{Name: "Bee", Icon: "bee", Status: 1}
	require.NoError(t, model.DB.Create(&acme).Error)
	require.NoError(t, model.DB.Create(&bee).Error)
	require.NoError(t, model.DB.Create(&[]model.Model{
		{ModelName: "alpha", VendorID: acme.Id, Status: 1, NameRule: model.ModelNameRuleExact},
		{ModelName: "beta", VendorID: bee.Id, Status: 1, NameRule: model.ModelNameRuleExact},
		{ModelName: "gamma", VendorID: acme.Id, Status: 1, NameRule: model.ModelNameRuleExact},
		{ModelName: "delta", VendorID: acme.Id, Status: 1, NameRule: model.ModelNameRuleExact},
	}).Error)

	insertRankingUsage(t, "alpha", 300, now.Add(-2*time.Hour))
	insertRankingUsage(t, "alpha", 100, now.Add(-26*time.Hour))
	insertRankingUsage(t, "beta", 200, now.Add(-3*time.Hour))
	insertRankingUsage(t, "gamma", 50, now.Add(-4*time.Hour))
	insertRankingUsage(t, "beta", 400, now.Add(-8*24*time.Hour))
	insertRankingUsage(t, "alpha", 100, now.Add(-9*24*time.Hour))
	insertRankingUsage(t, "delta", 50, now.Add(-10*24*time.Hour))
	insertRankingUsage(t, "outside-window", 999, now.Add(-40*24*time.Hour))

	snapshot, err := buildRankingsSnapshot(context.Background(), "week", now)
	require.NoError(t, err)
	require.Len(t, snapshot.Models, 3)
	assert.Equal(t, "alpha", snapshot.Models[0].ModelName)
	assert.Equal(t, 400, int(snapshot.Models[0].TotalTokens))
	require.NotNil(t, snapshot.Models[0].PreviousRank)
	assert.Equal(t, 2, *snapshot.Models[0].PreviousRank)
	assert.InDelta(t, 300, snapshot.Models[0].GrowthPct, 0.0001)
	assert.Equal(t, "Acme", snapshot.Models[0].Vendor)
	assert.Equal(t, "acme", snapshot.Models[0].VendorIcon)
	assert.Equal(t, "all", snapshot.Models[0].Category)
	assert.Equal(t, "beta", snapshot.Models[1].ModelName)
	assert.InDelta(t, -50, snapshot.Models[1].GrowthPct, 0.0001)
	assert.Nil(t, snapshot.Models[2].PreviousRank)

	require.Len(t, snapshot.Vendors, 2)
	assert.Equal(t, "Acme", snapshot.Vendors[0].Vendor)
	assert.Equal(t, int64(450), snapshot.Vendors[0].TotalTokens)
	assert.Equal(t, 2, snapshot.Vendors[0].ModelsCount)
	assert.Equal(t, "alpha", snapshot.Vendors[0].TopModel)
	require.Len(t, snapshot.TopMovers, 1)
	assert.Equal(t, "alpha", snapshot.TopMovers[0].ModelName)
	assert.Equal(t, 1, snapshot.TopMovers[0].RankDelta)
	require.Len(t, snapshot.TopDroppers, 1)
	assert.Equal(t, "beta", snapshot.TopDroppers[0].ModelName)
	assert.Equal(t, -1, snapshot.TopDroppers[0].RankDelta)

	assert.NotEmpty(t, snapshot.ModelsHistory.Points)
	assert.Equal(t, 3, len(snapshot.ModelsHistory.Models))
	assert.Equal(t, 2, snapshot.ModelsHistory.Buckets)
	assert.NotEmpty(t, snapshot.VendorShareHistory.Points)
	assert.Equal(t, 2, snapshot.VendorShareHistory.Buckets)
	for _, point := range snapshot.VendorShareHistory.Points {
		assert.Greater(t, point.Share, 0.0)
		assert.LessOrEqual(t, point.Share, 1.0)
	}
}

func TestBuildRankingsSnapshotEmptyAndPeriodValidation(t *testing.T) {
	setupRankingsTestDB(t)
	now := time.Date(2026, time.February, 1, 0, 0, 0, 0, time.UTC)

	snapshot, err := buildRankingsSnapshot(context.Background(), "today", now)
	require.NoError(t, err)
	assert.Empty(t, snapshot.Models)
	assert.NotNil(t, snapshot.Models)
	assert.Empty(t, snapshot.Vendors)
	assert.NotNil(t, snapshot.Vendors)
	assert.Empty(t, snapshot.TopMovers)
	assert.NotNil(t, snapshot.TopMovers)
	assert.Empty(t, snapshot.ModelsHistory.Points)
	assert.NotNil(t, snapshot.ModelsHistory.Points)
	assert.Empty(t, snapshot.VendorShareHistory.Points)
	assert.NotNil(t, snapshot.VendorShareHistory.Points)

	_, err = buildRankingsSnapshot(context.Background(), "all-time", now)
	assert.ErrorIs(t, err, ErrInvalidRankingPeriod)
	_, err = buildRankingsSnapshot(context.Background(), "WEEK", now)
	assert.ErrorIs(t, err, ErrInvalidRankingPeriod)
}

func TestBuildRankingsSnapshotRejectsUnboundedModelCardinality(t *testing.T) {
	setupRankingsTestDB(t)
	now := time.Date(2026, time.March, 1, 12, 0, 0, 0, time.UTC)
	rows := make([]model.QuotaData, 0, rankingAggregateModelLimit+1)
	for index := 0; index <= rankingAggregateModelLimit; index++ {
		rows = append(rows, model.QuotaData{
			UserID: 1, ModelName: fmt.Sprintf("model-%04d", index),
			CreatedAt: now.Add(-time.Hour).Unix(), TokenUsed: 1,
		})
	}
	require.NoError(t, model.DB.CreateInBatches(rows, 100).Error)

	_, err := buildRankingsSnapshot(context.Background(), "week", now)
	assert.ErrorIs(t, err, ErrRankingsTooLarge)
}

func TestRankingBucketExpressionIsCredentialFreeAndDialectSpecific(t *testing.T) {
	tests := []struct {
		dialect string
		want    string
	}{
		{dialect: "sqlite", want: "(created_at / 3600) * 3600"},
		{dialect: "mysql", want: "FLOOR(created_at / 3600) * 3600"},
		{dialect: "postgres", want: "FLOOR(created_at::numeric / 3600)::bigint * 3600"},
	}
	for _, test := range tests {
		t.Run(test.dialect, func(t *testing.T) {
			got, err := rankingBucketExpression(test.dialect, 3600)
			require.NoError(t, err)
			assert.Equal(t, test.want, got)
		})
	}

	_, err := rankingBucketExpression("clickhouse", 3600)
	assert.ErrorContains(t, err, "unsupported rankings database dialect")
	_, err = rankingBucketExpression("sqlite", 0)
	assert.True(t, errors.Is(err, ErrRankingsTooLarge))
}
