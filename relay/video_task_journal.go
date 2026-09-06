package relay

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/relay/channel/sora"
	"github.com/tokenrouter/tokenrouter/service"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	maxVideoRecoveryJournalBytes          = 64 * 1024
	maxVideoRecoveryProviderTaskIDBytes   = 191
	videoRecoveryJournalDiskVersion       = 1
	videoRecoveryJournalWriteAttempts     = 3
	videoRecoveryJournalPromotionLimit    = 100
	maxVideoRecoveryJournalPromotionLimit = 1000
)

type videoRecoveryJournalRecord struct {
	TaskID         string `json:"task_id"`
	ReservationID  string `json:"reservation_id"`
	UserID         int    `json:"user_id"`
	ChannelID      int    `json:"channel_id"`
	ProviderTaskID string `json:"provider_task_id"`
}

type videoRecoveryJournalDiskEnvelope struct {
	Version    int    `json:"version"`
	Ciphertext string `json:"ciphertext"`
}

var videoRecoveryJournalPromotionCursor atomic.Uint64

// RegisterVideoTaskRecoveryJournalPromoter wires the node-local journal into
// the service scheduler without creating a service-to-relay import cycle. The
// caller should invoke it while registering the async video reconciler.
func RegisterVideoTaskRecoveryJournalPromoter() {
	service.RegisterAsyncTaskPromoter(PromoteVideoTaskRecoveryJournalsContext)
}

func videoRecoveryJournalDirectory() string {
	if configured := strings.TrimSpace(os.Getenv("VIDEO_TASK_RECOVERY_DIR")); configured != "" {
		return configured
	}
	if sqlitePath := strings.TrimSpace(os.Getenv("SQLITE_PATH")); sqlitePath != "" {
		return filepath.Join(filepath.Dir(sqlitePath), ".tokenrouter-video-recovery")
	}
	return ".tokenrouter-video-recovery"
}

func validatedVideoRecoveryJournalDirectory() (string, error) {
	return validateRecoveryJournalDirectory(videoRecoveryJournalDirectory(), "video")
}

func videoRecoveryJournalPath(taskID string) (string, error) {
	if err := validateVideoTaskPublicID(taskID); err != nil || filepath.Base(taskID) != taskID {
		return "", errors.New("invalid video recovery task ID")
	}
	directory, err := validatedVideoRecoveryJournalDirectory()
	if err != nil {
		return "", err
	}
	return filepath.Join(directory, taskID+".json"), nil
}

// persistAcceptedVideoRecoveryJournal records the minimum identity needed to
// recover a provider acceptance if the primary database becomes unavailable
// immediately after dispatch. It performs no database I/O by design.
func persistAcceptedVideoRecoveryJournal(
	task *model.Task,
	reservationID, providerTaskID string,
) error {
	if task == nil || !isVideoTaskPlatform(task) {
		return errors.New("invalid video recovery task")
	}
	record := videoRecoveryJournalRecord{
		TaskID: task.TaskID, ReservationID: strings.TrimSpace(reservationID),
		UserID: task.UserId, ChannelID: task.ChannelId,
		ProviderTaskID: strings.TrimSpace(providerTaskID),
	}
	return persistVideoRecoveryJournal(record)
}

func persistVideoRecoveryJournal(record videoRecoveryJournalRecord) error {
	if err := validateVideoRecoveryJournalRecord(record); err != nil {
		return err
	}
	path, err := videoRecoveryJournalPath(record.TaskID)
	if err != nil {
		return err
	}
	plaintext, err := common.Marshal(record)
	if err != nil {
		return err
	}
	ciphertext, err := asyncTaskEncryptBound(
		string(plaintext), videoRecoveryJournalBinding(record.TaskID),
	)
	if err != nil {
		return errors.New("encrypt video recovery journal")
	}
	data, err := common.Marshal(videoRecoveryJournalDiskEnvelope{
		Version: videoRecoveryJournalDiskVersion, Ciphertext: ciphertext,
	})
	if err != nil {
		return err
	}
	if len(data) > maxVideoRecoveryJournalBytes {
		return errors.New("video recovery journal is too large")
	}
	var lastErr error
	for attempt := 0; attempt < videoRecoveryJournalWriteAttempts; attempt++ {
		if err := persistVideoRecoveryJournalOnce(path, data); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if attempt < videoRecoveryJournalWriteAttempts-1 {
			time.Sleep(time.Duration(20*(attempt+1)) * time.Millisecond)
		}
	}
	return fmt.Errorf("persist video recovery journal: %w", lastErr)
}

func persistVideoRecoveryJournalOnce(path string, data []byte) (returnErr error) {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".video-task-recovery-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	closed := false
	renamed := false
	defer func() {
		if !closed {
			if closeErr := temporary.Close(); closeErr != nil {
				returnErr = errors.Join(returnErr, fmt.Errorf("close video recovery temporary file: %w", closeErr))
			}
		}
		if !renamed {
			if removeErr := os.Remove(temporaryPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
				returnErr = errors.Join(returnErr, fmt.Errorf("remove video recovery temporary file: %w", removeErr))
			}
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		closed = true
		return err
	}
	closed = true
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	renamed = true
	return syncVideoRecoveryJournalDirectory(directory)
}

func loadVideoRecoveryJournal(taskID string) (videoRecoveryJournalRecord, error) {
	path, err := videoRecoveryJournalPath(taskID)
	if err != nil {
		return videoRecoveryJournalRecord{}, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return videoRecoveryJournalRecord{}, err
	}
	if !info.Mode().IsRegular() {
		return videoRecoveryJournalRecord{}, errors.New("video recovery journal is not a regular file")
	}
	file, err := openRecoveryJournalFile(path)
	if err != nil {
		return videoRecoveryJournalRecord{}, err
	}
	data, readErr := io.ReadAll(io.LimitReader(file, maxVideoRecoveryJournalBytes+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil {
		return videoRecoveryJournalRecord{}, errors.Join(readErr, closeErr)
	}
	if len(data) > maxVideoRecoveryJournalBytes {
		return videoRecoveryJournalRecord{}, errors.New("video recovery journal is too large")
	}
	var disk videoRecoveryJournalDiskEnvelope
	if err := common.Unmarshal(data, &disk); err != nil ||
		disk.Version != videoRecoveryJournalDiskVersion || strings.TrimSpace(disk.Ciphertext) == "" {
		return videoRecoveryJournalRecord{}, errors.New("invalid video recovery journal envelope")
	}
	plaintext, err := asyncTaskDecryptBound(
		disk.Ciphertext, videoRecoveryJournalBinding(taskID),
	)
	if err != nil {
		return videoRecoveryJournalRecord{}, errors.New("decrypt video recovery journal")
	}
	var record videoRecoveryJournalRecord
	if err := common.Unmarshal([]byte(plaintext), &record); err != nil {
		return videoRecoveryJournalRecord{}, errors.New("decode video recovery journal")
	}
	if record.TaskID != taskID {
		return videoRecoveryJournalRecord{}, errors.New("video recovery journal task binding mismatch")
	}
	if err := validateVideoRecoveryJournalRecord(record); err != nil {
		return videoRecoveryJournalRecord{}, err
	}
	return record, nil
}

func validateVideoRecoveryJournalRecord(record videoRecoveryJournalRecord) error {
	if _, err := videoRecoveryJournalPath(record.TaskID); err != nil {
		return err
	}
	if record.UserID <= 0 || record.ChannelID <= 0 {
		return errors.New("invalid video recovery owner or channel")
	}
	if !validVideoRecoveryOpaqueID(record.ReservationID, 64) {
		return errors.New("invalid video recovery reservation ID")
	}
	if !validVideoRecoveryOpaqueID(record.ProviderTaskID, maxVideoRecoveryProviderTaskIDBytes) {
		return errors.New("invalid video recovery provider task ID")
	}
	return nil
}

func validVideoRecoveryOpaqueID(value string, maxBytes int) bool {
	if value == "" || strings.TrimSpace(value) != value || len(value) > maxBytes || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if character < 0x21 || character == 0x7f {
			return false
		}
	}
	return true
}

func removeVideoRecoveryJournal(taskID string) error {
	path, err := videoRecoveryJournalPath(taskID)
	if err != nil {
		return err
	}
	var lastErr error
	for attempt := 0; attempt < videoRecoveryJournalWriteAttempts; attempt++ {
		if err := removeVideoRecoveryJournalOnce(path); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if attempt < videoRecoveryJournalWriteAttempts-1 {
			time.Sleep(time.Duration(20*(attempt+1)) * time.Millisecond)
		}
	}
	return fmt.Errorf("remove video recovery journal: %w", lastErr)
}

func removeVideoRecoveryJournalOnce(path string) error {
	directory := filepath.Dir(path)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return syncVideoRecoveryJournalDirectory(directory)
}

func videoRecoveryJournalNotFound(err error) bool {
	return errors.Is(err, os.ErrNotExist)
}

// PromoteVideoTaskRecoveryJournalsContext imports this node's accepted
// provider IDs before the cluster-wide async reconciler runs. Journals have no
// age-based deletion path: unresolved evidence survives database outages and
// is removed only after the database contains the same provider identity or a
// compatible terminal decision is already authoritative.
func PromoteVideoTaskRecoveryJournalsContext(ctx context.Context) error {
	return promoteVideoRecoveryJournalRecords(ctx, videoRecoveryJournalPromotionLimit)
}

func promoteVideoRecoveryJournalRecords(ctx context.Context, limit int) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if limit <= 0 || limit > maxVideoRecoveryJournalPromotionLimit {
		limit = videoRecoveryJournalPromotionLimit
	}
	directory, err := validatedVideoRecoveryJournalDirectory()
	if err != nil {
		return err
	}
	entries, err := readBoundedRecoveryJournalDirectory(
		ctx, directory, maximumRecoveryJournalDirectoryReadEntries,
	)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read video recovery journal directory: %w", err)
	}
	type journalFile struct {
		taskID string
		info   os.FileInfo
	}
	files := make([]journalFile, 0, len(entries))
	var promotionErrors []error
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || filepath.Ext(name) != ".json" {
			continue
		}
		taskID := strings.TrimSuffix(name, ".json")
		if _, pathErr := videoRecoveryJournalPath(taskID); pathErr != nil {
			continue
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			promotionErrors = append(promotionErrors, errors.New("inspect video recovery journal file"))
			continue
		}
		if info.Mode().IsRegular() {
			files = append(files, journalFile{taskID: taskID, info: info})
		}
	}
	sort.Slice(files, func(i, j int) bool {
		left, right := files[i].info.ModTime(), files[j].info.ModTime()
		if left.Equal(right) {
			return files[i].taskID < files[j].taskID
		}
		return left.Before(right)
	})
	if len(files) > limit {
		start := int((videoRecoveryJournalPromotionCursor.Add(uint64(limit)) - uint64(limit)) % uint64(len(files)))
		window := make([]journalFile, 0, limit)
		for offset := 0; offset < limit; offset++ {
			window = append(window, files[(start+offset)%len(files)])
		}
		files = window
	}
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			promotionErrors = append(promotionErrors, err)
			break
		}
		record, loadErr := loadVideoRecoveryJournal(file.taskID)
		if loadErr != nil {
			promotionErrors = append(promotionErrors,
				fmt.Errorf("promote video recovery %s: unreadable encrypted journal", file.taskID))
			continue
		}
		remove, promoteErr := promoteVideoRecoveryJournalRecord(ctx, record)
		if promoteErr != nil {
			promotionErrors = append(promotionErrors,
				fmt.Errorf("promote video recovery %s: durable transition failed", file.taskID))
			continue
		}
		if !remove {
			continue
		}
		if removeErr := removeVideoRecoveryJournal(file.taskID); removeErr != nil {
			promotionErrors = append(promotionErrors,
				fmt.Errorf("promote video recovery %s: cleanup failed", file.taskID))
		}
	}
	return errors.Join(promotionErrors...)
}

func promoteVideoRecoveryJournalRecord(
	ctx context.Context,
	record videoRecoveryJournalRecord,
) (bool, error) {
	if model.DB == nil {
		return false, errors.New("video recovery database is unavailable")
	}
	var operation model.TaskOperation
	if err := model.DB.WithContext(ctx).Where("task_id = ?", record.TaskID).First(&operation).Error; err != nil {
		return false, err
	}
	if operation.TaskID != record.TaskID || operation.ReservationID != record.ReservationID ||
		operation.UserID != record.UserID || operation.ChannelID != record.ChannelID ||
		!validVideoTaskPlatform(operation.Platform) {
		return false, errors.New("video recovery operation identity mismatch")
	}
	task, err := loadVideoRecoveryTask(ctx, record, &operation)
	if err != nil {
		return false, err
	}
	switch operation.State {
	case model.TaskOperationUnknown:
		if err := recoverSettledUnknownVideoJournal(ctx, record); err != nil {
			if videoRecoveryProviderIDIsDurable(record) {
				return true, nil
			}
			return false, err
		}
		return videoRecoveryProviderIDIsDurable(record), nil
	case model.TaskOperationReversed:
		if !videoRecoveryReversalIsDurable(record, &task, &operation) {
			return false, errors.New("video recovery reversal is not durably proven")
		}
		return true, nil
	case model.TaskOperationTerminal:
		if !videoRecoveryTerminalIsDurable(record, &task, &operation) {
			return false, errors.New("video recovery terminal state is not durably proven")
		}
		return true, nil
	case model.TaskOperationDispatching, model.TaskOperationSubmitted:
		if videoRecoveryProviderIDIsDurable(record) && operation.State == model.TaskOperationSubmitted {
			return true, nil
		}
		if operation.LeaseOwner != "" {
			return false, nil
		}
	case model.TaskOperationPrepared, model.TaskOperationManualReview:
		return false, nil
	case model.TaskOperationRefunded:
		return false, errors.New("accepted video recovery conflicts with terminal operation state")
	default:
		return false, errors.New("video recovery operation state is invalid")
	}

	if err := videoRecoveryProviderIDsDoNotConflict(record, &task, &operation); err != nil {
		return false, err
	}
	provider := &sora.Response{
		ID: record.ProviderTaskID, TaskID: record.ProviderTaskID,
		Object: "video", Status: "queued",
	}
	if err := persistAcceptedVideoFallback(
		&task, record.ReservationID, record.ProviderTaskID, provider,
	); err != nil {
		return false, err
	}
	return videoRecoveryProviderIDIsDurable(record), nil
}

func loadVideoRecoveryTask(
	ctx context.Context,
	record videoRecoveryJournalRecord,
	operation *model.TaskOperation,
) (model.Task, error) {
	var tasks []model.Task
	result := model.DB.WithContext(ctx).Where("task_id = ?", record.TaskID).Limit(2).Find(&tasks)
	if result.Error != nil {
		return model.Task{}, result.Error
	}
	if len(tasks) != 1 || !videoTaskOperationIdentityMatches(&tasks[0], operation, record.ReservationID) ||
		tasks[0].UserId != record.UserID || tasks[0].ChannelId != record.ChannelID {
		return model.Task{}, errors.New("video recovery task identity is unavailable or ambiguous")
	}
	return tasks[0], nil
}

// recoverSettledUnknownVideoJournal handles the race where another node
// conservatively settles Dispatching -> Unknown after the provider accepted
// the request but before this node could promote its journal. Accounting is
// already terminal, so this transaction changes only the task and its polling
// operation after proving the exact settled tuple and consume audit.
func recoverSettledUnknownVideoJournal(
	ctx context.Context,
	record videoRecoveryJournalRecord,
) error {
	encryptedProviderID, err := asyncTaskEncryptBound(
		record.ProviderTaskID,
		videoProviderTaskBinding(record.TaskID, record.ReservationID, record.UserID, record.ChannelID),
	)
	if err != nil {
		return errors.New("encrypt recovered video provider identity")
	}
	err = model.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var ledger model.RelayQuotaReservationRecord
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("reservation_id = ?", record.ReservationID).First(&ledger).Error; err != nil {
			return err
		}
		var tasks []model.Task
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("task_id = ?", record.TaskID).Limit(2).Find(&tasks).Error; err != nil {
			return err
		}
		if len(tasks) != 1 {
			return errors.New("video recovery task identity is unavailable or ambiguous")
		}
		task := tasks[0]
		var operation model.TaskOperation
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("task_id = ? AND reservation_id = ?", record.TaskID, record.ReservationID).
			First(&operation).Error; err != nil {
			return err
		}
		privateData, err := validateSettledUnknownVideoJournalTuple(
			tx, record, &ledger, &task, &operation,
		)
		if err != nil {
			return err
		}
		properties, err := decodeVideoTaskProperties(task.Properties)
		if err != nil {
			return errors.New("video recovery task properties are invalid")
		}
		now, err := model.DatabaseUnixTimestamp(tx)
		if err != nil {
			return err
		}
		privateData.EncryptedUpstreamTaskID = encryptedProviderID
		privateData.SettlementPending = false
		privateJSON, err := marshalVideoTaskPrivateData(privateData)
		if err != nil {
			return err
		}
		provider := &sora.Response{
			ID: record.ProviderTaskID, TaskID: record.ProviderTaskID,
			Object: "video", Status: "queued",
		}
		response := sanitizedVideoResponse(
			task.TaskID, properties, provider, model.TaskStatusSubmitted, "", task.CreatedAt, now,
		)
		data, err := common.Marshal(response)
		if err != nil || len(data) > videoTaskProviderPayloadMaxBytes {
			return errors.New("video provider response is too large")
		}
		taskResult := tx.Model(&model.Task{}).
			Where("id = ? AND task_id = ? AND platform = ? AND user_id = ? AND channel_id = ? AND quota = ?",
				task.ID, task.TaskID, task.Platform, task.UserId, task.ChannelId, task.Quota).
			Where("status = ? AND private_data = ? AND properties = ? AND progress = ? AND finish_time = ?",
				model.TaskStatusUnknown, task.PrivateData, task.Properties, task.Progress, task.FinishTime).
			Updates(map[string]any{
				"private_data": privateJSON, "data": string(data), "status": model.TaskStatusSubmitted,
				"fail_reason": "", "progress": videoTaskProgress(model.TaskStatusSubmitted, 0),
				"finish_time": 0, "updated_at": now,
			})
		if taskResult.Error != nil {
			return taskResult.Error
		}
		if taskResult.RowsAffected != 1 {
			return service.ErrRelayQuotaReservationBusy
		}
		opResult := tx.Model(&model.TaskOperation{}).
			Where("id = ? AND task_id = ? AND reservation_id = ? AND platform = ?",
				operation.ID, operation.TaskID, operation.ReservationID, operation.Platform).
			Where("user_id = ? AND channel_id = ? AND state = ? AND settlement_pending = ?",
				operation.UserID, operation.ChannelID, model.TaskOperationUnknown, false).
			Where("encrypted_provider_task_id = ? AND attempts = ? AND next_attempt_at = ?",
				"", operation.Attempts, int64(0)).
			Where("lease_owner = ? AND lease_expires_at = ? AND last_error = ?",
				"", int64(0), operation.LastError).
			Where("created_at = ? AND completed_at = ?", operation.CreatedAt, operation.CompletedAt).
			Updates(map[string]any{
				"state": model.TaskOperationSubmitted, "settlement_pending": false,
				"encrypted_provider_task_id": encryptedProviderID,
				"next_attempt_at":            now, "completed_at": 0, "updated_at": now,
				"last_error": "", "lease_owner": "", "lease_expires_at": 0,
			})
		if opResult.Error != nil {
			return opResult.Error
		}
		if opResult.RowsAffected != 1 {
			return service.ErrRelayQuotaReservationBusy
		}
		return nil
	})
	if err != nil && videoRecoveryProviderIDIsDurable(record) {
		return nil
	}
	return err
}

func validateSettledUnknownVideoJournalTuple(
	tx *gorm.DB,
	record videoRecoveryJournalRecord,
	ledger *model.RelayQuotaReservationRecord,
	task *model.Task,
	operation *model.TaskOperation,
) (videoTaskPrivateData, error) {
	if tx == nil || ledger == nil || task == nil || operation == nil ||
		ledger.ReservationID != record.ReservationID || ledger.UserID != record.UserID ||
		ledger.ChannelID != record.ChannelID || ledger.Status != model.RelayQuotaReservationStatusSettled ||
		ledger.Operation != model.RelayQuotaReservationOperationSettle || ledger.ActualQuota != task.Quota ||
		ledger.RequestedQuota != task.Quota || ledger.CompletedAt <= 0 || ledger.NextAttemptAt != 0 ||
		ledger.LeaseOwner != "" || ledger.LeaseExpiresAt != 0 ||
		!videoTaskOperationIdentityMatches(task, operation, record.ReservationID) ||
		task.UserId != record.UserID || task.ChannelId != record.ChannelID ||
		task.Status != model.TaskStatusUnknown || task.Progress != "100%" || task.FinishTime <= 0 ||
		operation.State != model.TaskOperationUnknown || operation.SettlementPending ||
		operation.EncryptedProviderTaskID != "" || operation.NextAttemptAt != 0 ||
		operation.LeaseOwner != "" || operation.LeaseExpiresAt != 0 || operation.CompletedAt <= 0 {
		return videoTaskPrivateData{}, errors.New("video recovery settled unknown tuple is inconsistent")
	}
	privateData, err := decodeVideoTaskPrivateData(task.PrivateData)
	if err != nil || !videoRecoveryPrivateAccountingMatches(privateData, ledger) ||
		privateData.SettlementPending || privateData.EncryptedUpstreamTaskID != "" {
		return videoTaskPrivateData{}, errors.New("video recovery private accounting metadata is inconsistent")
	}
	var auditCount int64
	if err := tx.Model(&model.AuditLogOutbox{}).
		Where("event_id = ?", "video:"+record.ReservationID).Count(&auditCount).Error; err != nil {
		return videoTaskPrivateData{}, err
	}
	if auditCount != 1 {
		return videoTaskPrivateData{}, errors.New("video recovery consume audit is unavailable")
	}
	return privateData, nil
}

func videoRecoveryPrivateAccountingMatches(
	privateData videoTaskPrivateData,
	ledger *model.RelayQuotaReservationRecord,
) bool {
	return ledger != nil && privateData.RelayReservationID == ledger.ReservationID &&
		privateData.BillingSource == ledger.FundingSource &&
		privateData.SubscriptionID == ledger.SubscriptionID &&
		privateData.FundingUsageEpoch == ledger.UsageEpoch && privateData.TokenID == ledger.TokenID
}

func videoRecoveryProviderIDsDoNotConflict(
	record videoRecoveryJournalRecord,
	task *model.Task,
	operation *model.TaskOperation,
) error {
	privateData, err := decodeVideoTaskPrivateData(task.PrivateData)
	if err != nil || privateData.RelayReservationID != record.ReservationID {
		return errors.New("video recovery private task identity mismatch")
	}
	binding := videoProviderTaskBinding(
		record.TaskID, record.ReservationID, record.UserID, record.ChannelID,
	)
	for _, encrypted := range []string{
		operation.EncryptedProviderTaskID,
		privateData.EncryptedUpstreamTaskID,
	} {
		if encrypted == "" {
			continue
		}
		providerID, decryptErr := asyncTaskDecryptBound(encrypted, binding)
		if decryptErr != nil || providerID != record.ProviderTaskID {
			return errors.New("video recovery provider identity conflicts with durable state")
		}
	}
	return nil
}

func videoRecoveryProviderIDIsDurable(record videoRecoveryJournalRecord) bool {
	var operation model.TaskOperation
	if model.DB == nil || model.DB.Where("task_id = ? AND reservation_id = ?", record.TaskID, record.ReservationID).
		First(&operation).Error != nil || operation.EncryptedProviderTaskID == "" {
		return false
	}
	var tasks []model.Task
	if model.DB.Where("task_id = ?", record.TaskID).Limit(2).Find(&tasks).Error != nil || len(tasks) != 1 {
		return false
	}
	task := tasks[0]
	if !videoTaskOperationIdentityMatches(&task, &operation, record.ReservationID) ||
		task.UserId != record.UserID || task.ChannelId != record.ChannelID {
		return false
	}
	privateData, err := decodeVideoTaskPrivateData(task.PrivateData)
	if err != nil || privateData.RelayReservationID != record.ReservationID ||
		privateData.EncryptedUpstreamTaskID == "" ||
		privateData.SettlementPending != operation.SettlementPending {
		return false
	}
	binding := videoProviderTaskBinding(
		operation.TaskID, operation.ReservationID, operation.UserID, operation.ChannelID,
	)
	operationProviderID, err := asyncTaskDecryptBound(
		operation.EncryptedProviderTaskID,
		binding,
	)
	if err != nil || operationProviderID != record.ProviderTaskID {
		return false
	}
	privateProviderID, err := asyncTaskDecryptBound(privateData.EncryptedUpstreamTaskID, binding)
	return err == nil && privateProviderID == record.ProviderTaskID
}

func videoRecoveryTerminalIsDurable(
	record videoRecoveryJournalRecord,
	task *model.Task,
	operation *model.TaskOperation,
) bool {
	if task == nil || operation == nil || operation.State != model.TaskOperationTerminal ||
		operation.SettlementPending || operation.EncryptedProviderTaskID == "" ||
		operation.NextAttemptAt != 0 || operation.LeaseOwner != "" || operation.LeaseExpiresAt != 0 ||
		operation.CompletedAt <= 0 || task.Status != model.TaskStatusSuccess || task.Progress != "100%" ||
		task.FinishTime <= 0 || !videoRecoveryProviderIDIsDurable(record) {
		return false
	}
	var ledger model.RelayQuotaReservationRecord
	if model.DB.Where("reservation_id = ?", record.ReservationID).First(&ledger).Error != nil ||
		ledger.UserID != record.UserID || ledger.ChannelID != record.ChannelID ||
		ledger.Status != model.RelayQuotaReservationStatusSettled ||
		ledger.Operation != model.RelayQuotaReservationOperationSettle ||
		ledger.ActualQuota != task.Quota || ledger.CompletedAt <= 0 || ledger.NextAttemptAt != 0 ||
		ledger.LeaseOwner != "" || ledger.LeaseExpiresAt != 0 {
		return false
	}
	privateData, err := decodeVideoTaskPrivateData(task.PrivateData)
	return err == nil && videoRecoveryPrivateAccountingMatches(privateData, &ledger) &&
		videoAuditEventExists("video:"+record.ReservationID)
}

func videoRecoveryReversalIsDurable(
	record videoRecoveryJournalRecord,
	task *model.Task,
	operation *model.TaskOperation,
) bool {
	if task == nil || operation == nil || operation.State != model.TaskOperationReversed ||
		operation.SettlementPending || operation.EncryptedProviderTaskID != "" ||
		operation.NextAttemptAt != 0 || operation.LeaseOwner != "" || operation.LeaseExpiresAt != 0 ||
		operation.CompletedAt <= 0 || task.Status != model.TaskStatusFailure || task.Quota != 0 ||
		task.Progress != "100%" || task.FinishTime <= 0 ||
		!videoTaskOperationIdentityMatches(task, operation, record.ReservationID) {
		return false
	}
	var ledger model.RelayQuotaReservationRecord
	if model.DB.Where("reservation_id = ?", record.ReservationID).First(&ledger).Error != nil ||
		ledger.UserID != record.UserID || ledger.ChannelID != record.ChannelID ||
		ledger.Status != model.RelayQuotaReservationStatusReversed ||
		ledger.Operation != model.RelayQuotaReservationOperationReverse ||
		ledger.ActualQuota != ledger.RequestedQuota || ledger.CompletedAt <= 0 ||
		ledger.NextAttemptAt != 0 || ledger.LeaseOwner != "" || ledger.LeaseExpiresAt != 0 {
		return false
	}
	privateData, err := decodeVideoTaskPrivateData(task.PrivateData)
	if err != nil || !videoRecoveryPrivateAccountingMatches(privateData, &ledger) ||
		privateData.SettlementPending {
		return false
	}
	if privateData.EncryptedUpstreamTaskID != "" {
		providerID, err := asyncTaskDecryptBound(
			privateData.EncryptedUpstreamTaskID,
			videoProviderTaskBinding(record.TaskID, record.ReservationID, record.UserID, record.ChannelID),
		)
		if err != nil || providerID != record.ProviderTaskID {
			return false
		}
	}
	return videoAuditEventExists("video:"+record.ReservationID) &&
		videoAuditEventExists("video-refund:"+record.ReservationID)
}

func syncVideoRecoveryJournalDirectory(directory string) (returnErr error) {
	dir, err := os.Open(directory)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	defer func() {
		if closeErr := dir.Close(); closeErr != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("close video recovery directory: %w", closeErr))
		}
	}()
	return dir.Sync()
}
