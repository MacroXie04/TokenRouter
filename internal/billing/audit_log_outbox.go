package billing

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"github.com/tokenrouter/tokenrouter/internal/platform/cryptoutil"
	"github.com/tokenrouter/tokenrouter/internal/platform/env"
	"github.com/tokenrouter/tokenrouter/internal/platform/jsonutil"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"math"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	defaultAuditLogOutboxBatchSize    = 100
	defaultAuditLogOutboxLeaseSeconds = int64(45)
	defaultAuditLogSinkTimeoutSeconds = 10
	maxAuditLogOutboxBackoffSeconds   = int64(5 * time.Minute / time.Second)
	maxAuditLogOutboxErrorBytes       = 255
	auditLogPayloadScrubBatchSize     = 500
	auditLogPayloadReceiptPrefix      = "sha256:"
)

var (
	errAuditLogSinkUnavailable    = errors.New("log database is nil")
	errAuditLogPrimaryUnavailable = errors.New("primary database is nil")
	// ErrAuditLogOutboxLeaseLost means a stale worker was fenced from changing
	// an outbox row after its lease expired or was taken over.
	ErrAuditLogOutboxLeaseLost = errors.New("audit log outbox lease lost")
)

type auditLogOutboxPayload struct {
	EventID string    `json:"event_id"`
	Log     model.Log `json:"log"`
}

// persistAuditLogWithOutbox first attempts the configured log sink. A sink
// failure is considered handled only after the complete record is durable in
// the primary-database outbox. Event IDs make ambiguous sink commits safe to
// reconcile on a later delivery pass.
func persistAuditLogWithOutbox(logEntry *model.Log) error {
	if logEntry == nil {
		return errors.New("audit log is nil")
	}
	entry := *logEntry
	entry.Id = 0
	if entry.AuditEventId == nil || *entry.AuditEventId == "" {
		eventID, err := cryptoutil.SecureRandomUUID()
		if err != nil {
			return err
		}
		entry.AuditEventId = &eventID
	}
	if len(*entry.AuditEventId) > 64 {
		return errors.New("audit event id is invalid")
	}
	if model.DB == nil {
		return safeAuditLogError("read database clock for audit write", errAuditLogPrimaryUnavailable)
	}
	createdAt, err := model.PrimaryDatabaseUnixTimestamp(context.Background())
	if err != nil {
		return safeAuditLogError("read database clock for audit write", err)
	}
	// The primary database is the one clock authority for both writes and
	// retention cutoffs. Ignore a caller/process timestamp so a skewed node
	// cannot make a new audit event immediately eligible for deletion.
	entry.CreatedAt = createdAt
	logEntry.CreatedAt = createdAt

	if err := insertAuditLogIntoSink(&entry); err == nil {
		return nil
	} else {
		// A driver can return an error after the remote commit succeeded. Verify
		// the immutable event id before adding a fallback row.
		if exists, verifyErr := auditLogExistsInSink(*entry.AuditEventId); verifyErr == nil && exists {
			return nil
		}
		if enqueueErr := enqueueAuditLog(&entry); enqueueErr != nil {
			return errors.Join(
				safeAuditLogError("write log sink", err),
				safeAuditLogError("enqueue primary audit fallback", enqueueErr),
			)
		}
		return nil
	}
}

func enqueueAuditLog(logEntry *model.Log) error {
	if model.DB == nil {
		return errAuditLogPrimaryUnavailable
	}
	return EnqueueAuditLogTx(model.DB, logEntry)
}

// EnqueueAuditLogTx stores an immutable audit event in the primary database
// using the caller's transaction. Accounting paths use this form so the
// charge and its eventual audit record have one commit boundary. Replaying an
// identical event is idempotent; reusing an event id for different content is
// rejected.
func EnqueueAuditLogTx(tx *gorm.DB, logEntry *model.Log) error {
	if tx == nil {
		return errAuditLogPrimaryUnavailable
	}
	if logEntry == nil || logEntry.AuditEventId == nil || *logEntry.AuditEventId == "" {
		return errors.New("audit event id is missing")
	}
	if len(*logEntry.AuditEventId) > 64 {
		return errors.New("audit event id is invalid")
	}
	entry := *logEntry
	entry.Id = 0
	payload, err := jsonutil.Marshal(&auditLogOutboxPayload{EventID: *entry.AuditEventId, Log: entry})
	if err != nil {
		return err
	}
	now, err := model.DatabaseUnixTimestamp(tx)
	if err != nil {
		return safeAuditLogError("read database clock for audit enqueue", err)
	}
	record := &model.AuditLogOutbox{
		EventID: *entry.AuditEventId, Payload: string(payload),
		Status:    model.AuditLogOutboxStatusPending,
		CreatedAt: now, UpdatedAt: now,
	}
	// Let the unique event id serialize concurrent first delivery attempts.
	// PostgreSQL aborts a transaction after a plain duplicate-key error, so a
	// check-then-insert cannot implement idempotency for callers without another
	// ledger row to lock (for example unmatched provider webhooks). A no-op
	// conflict followed by a read works in the caller's transaction on every
	// supported database and still detects event-id/content collisions.
	if err := tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "event_id"}},
		DoNothing: true,
	}).Create(record).Error; err != nil {
		return err
	}
	var existing model.AuditLogOutbox
	lookup := tx.Where("event_id = ?", record.EventID).Limit(1).Find(&existing)
	if lookup.Error != nil {
		return lookup.Error
	}
	if lookup.RowsAffected != 1 {
		return errors.New("audit event id was not persisted")
	}
	if existing.Payload == record.Payload ||
		(existing.Status == model.AuditLogOutboxStatusDelivered &&
			existing.Payload == auditLogPayloadReceipt(record.Payload)) {
		return nil
	}
	return errors.New("audit event id payload mismatch")
}

// insertAuditLogIntoSink writes a fresh event. Relational databases allocate
// their normal row id; ClickHouse receives a stable event-derived id so replay
// of an ambiguous write does not invent a second identity.
func insertAuditLogIntoSink(logEntry *model.Log) error {
	return insertAuditLogIntoSinkContext(context.Background(), logEntry)
}

func insertAuditLogIntoSinkContext(parent context.Context, logEntry *model.Log) error {
	if model.LOG_DB == nil {
		return errAuditLogSinkUnavailable
	}
	if parent == nil {
		return context.Canceled
	}
	ctx, cancel := context.WithTimeout(parent, auditLogSinkTimeout())
	defer cancel()
	entry := *logEntry
	if model.UsingClickHouseLog() {
		entry.Id = auditLogNumericID(entry.AuditEventId)
		return model.InsertClickHouseLogContext(ctx, &entry)
	}
	entry.Id = 0
	return model.LOG_DB.WithContext(ctx).Create(&entry).Error
}

func auditLogExistsInSink(eventID string) (bool, error) {
	return auditLogExistsInSinkContext(context.Background(), eventID)
}

func auditLogExistsInSinkContext(parent context.Context, eventID string) (bool, error) {
	if model.LOG_DB == nil {
		return false, errAuditLogSinkUnavailable
	}
	if parent == nil {
		return false, context.Canceled
	}
	ctx, cancel := context.WithTimeout(parent, auditLogSinkTimeout())
	defer cancel()
	var count int64
	if err := model.LOG_DB.WithContext(ctx).Table("logs").
		Where("audit_event_id = ?", eventID).Limit(1).Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

func auditLogNumericID(eventID *string) int {
	value := ""
	if eventID != nil {
		value = *eventID
	}
	sum := sha256.Sum256([]byte(value))
	n := binary.BigEndian.Uint64(sum[:8]) & math.MaxInt64
	if n == 0 {
		n = 1
	}
	return int(n)
}

// DeliverAuditLogOutbox delivers one bounded batch. It is invoked by a
// periodic job under the cluster-wide system-task lease; per-row leases remain
// necessary to fence stale workers and controlled/manual invocations.
func DeliverAuditLogOutbox() error {
	return DeliverAuditLogOutboxContext(context.Background())
}

// DeliverAuditLogOutboxContext is the cancellable periodic-delivery form. Its
// parent context reaches the primary database and each bounded sink request.
func DeliverAuditLogOutboxContext(ctx context.Context) error {
	if ctx == nil {
		return errors.New("audit outbox context is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	workerUUID, err := cryptoutil.SecureRandomUUID()
	if err != nil {
		return err
	}
	workerID := "audit-outbox-" + workerUUID
	deliveryErr := deliverAuditLogOutboxWithClockContext(ctx, auditLogOutboxBatchSize(), workerID, func() (int64, error) {
		return model.DatabaseUnixTimestamp(model.DB.WithContext(ctx))
	})
	_, _, scrubErr := scrubDeliveredAuditLogPayloadBatchContext(ctx, 0, auditLogPayloadScrubBatchSize)
	return errors.Join(deliveryErr, scrubErr)
}

// DeliverAuditLogOutboxEvent attempts prompt delivery of one already-durable
// event. A failure leaves the primary outbox row pending for the periodic
// worker, so callers may surface degraded immediacy without risking audit
// loss or creating a duplicate event.
func DeliverAuditLogOutboxEvent(eventID string) error {
	if model.DB == nil {
		return errAuditLogPrimaryUnavailable
	}
	if eventID == "" {
		return errors.New("audit event id is missing")
	}
	var queued model.AuditLogOutbox
	lookup := model.DB.Where("event_id = ?", eventID).First(&queued)
	if lookup.Error != nil {
		return lookup.Error
	}
	if queued.Status == model.AuditLogOutboxStatusDelivered {
		return nil
	}
	now, err := auditLogDatabaseClock()
	if err != nil {
		return safeAuditLogError("read database clock for immediate audit delivery", err)
	}
	workerUUID, err := cryptoutil.SecureRandomUUID()
	if err != nil {
		return err
	}
	record, claimed, err := claimAuditLogOutbox(queued.ID, "audit-immediate-"+workerUUID, now)
	if err != nil {
		return err
	}
	if !claimed {
		// Another worker owns this durable event. It is already safe and will be
		// delivered by that worker or retried after its lease.
		return nil
	}
	if err := deliverClaimedAuditLog(record); err != nil {
		failedAt, clockErr := auditLogDatabaseClock()
		if clockErr != nil {
			return errors.Join(err, safeAuditLogError("read database clock after audit delivery failure", clockErr))
		}
		return errors.Join(err, failClaimedAuditLog(record, failedAt, err))
	}
	completedAt, err := auditLogDatabaseClock()
	if err != nil {
		return safeAuditLogError("read database clock after audit delivery", err)
	}
	return completeClaimedAuditLog(record, completedAt)
}

func deliverAuditLogOutboxAt(now int64, batchSize int, workerID string) error {
	return deliverAuditLogOutboxWithClock(batchSize, workerID, func() (int64, error) { return now, nil })
}

type auditLogClock func() (int64, error)

func auditLogDatabaseClock() (int64, error) {
	return model.DatabaseUnixTimestamp(model.DB)
}

func deliverAuditLogOutboxWithClock(batchSize int, workerID string, clock auditLogClock) error {
	return deliverAuditLogOutboxWithClockContext(context.Background(), batchSize, workerID, clock)
}

func deliverAuditLogOutboxWithClockContext(ctx context.Context, batchSize int, workerID string, clock auditLogClock) error {
	if model.DB == nil {
		return errAuditLogPrimaryUnavailable
	}
	if ctx == nil {
		return errors.New("audit outbox context is nil")
	}
	if clock == nil {
		return errors.New("audit outbox clock is nil")
	}
	if batchSize <= 0 {
		batchSize = defaultAuditLogOutboxBatchSize
	}
	now, err := clock()
	if err != nil {
		return safeAuditLogError("read database clock for audit scan", err)
	}
	var ids []int64
	if err := model.DB.WithContext(ctx).Model(&model.AuditLogOutbox{}).
		Select("id").
		Where("status = ? AND next_attempt_at <= ? AND (lease_expires_at = 0 OR lease_expires_at <= ?)",
			model.AuditLogOutboxStatusPending, now, now).
		Order("id asc").Limit(batchSize).Scan(&ids).Error; err != nil {
		return safeAuditLogError("list pending audit events", err)
	}

	var deliveryErrors []error
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			deliveryErrors = append(deliveryErrors, err)
			break
		}
		claimAt, err := clock()
		if err != nil {
			deliveryErrors = append(deliveryErrors, safeAuditLogError("read database clock for audit claim", err))
			break
		}
		record, claimed, err := claimAuditLogOutboxContext(ctx, id, workerID, claimAt)
		if err != nil {
			deliveryErrors = append(deliveryErrors, safeAuditLogError("claim audit event", err))
			continue
		}
		if !claimed {
			continue
		}
		if err := deliverClaimedAuditLogContext(ctx, record); err != nil {
			failedAt, clockErr := clock()
			if clockErr != nil {
				deliveryErrors = append(deliveryErrors, errors.Join(
					safeAuditLogEventError(record.EventID, err),
					safeAuditLogError("read database clock after audit delivery failure", clockErr),
				))
				continue
			}
			failureErr := failClaimedAuditLogContext(ctx, record, failedAt, err)
			deliveryErrors = append(deliveryErrors,
				errors.Join(safeAuditLogEventError(record.EventID, err), failureErr))
			continue
		}
		completedAt, clockErr := clock()
		if clockErr != nil {
			deliveryErrors = append(deliveryErrors, safeAuditLogError("read database clock after audit delivery", clockErr))
			continue
		}
		if err := completeClaimedAuditLogContext(ctx, record, completedAt); err != nil {
			deliveryErrors = append(deliveryErrors, err)
		}
	}
	return errors.Join(deliveryErrors...)
}

func claimAuditLogOutbox(id int64, workerID string, now int64) (*model.AuditLogOutbox, bool, error) {
	return claimAuditLogOutboxContext(context.Background(), id, workerID, now)
}

func claimAuditLogOutboxContext(ctx context.Context, id int64, workerID string, now int64) (*model.AuditLogOutbox, bool, error) {
	if model.DB == nil {
		return nil, false, errAuditLogPrimaryUnavailable
	}
	if id <= 0 || workerID == "" {
		return nil, false, errors.New("invalid audit outbox claim")
	}
	leaseToken, err := cryptoutil.SecureRandomUUID()
	if err != nil {
		return nil, false, err
	}
	leaseUntil := now + auditLogOutboxLeaseSeconds()
	db := model.DB.WithContext(ctx)
	result := db.Model(&model.AuditLogOutbox{}).
		Where("id = ? AND status = ? AND next_attempt_at <= ? AND (lease_expires_at = 0 OR lease_expires_at <= ?)",
			id, model.AuditLogOutboxStatusPending, now, now).
		Updates(map[string]any{
			"lease_owner": workerID, "lease_token": leaseToken,
			"lease_expires_at": leaseUntil, "lease_version": gorm.Expr("lease_version + ?", 1),
			"attempts": gorm.Expr("attempts + ?", 1), "updated_at": now,
		})
	if result.Error != nil {
		return nil, false, result.Error
	}
	if result.RowsAffected != 1 {
		return nil, false, nil
	}
	var record model.AuditLogOutbox
	if err := db.Where("id = ?", id).First(&record).Error; err != nil {
		return nil, false, err
	}
	if record.LeaseToken != leaseToken || record.Status != model.AuditLogOutboxStatusPending {
		return &record, false, nil
	}
	return &record, true, nil
}

func deliverClaimedAuditLog(record *model.AuditLogOutbox) error {
	return deliverClaimedAuditLogContext(context.Background(), record)
}

func deliverClaimedAuditLogContext(ctx context.Context, record *model.AuditLogOutbox) error {
	if record == nil || record.EventID == "" || record.Status != model.AuditLogOutboxStatusPending {
		return errors.New("invalid claimed audit event")
	}
	var payload auditLogOutboxPayload
	if err := jsonutil.UnmarshalJsonStr(record.Payload, &payload); err != nil {
		return errors.New("invalid audit event payload")
	}
	if payload.EventID != record.EventID {
		return errors.New("audit event payload id mismatch")
	}
	logEntry := payload.Log
	logEntry.AuditEventId = &record.EventID
	// Keep the immutable payload byte-for-byte stable for idempotent enqueue
	// comparison, but materialize its log timestamp from the durable outbox
	// row's primary-database clock on every delivery attempt.
	logEntry.CreatedAt = record.CreatedAt
	if exists, err := auditLogExistsInSinkContext(ctx, record.EventID); err == nil && exists {
		return nil
	}
	if err := insertAuditLogIntoSinkContext(ctx, &logEntry); err != nil {
		// Resolve errors returned after a successful remote commit.
		if exists, verifyErr := auditLogExistsInSinkContext(ctx, record.EventID); verifyErr == nil && exists {
			return nil
		}
		return err
	}
	return nil
}

func completeClaimedAuditLog(record *model.AuditLogOutbox, now int64) error {
	return completeClaimedAuditLogContext(context.Background(), record, now)
}

func completeClaimedAuditLogContext(ctx context.Context, record *model.AuditLogOutbox, now int64) error {
	if record == nil {
		return errors.New("invalid claimed audit event")
	}
	result := model.DB.WithContext(ctx).Model(&model.AuditLogOutbox{}).
		Where("id = ? AND status = ? AND lease_token = ? AND lease_version = ? AND lease_expires_at > ?",
			record.ID, model.AuditLogOutboxStatusPending, record.LeaseToken, record.LeaseVersion, now).
		Updates(map[string]any{
			"status": model.AuditLogOutboxStatusDelivered, "delivered_at": now,
			"next_attempt_at": 0, "lease_owner": "", "lease_token": "",
			"lease_expires_at": 0, "last_error": "", "updated_at": now,
			"payload": auditLogPayloadReceipt(record.Payload), "payload_scrubbed": true,
		})
	if result.Error != nil {
		return safeAuditLogError("complete audit event", result.Error)
	}
	if result.RowsAffected != 1 {
		return ErrAuditLogOutboxLeaseLost
	}
	return nil
}

// ScrubDeliveredAuditLogPayloads replaces legacy delivered outbox payloads
// with content-free SHA-256 receipts. Pending rows retain the full record only
// while it is required for delivery. Startup runs the full scrub before
// serving, and each periodic delivery lease also scrubs one bounded batch so
// mixed-version deployments cannot keep adding indefinite copies of log
// content after startup.
func ScrubDeliveredAuditLogPayloads() error {
	return scrubDeliveredAuditLogPayloadsContext(context.Background())
}

func scrubDeliveredAuditLogPayloadsContext(ctx context.Context) error {
	var afterID int64
	for {
		nextAfterID, count, err := scrubDeliveredAuditLogPayloadBatchContext(
			ctx, afterID, auditLogPayloadScrubBatchSize,
		)
		if err != nil {
			return err
		}
		if count < auditLogPayloadScrubBatchSize {
			return nil
		}
		afterID = nextAfterID
	}
}

func scrubDeliveredAuditLogPayloadBatchContext(ctx context.Context, afterID int64, batchSize int) (int64, int, error) {
	if ctx == nil {
		return afterID, 0, errors.New("audit outbox scrub context is nil")
	}
	if err := ctx.Err(); err != nil {
		return afterID, 0, err
	}
	if model.DB == nil {
		return afterID, 0, errAuditLogPrimaryUnavailable
	}
	if batchSize <= 0 || batchSize > auditLogPayloadScrubBatchSize {
		batchSize = auditLogPayloadScrubBatchSize
	}
	var records []model.AuditLogOutbox
	if err := model.DB.WithContext(ctx).
		Select("id", "payload").
		Where("status = ? AND payload_scrubbed = ? AND id > ?",
			model.AuditLogOutboxStatusDelivered, false, afterID).
		Order("id asc").Limit(batchSize).
		Find(&records).Error; err != nil {
		return afterID, 0, safeAuditLogError("list delivered audit receipts", err)
	}
	for i := range records {
		record := &records[i]
		afterID = record.ID
		receipt := record.Payload
		if !isAuditLogPayloadReceipt(receipt) {
			receipt = auditLogPayloadReceipt(record.Payload)
		}
		result := model.DB.WithContext(ctx).Model(&model.AuditLogOutbox{}).
			Where("id = ? AND status = ? AND payload_scrubbed = ? AND payload = ?", record.ID,
				model.AuditLogOutboxStatusDelivered, false, record.Payload).
			Updates(map[string]any{"payload": receipt, "payload_scrubbed": true})
		if result.Error != nil {
			return afterID, len(records), safeAuditLogError("scrub delivered audit payload", result.Error)
		}
	}
	return afterID, len(records), nil
}

func auditLogPayloadReceipt(payload string) string {
	return auditLogPayloadReceiptPrefix + cryptoutil.SHA256Hex(payload)
}

func isAuditLogPayloadReceipt(payload string) bool {
	if len(payload) != len(auditLogPayloadReceiptPrefix)+sha256.Size*2 ||
		!strings.HasPrefix(payload, auditLogPayloadReceiptPrefix) {
		return false
	}
	for _, character := range payload[len(auditLogPayloadReceiptPrefix):] {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func failClaimedAuditLog(record *model.AuditLogOutbox, now int64, deliveryErr error) error {
	return failClaimedAuditLogContext(context.Background(), record, now, deliveryErr)
}

func failClaimedAuditLogContext(ctx context.Context, record *model.AuditLogOutbox, now int64, deliveryErr error) error {
	message := boundedAuditLogError(deliveryErr)
	nextAttemptAt := now + auditLogOutboxBackoffSeconds(record.Attempts)
	result := model.DB.WithContext(ctx).Model(&model.AuditLogOutbox{}).
		Where("id = ? AND status = ? AND lease_token = ? AND lease_version = ? AND lease_expires_at > ?",
			record.ID, model.AuditLogOutboxStatusPending, record.LeaseToken, record.LeaseVersion, now).
		Updates(map[string]any{
			"next_attempt_at": nextAttemptAt, "lease_owner": "", "lease_token": "",
			"lease_expires_at": 0, "last_error": message, "updated_at": now,
		})
	if result.Error != nil {
		return safeAuditLogError("reschedule audit event", result.Error)
	}
	if result.RowsAffected != 1 {
		return ErrAuditLogOutboxLeaseLost
	}
	return nil
}

func auditLogOutboxBackoffSeconds(attempt int) int64 {
	if attempt < 1 {
		return 1
	}
	shift := attempt - 1
	if shift > 8 {
		shift = 8
	}
	seconds := int64(1) << shift
	if seconds > maxAuditLogOutboxBackoffSeconds {
		return maxAuditLogOutboxBackoffSeconds
	}
	return seconds
}

func auditLogOutboxBatchSize() int {
	size := env.GetEnvInt("AUDIT_LOG_OUTBOX_BATCH_SIZE", defaultAuditLogOutboxBatchSize)
	if size < 1 || size > 1000 {
		return defaultAuditLogOutboxBatchSize
	}
	return size
}

func auditLogOutboxLeaseSeconds() int64 {
	seconds := int64(env.GetEnvInt("AUDIT_LOG_OUTBOX_LEASE_SECONDS", int(defaultAuditLogOutboxLeaseSeconds)))
	if seconds < 1 || seconds > int64(10*time.Minute/time.Second) {
		seconds = defaultAuditLogOutboxLeaseSeconds
	}
	// One delivery can perform an existence check, an insert, and an
	// ambiguous-commit verification. Keep the row lease valid through all
	// three bounded sink operations so an on-time worker can finish its CAS.
	minimum := int64(auditLogSinkTimeout()/time.Second)*3 + 5
	if seconds < minimum {
		seconds = minimum
	}
	return seconds
}

func auditLogSinkTimeout() time.Duration {
	seconds := env.GetEnvInt("AUDIT_LOG_SINK_TIMEOUT_SECONDS", defaultAuditLogSinkTimeoutSeconds)
	if seconds < 1 || seconds > 60 {
		seconds = defaultAuditLogSinkTimeoutSeconds
	}
	return time.Duration(seconds) * time.Second
}

func safeAuditLogEventError(eventID string, err error) error {
	return fmt.Errorf("deliver audit event %s: %s", eventID, boundedAuditLogError(err))
}

func safeAuditLogError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %s", operation, boundedAuditLogError(err))
}

// boundedAuditLogError deliberately persists only a failure class. Database
// errors can include bound values, so their raw text must not be copied into
// application logs or the primary outbox.
func boundedAuditLogError(err error) string {
	if err == nil {
		return ""
	}
	var message string
	switch {
	case errors.Is(err, errAuditLogSinkUnavailable):
		message = errAuditLogSinkUnavailable.Error()
	case errors.Is(err, errAuditLogPrimaryUnavailable):
		message = errAuditLogPrimaryUnavailable.Error()
	case errors.Is(err, context.DeadlineExceeded):
		message = "operation timed out"
	case errors.Is(err, context.Canceled):
		message = "operation canceled"
	default:
		message = fmt.Sprintf("storage failure (%T)", err)
	}
	message = strings.ToValidUTF8(message, "�")
	if len(message) <= maxAuditLogOutboxErrorBytes {
		return message
	}
	const suffix = "... [truncated]"
	cut := maxAuditLogOutboxErrorBytes - len(suffix)
	for cut > 0 && !utf8.RuneStart(message[cut]) {
		cut--
	}
	return message[:cut] + suffix
}
