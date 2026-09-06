package billing

import (
	"context"
	"errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	wallclock "github.com/tokenrouter/tokenrouter/internal/platform/clock"
	"github.com/tokenrouter/tokenrouter/internal/platform/jsonutil"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"github.com/tokenrouter/tokenrouter/internal/testutil"
	"testing"
)

func TestDeliverClaimedAuditLogContextPropagatesCancellationToSink(t *testing.T) {
	db := testutil.OpenPeriodicContextTestDB(t, &model.Log{}, &model.AuditLogOutbox{})
	eventID := "periodic-context-sink"
	payload, err := jsonutil.Marshal(auditLogOutboxPayload{
		EventID: eventID,
		Log:     model.Log{AuditEventId: &eventID, CreatedAt: wallclock.NowTimestamp()},
	})
	require.NoError(t, err)
	record := &model.AuditLogOutbox{
		EventID: eventID, Payload: string(payload), Status: model.AuditLogOutboxStatusPending,
	}

	err = deliverClaimedAuditLogContext(testutil.CanceledPeriodicContext(), record)
	require.True(t, errors.Is(err, context.Canceled), "unexpected cancellation error: %v", err)
	var count int64
	require.NoError(t, db.Model(&model.Log{}).Count(&count).Error)
	assert.Zero(t, count)
}
