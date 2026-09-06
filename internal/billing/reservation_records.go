package billing

import (
	"errors"
	"fmt"
	model "github.com/tokenrouter/tokenrouter/internal/store"
)

func relayReservationFromRecord(record *model.RelayQuotaReservationRecord) (*RelayQuotaReservation, error) {
	if record == nil || record.ReservationID == "" || record.UserID <= 0 {
		return nil, errors.New("invalid relay quota reservation record")
	}
	if record.UsageEpoch < 0 {
		return nil, errors.New("invalid durable subscription usage epoch")
	}
	if err := validateQuotaAmount(record.RequestedQuota); err != nil {
		return nil, err
	}
	if err := validateQuotaAmount(record.ReservedQuota); err != nil {
		return nil, err
	}
	if err := validateQuotaAmount(record.TokenReserved); err != nil {
		return nil, err
	}
	if record.TokenID < 0 || (record.TokenReserved > 0 && record.TokenID <= 0) ||
		(record.TokenID == 0 && (record.TokenReserved != 0 || !record.TokenUnlimited)) ||
		(record.TokenUnlimited && record.TokenReserved != 0) {
		return nil, errors.New("invalid durable token reservation")
	}
	expectedTokenReserved := record.RequestedQuota
	if record.TokenUnlimited || record.TrustQuotaBypassed || record.FundingSource == BillingSourceFreeModel {
		expectedTokenReserved = 0
	}
	if record.TokenReserved != expectedTokenReserved {
		return nil, errors.New("invalid durable token reservation")
	}
	switch record.FundingSource {
	case BillingSourceWallet:
		expectedReserved := record.RequestedQuota
		if record.TrustQuotaBypassed {
			expectedReserved = 0
		}
		if record.SubscriptionID != 0 || record.UsageEpoch != 0 || record.ReservedQuota != expectedReserved {
			return nil, errors.New("invalid durable wallet reservation")
		}
	case BillingSourceSubscription:
		if record.TrustQuotaBypassed {
			return nil, errors.New("invalid durable subscription trust bypass")
		}
		expectedReserved := record.RequestedQuota
		if expectedReserved == 0 {
			expectedReserved = 1
		}
		if record.SubscriptionID <= 0 || record.ReservedQuota != expectedReserved {
			return nil, errors.New("invalid durable subscription reservation")
		}
	case BillingSourceFreeModel:
		if record.TrustQuotaBypassed || record.RequestedQuota != 0 || record.ReservedQuota != 0 ||
			record.TokenReserved != 0 || record.SubscriptionID != 0 || record.UsageEpoch != 0 {
			return nil, errors.New("invalid durable free-model reservation")
		}
	default:
		return nil, fmt.Errorf("unsupported durable funding source %q", record.FundingSource)
	}
	switch record.Status {
	case model.RelayQuotaReservationStatusHeld:
		if record.Operation != "" || record.DispatchedAt != 0 {
			return nil, errors.New("invalid durable undispatched reservation")
		}
	case model.RelayQuotaReservationStatusDispatched:
		if record.Operation != model.RelayQuotaReservationOperationSettle ||
			record.ActualQuota != relayQuotaDispatchFallbackQuota(record) || record.DispatchedAt <= 0 {
			return nil, errors.New("invalid durable dispatched reservation")
		}
	case model.RelayQuotaReservationStatusPendingSettlement, model.RelayQuotaReservationStatusSettled:
		if record.Operation != model.RelayQuotaReservationOperationSettle {
			return nil, errors.New("invalid durable settlement operation")
		}
		if err := validateQuotaAmount(record.ActualQuota); err != nil {
			return nil, fmt.Errorf("stored actual quota: %w", err)
		}
		if record.ChannelID < 0 {
			return nil, errors.New("invalid durable settlement channel")
		}
	case model.RelayQuotaReservationStatusPendingRefund, model.RelayQuotaReservationStatusRefunded:
		if record.Operation != model.RelayQuotaReservationOperationRefund || record.ActualQuota != 0 {
			return nil, errors.New("invalid durable refund operation")
		}
	case model.RelayQuotaReservationStatusReversed:
		if record.Operation != model.RelayQuotaReservationOperationReverse {
			return nil, errors.New("invalid durable reversal operation")
		}
		if err := validateQuotaAmount(record.ActualQuota); err != nil {
			return nil, fmt.Errorf("stored reversed quota: %w", err)
		}
		if record.ChannelID < 0 {
			return nil, errors.New("invalid durable reversal channel")
		}
	case model.RelayQuotaReservationStatusManualReview:
		// Manual-review rows deliberately preserve the operation and accounting
		// fields that failed validation so an operator can diagnose them.
	default:
		return nil, fmt.Errorf("unsupported durable reservation status %q", record.Status)
	}
	funding := &FundingSession{
		userId: record.UserID, requestId: record.ReservationID,
		source: record.FundingSource, reserved: record.ReservedQuota,
		subscriptionId: record.SubscriptionID,
		usageEpoch:     record.UsageEpoch,
	}
	return &RelayQuotaReservation{
		reservationId:      record.ReservationID,
		funding:            funding,
		tokenId:            record.TokenID,
		tokenReserved:      record.TokenReserved,
		tokenUnlimited:     record.TokenUnlimited,
		trustQuotaBypassed: record.TrustQuotaBypassed,
		channelId:          record.ChannelID,
		quota:              record.RequestedQuota,
		dispatched:         record.Status == model.RelayQuotaReservationStatusDispatched,
		settled: record.Status == model.RelayQuotaReservationStatusSettled ||
			record.Status == model.RelayQuotaReservationStatusReversed,
		refunded: record.Status == model.RelayQuotaReservationStatusRefunded,
	}, nil
}

func relayQuotaDispatchFallbackQuota(record *model.RelayQuotaReservationRecord) int {
	if record != nil && record.TrustQuotaBypassed {
		return record.RequestedQuota
	}
	if record == nil {
		return 0
	}
	return record.ReservedQuota
}
