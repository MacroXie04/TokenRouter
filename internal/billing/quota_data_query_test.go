package billing

import (
	"context"
	"errors"
	"fmt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tokenrouter/tokenrouter/internal/auth/roles"
	wallclock "github.com/tokenrouter/tokenrouter/internal/platform/clock"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"testing"
)

func TestValidateDashboardDataRange(t *testing.T) {
	start := wallclock.NowTimestamp() - 3_600
	assert.ErrorIs(t, ValidateDashboardDataRange(0, start), ErrInvalidDashboardDataRange)
	assert.ErrorIs(t, ValidateDashboardDataRange(start, 0), ErrInvalidDashboardDataRange)
	assert.ErrorIs(t, ValidateDashboardDataRange(start+1, start), ErrInvalidDashboardDataRange)
	assert.ErrorIs(t, ValidateDashboardDataRange(
		start, start+DashboardDataMaxRangeSeconds+1,
	), ErrDashboardDataRangeTooLarge)
	assert.NoError(t, ValidateDashboardDataRange(start, start+DashboardDataMaxRangeSeconds))
}

func TestDashboardDataQueriesRejectInvalidAndOversizedRanges(t *testing.T) {
	setupQuotaDataTestDB(t)
	start := wallclock.NowTimestamp() - 3_600
	queries := map[string]func(int64, int64) error{
		"all": func(from, to int64) error {
			_, err := GetAllQuotaDatesContext(context.Background(), from, to, "")
			return err
		},
		"username": func(from, to int64) error {
			_, err := GetQuotaDataByUsernameContext(context.Background(), "alice", from, to)
			return err
		},
		"self": func(from, to int64) error {
			_, err := GetQuotaDataByUserIDContext(context.Background(), 1, from, to)
			return err
		},
		"users": func(from, to int64) error {
			_, err := GetQuotaDataGroupByUserContext(context.Background(), from, to)
			return err
		},
		"flow": func(from, to int64) error {
			_, err := GetFlowQuotaDataContext(context.Background(), from, to, "", 1, roles.RoleCommonUser)
			return err
		},
	}
	for name, query := range queries {
		t.Run(name, func(t *testing.T) {
			assert.ErrorIs(t, query(start+1, start), ErrInvalidDashboardDataRange)
			assert.ErrorIs(t,
				query(start, start+DashboardDataMaxRangeSeconds+1),
				ErrDashboardDataRangeTooLarge,
			)
		})
	}
}

func TestDashboardDataQueriesHonorCancellation(t *testing.T) {
	setupQuotaDataTestDB(t)
	start := wallclock.NowTimestamp() - 3_600
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	queries := map[string]func() error{
		"all": func() error {
			_, err := GetAllQuotaDatesContext(ctx, start, start, "")
			return err
		},
		"username": func() error {
			_, err := GetQuotaDataByUsernameContext(ctx, "alice", start, start)
			return err
		},
		"self": func() error {
			_, err := GetQuotaDataByUserIDContext(ctx, 1, start, start)
			return err
		},
		"users": func() error {
			_, err := GetQuotaDataGroupByUserContext(ctx, start, start)
			return err
		},
		"flow": func() error {
			_, err := GetFlowQuotaDataContext(ctx, start, start, "", 1, roles.RoleCommonUser)
			return err
		},
	}
	for name, query := range queries {
		t.Run(name, func(t *testing.T) {
			require.ErrorIs(t, query(), context.Canceled)
		})
	}
}

func TestDashboardDataQueryResultCap(t *testing.T) {
	setupQuotaDataTestDB(t)
	createdAt := wallclock.NowTimestamp() - wallclock.NowTimestamp()%3_600
	rows := make([]model.QuotaData, DashboardDataMaxRows+1)
	for index := range rows {
		rows[index] = model.QuotaData{
			UserID: index + 1, Username: fmt.Sprintf("dashboard-user-%05d", index),
			ModelName: "gpt-4o", CreatedAt: createdAt, UseGroup: "default",
			Count: 1, Quota: 1, TokenUsed: 1,
		}
	}
	require.NoError(t, model.DB.CreateInBatches(&rows, 500).Error)

	result, err := GetQuotaDataGroupByUserContext(context.Background(), createdAt, createdAt)
	assert.Nil(t, result)
	assert.True(t, errors.Is(err, ErrDashboardDataTooLarge), "unexpected error: %v", err)
}

func TestDashboardFlowCancellationBetweenAggregationAndEnrichment(t *testing.T) {
	setupQuotaDataTestDB(t)
	createdAt := wallclock.NowTimestamp() - wallclock.NowTimestamp()%3_600
	require.NoError(t, model.DB.Create(&model.QuotaData{
		UserID: 1, Username: "alice", ModelName: "gpt-4o", CreatedAt: createdAt,
		UseGroup: "default", TokenID: 7, Count: 1, Quota: 1, TokenUsed: 1,
	}).Error)

	ctx, cancel := context.WithCancel(context.Background())
	callbackName := "test:cancel_dashboard_after_aggregate"
	require.NoError(t, model.DB.Callback().Query().After("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == (model.QuotaData{}).TableName() {
			cancel()
		}
	}))
	t.Cleanup(func() {
		cancel()
		_ = model.DB.Callback().Query().Remove(callbackName)
	})

	result, err := GetFlowQuotaDataContext(
		ctx, createdAt, createdAt, "", 1, roles.RoleCommonUser,
	)
	assert.Nil(t, result)
	assert.ErrorIs(t, err, context.Canceled)
}
