package model

import (
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type legacyRelayQuotaReservationTrustSchema struct {
	ID             int64  `gorm:"primaryKey;autoIncrement"`
	ReservationID  string `gorm:"type:varchar(64);not null;uniqueIndex"`
	UserID         int    `gorm:"not null"`
	TokenID        int
	TokenUnlimited bool   `gorm:"not null;default:false"`
	FundingSource  string `gorm:"type:varchar(32);not null"`
	RequestedQuota int    `gorm:"not null"`
	ReservedQuota  int    `gorm:"not null"`
	TokenReserved  int    `gorm:"not null"`
	Status         string `gorm:"type:varchar(32);not null"`
}

func (legacyRelayQuotaReservationTrustSchema) TableName() string {
	return "relay_quota_reservations"
}

type legacyRelayQuotaReviewTrustSchema struct {
	ID             int64  `gorm:"primaryKey;autoIncrement"`
	EventID        string `gorm:"type:varchar(64);not null;uniqueIndex"`
	ReservationID  string `gorm:"type:varchar(64);not null"`
	Revision       int    `gorm:"not null"`
	OperatorUserID int    `gorm:"not null"`
	Action         string `gorm:"type:varchar(16);not null"`
	FromStatus     string `gorm:"type:varchar(32);not null"`
	ToStatus       string `gorm:"type:varchar(32);not null"`
	Operation      string `gorm:"type:varchar(16);not null"`
	UserID         int    `gorm:"not null"`
	TokenID        int
	TokenUnlimited bool `gorm:"not null;default:false"`
	FundingSource  string
	RequestedQuota int
	ReservedQuota  int
	TokenReserved  int
	ActualQuota    int
	CreatedAt      int64
}

func (legacyRelayQuotaReviewTrustSchema) TableName() string {
	return "relay_quota_reservation_review_events"
}

func TestRelayQuotaTrustMarkerFreshAndLegacyMigration(t *testing.T) {
	for _, test := range []struct {
		name   string
		legacy bool
	}{
		{name: "fresh"},
		{name: "legacy", legacy: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "trust-marker.db")), &gorm.Config{})
			require.NoError(t, err)
			previousDB, previousLogDB := DB, LOG_DB
			DB, LOG_DB = db, db
			t.Cleanup(func() { DB, LOG_DB = previousDB, previousLogDB })

			reservationID := test.name + "-trust-marker"
			eventID := test.name + "-trust-event"
			if test.legacy {
				require.NoError(t, db.AutoMigrate(
					&legacyRelayQuotaReservationTrustSchema{},
					&legacyRelayQuotaReviewTrustSchema{},
				))
				require.NoError(t, db.Create(&legacyRelayQuotaReservationTrustSchema{
					ReservationID: reservationID, UserID: 7, TokenID: 8,
					FundingSource: "wallet", RequestedQuota: 9, ReservedQuota: 9,
					TokenReserved: 9, Status: RelayQuotaReservationStatusHeld,
				}).Error)
				require.NoError(t, db.Create(&legacyRelayQuotaReviewTrustSchema{
					EventID: eventID, ReservationID: reservationID,
					Revision: 1, OperatorUserID: 1, Action: RelayQuotaReservationReviewActionRetry,
					FromStatus: RelayQuotaReservationStatusManualReview,
					ToStatus:   RelayQuotaReservationStatusPendingSettlement,
					Operation:  RelayQuotaReservationOperationSettle, UserID: 7, TokenID: 8,
					FundingSource: "wallet", RequestedQuota: 9, ReservedQuota: 9,
					TokenReserved: 9, ActualQuota: 9, CreatedAt: 1,
				}).Error)
			}

			require.NoError(t, migrateDB())
			assert.True(t, db.Migrator().HasColumn(&RelayQuotaReservationRecord{}, "TrustQuotaBypassed"))
			assert.True(t, db.Migrator().HasColumn(&RelayQuotaReservationReviewEvent{}, "TrustQuotaBypassed"))
			for _, field := range []string{
				"ViolationFeeStatus", "ViolationFeeFailureCode", "ViolationFeeAttempts", "ViolationFeeCode",
				"ViolationFeeQuota", "ViolationFeeAmount", "ViolationFeeGroupRatio", "ViolationFeeChannelID",
				"ViolationFeeAuditEventID", "ViolationFeeModelName", "ViolationFeeGroup", "ViolationFeeRequestID",
				"ViolationFeeStatusCode", "ViolationFeeUseTime", "ViolationFeeIsStream",
				"ViolationFeeUpdatedAt", "ViolationFeeChargedAt",
			} {
				assert.True(t, db.Migrator().HasColumn(&RelayQuotaReservationRecord{}, field), field)
			}
			for _, field := range []string{
				"ViolationFeeCode", "ViolationFeeQuota", "ViolationFeeAmount", "ViolationFeeGroupRatio",
				"ViolationFeeChannelID", "ViolationFeeAuditEventID", "ViolationFeeFromStatus",
				"ViolationFeeToStatus", "ViolationFeeFailureCode", "ViolationFeeAttempts",
			} {
				assert.True(t, db.Migrator().HasColumn(&RelayQuotaReservationReviewEvent{}, field), field)
			}

			if !test.legacy {
				require.NoError(t, db.Create(&RelayQuotaReservationRecord{
					ReservationID: reservationID, UserID: 7, TokenID: 8,
					FundingSource: "wallet", RequestedQuota: 9, ReservedQuota: 9,
					TokenReserved: 9, Status: RelayQuotaReservationStatusHeld,
				}).Error)
				require.NoError(t, db.Create(&RelayQuotaReservationReviewEvent{
					EventID: eventID, ReservationID: reservationID, Revision: 1,
					OperatorUserID: 1, Action: RelayQuotaReservationReviewActionRetry,
					FromStatus: RelayQuotaReservationStatusManualReview,
					ToStatus:   RelayQuotaReservationStatusPendingSettlement,
					Operation:  RelayQuotaReservationOperationSettle, UserID: 7, TokenID: 8,
					FundingSource: "wallet", RequestedQuota: 9, ReservedQuota: 9,
					TokenReserved: 9, ActualQuota: 9, CreatedAt: 1,
				}).Error)
			}
			var reservation RelayQuotaReservationRecord
			require.NoError(t, db.Where("reservation_id = ?", reservationID).First(&reservation).Error)
			assert.False(t, reservation.TrustQuotaBypassed)
			assert.Equal(t, 9, reservation.ReservedQuota)
			var event RelayQuotaReservationReviewEvent
			require.NoError(t, db.Where("event_id = ?", eventID).First(&event).Error)
			assert.False(t, event.TrustQuotaBypassed)
			assert.Equal(t, 9, event.ReservedQuota)

			// Rolling restarts must keep the newly added marker stable.
			require.NoError(t, migrateDB())
		})
	}
}
