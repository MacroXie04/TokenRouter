package auth

import (
	"context"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"github.com/tokenrouter/tokenrouter/internal/testutil"
	"strings"
	"testing"
	"time"
)

func TestCleanupAuthFlowsAtUsesCallerDatabaseClockBoundary(t *testing.T) {
	db := testutil.OpenPeriodicContextTestDB(t, &model.AuthFlow{})
	const now int64 = 2_000_000
	cutoff := time.Unix(now, 0).UTC().Add(-24 * time.Hour)
	consumedAt := time.Unix(now-1, 0).UTC()
	flows := []model.AuthFlow{
		{TokenHash: strings.Repeat("a", 64), Purpose: "oauth", ExpiresAt: cutoff.Add(-time.Second)},
		{TokenHash: strings.Repeat("b", 64), Purpose: "oauth", ExpiresAt: cutoff},
		{TokenHash: strings.Repeat("c", 64), Purpose: "oauth", ExpiresAt: time.Unix(now-1, 0).UTC()},
		{TokenHash: strings.Repeat("d", 64), Purpose: "oauth", ExpiresAt: time.Unix(now+60, 0).UTC(), ConsumedAt: &consumedAt},
	}
	require.NoError(t, db.Create(&flows).Error)
	require.NoError(t, cleanupAuthFlowsAt(context.Background(), now))

	var hashes []string
	require.NoError(t, db.Model(&model.AuthFlow{}).Order("token_hash asc").Pluck("token_hash", &hashes).Error)
	assert.Equal(t, []string{strings.Repeat("b", 64), strings.Repeat("c", 64)}, hashes,
		"only flows strictly older than the database-clock cutoff and consumed flows are removed")
}
