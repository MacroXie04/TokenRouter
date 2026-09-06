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
	"unicode"
	"unicode/utf8"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/relay/channel/suno"
	"github.com/tokenrouter/tokenrouter/service"
)

const (
	maxSunoRecoveryJournalBytes          = 64 * 1024
	sunoRecoveryJournalDiskVersion       = 1
	sunoRecoveryJournalWriteAttempts     = 3
	sunoRecoveryJournalPromotionLimit    = 100
	maxSunoRecoveryJournalPromotionLimit = 1000
	sunoRecoveryOutcomeAccepted          = "accepted"
	sunoRecoveryOutcomeRejected          = "rejected"
)

type sunoRecoveryJournalRecord struct {
	Family         string      `json:"family"`
	Platform       string      `json:"platform"`
	Outcome        string      `json:"outcome"`
	TaskID         string      `json:"task_id"`
	ReservationID  string      `json:"reservation_id"`
	UserID         int         `json:"user_id"`
	ChannelID      int         `json:"channel_id"`
	Action         suno.Action `json:"action"`
	ProviderTaskID string      `json:"provider_task_id,omitempty"`
	FailReason     string      `json:"fail_reason,omitempty"`
}

type sunoRecoveryJournalDiskEnvelope struct {
	Version    int    `json:"version"`
	Action     string `json:"action"`
	Ciphertext string `json:"ciphertext"`
}

var sunoRecoveryJournalPromotionCursor atomic.Uint64

func sunoRecoveryJournalDirectory() string {
	if configured := strings.TrimSpace(os.Getenv("SUNO_TASK_RECOVERY_DIR")); configured != "" {
		return configured
	}
	if sqlitePath := strings.TrimSpace(os.Getenv("SQLITE_PATH")); sqlitePath != "" {
		return filepath.Join(filepath.Dir(sqlitePath), ".tokenrouter-suno-recovery")
	}
	return ".tokenrouter-suno-recovery"
}

func sunoRecoveryJournalPath(taskID string) (string, error) {
	if validateSunoTaskPublicID(taskID) != nil || filepath.Base(taskID) != taskID {
		return "", errors.New("invalid Suno recovery task id")
	}
	directory, err := validatedSunoRecoveryJournalDirectory()
	if err != nil {
		return "", err
	}
	return filepath.Join(directory, taskID+".json"), nil
}

func validatedSunoRecoveryJournalDirectory() (string, error) {
	return validateRecoveryJournalDirectory(sunoRecoveryJournalDirectory(), "Suno")
}

func sunoRecoveryJournalBinding(taskID string, action suno.Action) string {
	return "suno:recovery-journal:v1:" + taskID + ":" + string(action)
}

// persistAcceptedSunoRecoveryJournal records the minimum accepted-provider
// identity without database I/O. This closes the outage window after the
// provider accepts a task but before the primary database can store its id.
func persistAcceptedSunoRecoveryJournal(
	task *model.Task,
	reservationID, providerTaskID string,
	action suno.Action,
) error {
	if task == nil || task.Platform != sunoTaskPlatform {
		return errors.New("invalid Suno recovery task")
	}
	record := sunoRecoveryJournalRecord{
		Family: "suno", Platform: sunoTaskPlatform, Outcome: sunoRecoveryOutcomeAccepted,
		TaskID:        task.TaskID,
		ReservationID: strings.TrimSpace(reservationID), UserID: task.UserId,
		ChannelID: task.ChannelId, Action: action,
		ProviderTaskID: strings.TrimSpace(providerTaskID),
	}
	return persistSunoRecoveryJournal(record)
}

// persistRejectedSunoRecoveryJournal preserves authoritative evidence that no
// provider work was accepted. If the primary refund transaction is
// temporarily unavailable, the source node can safely finish it later rather
// than conservatively charging an outcome that is no longer ambiguous.
func persistRejectedSunoRecoveryJournal(
	task *model.Task,
	reservationID string,
	action suno.Action,
	reason string,
) error {
	if task == nil || task.Platform != sunoTaskPlatform {
		return errors.New("invalid rejected Suno recovery task")
	}
	record := sunoRecoveryJournalRecord{
		Family: "suno", Platform: sunoTaskPlatform, Outcome: sunoRecoveryOutcomeRejected,
		TaskID: task.TaskID, ReservationID: strings.TrimSpace(reservationID),
		UserID: task.UserId, ChannelID: task.ChannelId, Action: action,
		FailReason: boundedSunoFailReason(reason),
	}
	return persistSunoRecoveryJournal(record)
}

func persistSunoRecoveryJournal(record sunoRecoveryJournalRecord) error {
	if err := validateSunoRecoveryJournalRecord(record); err != nil {
		return err
	}
	path, err := sunoRecoveryJournalPath(record.TaskID)
	if err != nil {
		return err
	}
	plaintext, err := common.Marshal(record)
	if err != nil {
		return errors.New("encode Suno recovery journal")
	}
	ciphertext, err := asyncTaskEncryptBound(
		string(plaintext), sunoRecoveryJournalBinding(record.TaskID, record.Action),
	)
	if err != nil {
		return errors.New("encrypt Suno recovery journal")
	}
	data, err := common.Marshal(sunoRecoveryJournalDiskEnvelope{
		Version: sunoRecoveryJournalDiskVersion, Action: string(record.Action), Ciphertext: ciphertext,
	})
	if err != nil || len(data) > maxSunoRecoveryJournalBytes {
		return errors.New("encode Suno recovery journal envelope")
	}
	var lastErr error
	for attempt := 0; attempt < sunoRecoveryJournalWriteAttempts; attempt++ {
		if err := persistVideoRecoveryJournalOnce(path, data); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if attempt < sunoRecoveryJournalWriteAttempts-1 {
			time.Sleep(time.Duration(20*(attempt+1)) * time.Millisecond)
		}
	}
	return fmt.Errorf("persist Suno recovery journal: %w", lastErr)
}

func loadSunoRecoveryJournal(taskID string) (sunoRecoveryJournalRecord, error) {
	path, err := sunoRecoveryJournalPath(taskID)
	if err != nil {
		return sunoRecoveryJournalRecord{}, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return sunoRecoveryJournalRecord{}, err
	}
	if !info.Mode().IsRegular() {
		return sunoRecoveryJournalRecord{}, errors.New("Suno recovery journal is not a regular file")
	}
	file, err := openRecoveryJournalFile(path)
	if err != nil {
		return sunoRecoveryJournalRecord{}, err
	}
	data, readErr := io.ReadAll(io.LimitReader(file, maxSunoRecoveryJournalBytes+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil {
		return sunoRecoveryJournalRecord{}, errors.Join(readErr, closeErr)
	}
	if len(data) > maxSunoRecoveryJournalBytes {
		return sunoRecoveryJournalRecord{}, errors.New("Suno recovery journal is too large")
	}
	var disk sunoRecoveryJournalDiskEnvelope
	if err := strictSunoJSON(data, &disk); err != nil || disk.Version != sunoRecoveryJournalDiskVersion ||
		strings.TrimSpace(disk.Ciphertext) == "" {
		return sunoRecoveryJournalRecord{}, errors.New("invalid Suno recovery journal envelope")
	}
	action, err := suno.ParseAction(disk.Action)
	if err != nil {
		return sunoRecoveryJournalRecord{}, errors.New("invalid Suno recovery journal action")
	}
	plaintext, err := asyncTaskDecryptBound(
		disk.Ciphertext, sunoRecoveryJournalBinding(taskID, action),
	)
	if err != nil {
		return sunoRecoveryJournalRecord{}, errors.New("decrypt Suno recovery journal")
	}
	var record sunoRecoveryJournalRecord
	if err := strictSunoJSON([]byte(plaintext), &record); err != nil ||
		record.TaskID != taskID || record.Action != action {
		return sunoRecoveryJournalRecord{}, errors.New("decode Suno recovery journal")
	}
	if err := validateSunoRecoveryJournalRecord(record); err != nil {
		return sunoRecoveryJournalRecord{}, err
	}
	return record, nil
}

func validateSunoRecoveryJournalRecord(record sunoRecoveryJournalRecord) error {
	if record.Family != "suno" || record.Platform != sunoTaskPlatform {
		return errors.New("invalid Suno recovery provenance")
	}
	modelName, ok := suno.ModelForAction(record.Action)
	if !ok || modelName == "" {
		return errors.New("invalid Suno recovery action")
	}
	if _, err := sunoRecoveryJournalPath(record.TaskID); err != nil {
		return err
	}
	if record.UserID <= 0 || record.ChannelID <= 0 ||
		!validSunoRecoveryOpaqueID(record.ReservationID, 64) {
		return errors.New("invalid Suno recovery identity")
	}
	switch record.Outcome {
	case sunoRecoveryOutcomeAccepted:
		if record.FailReason != "" || !validSunoRecoveryOpaqueID(record.ProviderTaskID, suno.MaxTaskIDBytes) {
			return errors.New("invalid accepted Suno recovery outcome")
		}
	case sunoRecoveryOutcomeRejected:
		if record.ProviderTaskID != "" || record.FailReason == "" ||
			record.FailReason != boundedSunoFailReason(record.FailReason) {
			return errors.New("invalid rejected Suno recovery outcome")
		}
	default:
		return errors.New("invalid Suno recovery outcome")
	}
	return nil
}

func validSunoRecoveryOpaqueID(value string, maxBytes int) bool {
	if value == "" || strings.TrimSpace(value) != value || len(value) > maxBytes || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) || unicode.IsSpace(character) || character == '/' ||
			character == '\\' || character == '?' || character == '#' {
			return false
		}
	}
	return true
}

func removeSunoRecoveryJournal(taskID string) error {
	path, err := sunoRecoveryJournalPath(taskID)
	if err != nil {
		return err
	}
	var lastErr error
	for attempt := 0; attempt < sunoRecoveryJournalWriteAttempts; attempt++ {
		if err := removeVideoRecoveryJournalOnce(path); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if attempt < sunoRecoveryJournalWriteAttempts-1 {
			time.Sleep(time.Duration(20*(attempt+1)) * time.Millisecond)
		}
	}
	return fmt.Errorf("remove Suno recovery journal: %w", lastErr)
}

// PromoteSunoTaskRecoveryJournalsContext imports node-local accepted task ids
// before the cluster-wide reconciler runs. Journal evidence is never expired;
// it is removed only after the same provider identity is durable in the DB.
func PromoteSunoTaskRecoveryJournalsContext(ctx context.Context) error {
	return promoteSunoRecoveryJournalRecords(ctx, sunoRecoveryJournalPromotionLimit)
}

func promoteSunoRecoveryJournalRecords(ctx context.Context, limit int) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if limit <= 0 || limit > maxSunoRecoveryJournalPromotionLimit {
		limit = sunoRecoveryJournalPromotionLimit
	}
	directory, err := validatedSunoRecoveryJournalDirectory()
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
		return fmt.Errorf("read Suno recovery journal directory: %w", err)
	}
	type journalFile struct {
		taskID string
		info   os.FileInfo
	}
	files := make([]journalFile, 0, len(entries))
	var promotionErrors []error
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		taskID := strings.TrimSuffix(entry.Name(), ".json")
		if _, err := sunoRecoveryJournalPath(taskID); err != nil {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			promotionErrors = append(promotionErrors, errors.New("inspect Suno recovery journal file"))
			continue
		}
		if info.Mode().IsRegular() {
			files = append(files, journalFile{taskID: taskID, info: info})
		}
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].info.ModTime().Equal(files[j].info.ModTime()) {
			return files[i].taskID < files[j].taskID
		}
		return files[i].info.ModTime().Before(files[j].info.ModTime())
	})
	if len(files) > limit {
		start := int((sunoRecoveryJournalPromotionCursor.Add(uint64(limit)) - uint64(limit)) % uint64(len(files)))
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
		record, err := loadSunoRecoveryJournal(file.taskID)
		if err != nil {
			promotionErrors = append(promotionErrors,
				fmt.Errorf("promote Suno recovery %s: unreadable encrypted journal", file.taskID))
			continue
		}
		remove, err := promoteSunoRecoveryJournalRecord(ctx, record)
		if err != nil {
			promotionErrors = append(promotionErrors,
				fmt.Errorf("promote Suno recovery %s: durable transition failed", file.taskID))
			continue
		}
		if remove {
			if err := removeSunoRecoveryJournal(file.taskID); err != nil {
				promotionErrors = append(promotionErrors,
					fmt.Errorf("promote Suno recovery %s: cleanup failed", file.taskID))
			}
		}
	}
	return errors.Join(promotionErrors...)
}

func promoteSunoRecoveryJournalRecord(ctx context.Context, record sunoRecoveryJournalRecord) (bool, error) {
	if model.DB == nil {
		return false, errors.New("Suno recovery database is unavailable")
	}
	var operation model.TaskOperation
	if err := model.DB.WithContext(ctx).
		Where("task_id = ? AND reservation_id = ? AND platform = ?", record.TaskID,
			record.ReservationID, sunoTaskPlatform).First(&operation).Error; err != nil {
		return false, err
	}
	if operation.UserID != record.UserID || operation.ChannelID != record.ChannelID {
		return false, errors.New("Suno recovery operation identity mismatch")
	}
	var tasks []model.Task
	if err := model.DB.WithContext(ctx).Where("task_id = ? AND platform = ?", record.TaskID,
		sunoTaskPlatform).Limit(2).Find(&tasks).Error; err != nil {
		return false, err
	}
	if len(tasks) != 1 || !sunoTaskOperationIdentityMatches(&tasks[0], &operation) ||
		tasks[0].UserId != record.UserID || tasks[0].ChannelId != record.ChannelID {
		return false, errors.New("Suno recovery task identity is unavailable or ambiguous")
	}
	properties, err := decodeSunoTaskProperties(tasks[0].Properties)
	if err != nil || properties.Action != record.Action {
		return false, errors.New("Suno recovery action mismatch")
	}
	switch record.Outcome {
	case sunoRecoveryOutcomeAccepted:
		return promoteAcceptedSunoRecoveryJournalRecord(ctx, record, &tasks[0], &operation)
	case sunoRecoveryOutcomeRejected:
		return promoteRejectedSunoRecoveryJournalRecord(record, &tasks[0], &operation)
	default:
		return false, errors.New("Suno recovery outcome is not promotable")
	}
}

func promoteAcceptedSunoRecoveryJournalRecord(
	ctx context.Context,
	record sunoRecoveryJournalRecord,
	task *model.Task,
	operation *model.TaskOperation,
) (bool, error) {
	if sunoRecoveryProviderIDIsDurable(record, task, operation) {
		return true, nil
	}
	if operation.LeaseOwner != "" {
		return false, nil
	}
	switch operation.State {
	case model.TaskOperationDispatching, model.TaskOperationSubmitted,
		model.TaskOperationUnknown, model.TaskOperationManualReview:
		if err := sunoRecoveryProviderIDsDoNotConflict(record, task, operation); err != nil {
			return false, err
		}
		if err := persistAcceptedSunoFallback(task, record.ReservationID, record.ProviderTaskID); err != nil {
			return false, err
		}
		var freshTask model.Task
		var freshOperation model.TaskOperation
		if err := model.DB.WithContext(ctx).Where("id = ?", task.ID).First(&freshTask).Error; err != nil {
			return false, err
		}
		if err := model.DB.WithContext(ctx).Where("id = ?", operation.ID).First(&freshOperation).Error; err != nil {
			return false, err
		}
		return sunoRecoveryProviderIDIsDurable(record, &freshTask, &freshOperation), nil
	case model.TaskOperationTerminal, model.TaskOperationReversed:
		return false, errors.New("Suno recovery provider identity is missing from terminal state")
	case model.TaskOperationPrepared, model.TaskOperationRefunded:
		return false, errors.New("Suno recovery journal conflicts with a non-dispatched operation")
	default:
		return false, errors.New("Suno recovery operation state is not promotable")
	}
}

func promoteRejectedSunoRecoveryJournalRecord(
	record sunoRecoveryJournalRecord,
	task *model.Task,
	operation *model.TaskOperation,
) (bool, error) {
	if operation.State == model.TaskOperationRefunded && task.Status == model.TaskStatusFailure && task.Quota == 0 {
		return true, nil
	}
	if operation.State == model.TaskOperationReversed && task.Status == model.TaskStatusFailure && task.Quota == 0 {
		return true, nil
	}
	if operation.LeaseOwner != "" {
		return false, nil
	}
	privateData, err := decodeSunoTaskPrivateData(task.PrivateData)
	if err != nil || operation.EncryptedProviderTaskID != "" || privateData.EncryptedProviderTaskID != "" {
		return false, errors.New("rejected Suno recovery conflicts with an accepted provider identity")
	}
	switch operation.State {
	case model.TaskOperationPrepared, model.TaskOperationDispatching:
		reservation, err := service.RestoreRelayQuotaReservation(record.ReservationID)
		if err != nil {
			return false, err
		}
		if err := refundRejectedSunoTask(task, reservation, record.FailReason); err != nil {
			return false, err
		}
		return sunoOperationStateMatches(record.TaskID, record.ReservationID, model.TaskOperationRefunded), nil
	case model.TaskOperationUnknown, model.TaskOperationManualReview:
		if err := reverseRejectedSunoTask(task, operation, record.FailReason); err != nil {
			return false, err
		}
		return sunoOperationStateMatches(record.TaskID, record.ReservationID, model.TaskOperationReversed), nil
	default:
		return false, errors.New("rejected Suno recovery operation state is not refundable")
	}
}

func sunoRecoveryProviderIDsDoNotConflict(
	record sunoRecoveryJournalRecord,
	task *model.Task,
	operation *model.TaskOperation,
) error {
	privateData, err := decodeSunoTaskPrivateData(task.PrivateData)
	if err != nil || privateData.RelayReservationID != record.ReservationID {
		return errors.New("Suno recovery private identity mismatch")
	}
	binding := sunoProviderTaskBinding(
		record.TaskID, record.ReservationID, record.UserID, record.ChannelID,
	)
	for _, encoded := range []string{operation.EncryptedProviderTaskID, privateData.EncryptedProviderTaskID} {
		if encoded == "" {
			continue
		}
		providerID, err := asyncTaskDecryptBound(encoded, binding)
		if err != nil || providerID != record.ProviderTaskID {
			return errors.New("Suno recovery provider identity conflicts with durable state")
		}
	}
	return nil
}

func sunoRecoveryProviderIDIsDurable(
	record sunoRecoveryJournalRecord,
	task *model.Task,
	operation *model.TaskOperation,
) bool {
	if task == nil || operation == nil || operation.EncryptedProviderTaskID == "" ||
		!sunoTaskOperationIdentityMatches(task, operation) {
		return false
	}
	privateData, err := decodeSunoTaskPrivateData(task.PrivateData)
	if err != nil || privateData.EncryptedProviderTaskID == "" ||
		privateData.SettlementPending != operation.SettlementPending {
		return false
	}
	binding := sunoProviderTaskBinding(
		record.TaskID, record.ReservationID, record.UserID, record.ChannelID,
	)
	for _, encoded := range []string{operation.EncryptedProviderTaskID, privateData.EncryptedProviderTaskID} {
		providerID, err := asyncTaskDecryptBound(encoded, binding)
		if err != nil || providerID != record.ProviderTaskID {
			return false
		}
	}
	return true
}
