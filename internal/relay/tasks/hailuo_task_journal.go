package tasks

import (
	"context"
	"errors"
	"fmt"
	"github.com/tokenrouter/tokenrouter/internal/platform/jsonutil"
	"github.com/tokenrouter/tokenrouter/internal/relay/providers/task/hailuo"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	maxHailuoRecoveryJournalBytes          = 64 * 1024
	hailuoRecoveryJournalDiskVersion       = 1
	hailuoRecoveryJournalWriteAttempts     = 3
	hailuoRecoveryJournalPromotionLimit    = 100
	maxHailuoRecoveryJournalPromotionLimit = 1000
)

type hailuoRecoveryJournalRecord struct {
	Family         string        `json:"family"`
	Platform       string        `json:"platform"`
	TaskID         string        `json:"task_id"`
	ReservationID  string        `json:"reservation_id"`
	UserID         int           `json:"user_id"`
	ChannelID      int           `json:"channel_id"`
	Action         hailuo.Action `json:"action"`
	ProviderTaskID string        `json:"provider_task_id"`
}

type hailuoRecoveryJournalDiskEnvelope struct {
	Version    int    `json:"version"`
	Action     string `json:"action"`
	Ciphertext string `json:"ciphertext"`
}

var hailuoRecoveryJournalPromotionCursor atomic.Uint64

func hailuoRecoveryJournalDirectory() string {
	if configured := strings.TrimSpace(os.Getenv("HAILUO_TASK_RECOVERY_DIR")); configured != "" {
		return configured
	}
	if videoDirectory := strings.TrimSpace(os.Getenv("VIDEO_TASK_RECOVERY_DIR")); videoDirectory != "" {
		return filepath.Join(videoDirectory, "hailuo-task-v1")
	}
	if sqlitePath := strings.TrimSpace(os.Getenv("SQLITE_PATH")); sqlitePath != "" {
		return filepath.Join(filepath.Dir(sqlitePath), ".tokenrouter-hailuo-recovery")
	}
	return ".tokenrouter-hailuo-recovery"
}

func hailuoRecoveryJournalPath(taskID string) (string, error) {
	if validateHailuoTaskPublicID(taskID) != nil || filepath.Base(taskID) != taskID {
		return "", errors.New("invalid Hailuo recovery task id")
	}
	directory, err := validatedHailuoRecoveryJournalDirectory()
	if err != nil {
		return "", err
	}
	return filepath.Join(directory, taskID+".json"), nil
}

func validatedHailuoRecoveryJournalDirectory() (string, error) {
	return validateRecoveryJournalDirectory(hailuoRecoveryJournalDirectory(), "Hailuo")
}

// persistAcceptedHailuoRecoveryJournal records the minimum accepted-provider
// identity without database I/O. This closes the outage window after the
// provider accepts a task but before the primary database can store its id.
func persistAcceptedHailuoRecoveryJournal(
	task *model.Task,
	reservationID, providerTaskID string,
	action hailuo.Action,
) error {
	if task == nil || task.Platform != hailuoTaskPlatform {
		return errors.New("invalid Hailuo recovery task")
	}
	record := hailuoRecoveryJournalRecord{
		Family: "hailuo", Platform: hailuoTaskPlatform, TaskID: task.TaskID,
		ReservationID: strings.TrimSpace(reservationID), UserID: task.UserId,
		ChannelID: task.ChannelId, Action: action,
		ProviderTaskID: strings.TrimSpace(providerTaskID),
	}
	return persistHailuoRecoveryJournal(record)
}

func persistHailuoRecoveryJournal(record hailuoRecoveryJournalRecord) error {
	if err := validateHailuoRecoveryJournalRecord(record); err != nil {
		return err
	}
	path, err := hailuoRecoveryJournalPath(record.TaskID)
	if err != nil {
		return err
	}
	plaintext, err := jsonutil.Marshal(record)
	if err != nil {
		return errors.New("encode Hailuo recovery journal")
	}
	ciphertext, err := asyncTaskEncryptBound(
		string(plaintext), hailuoRecoveryJournalBinding(record.TaskID, record.Action),
	)
	if err != nil {
		return errors.New("encrypt Hailuo recovery journal")
	}
	data, err := jsonutil.Marshal(hailuoRecoveryJournalDiskEnvelope{
		Version: hailuoRecoveryJournalDiskVersion, Action: string(record.Action), Ciphertext: ciphertext,
	})
	if err != nil || len(data) > maxHailuoRecoveryJournalBytes {
		return errors.New("encode Hailuo recovery journal envelope")
	}
	var lastErr error
	for attempt := 0; attempt < hailuoRecoveryJournalWriteAttempts; attempt++ {
		if err := persistVideoRecoveryJournalOnce(path, data); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if attempt < hailuoRecoveryJournalWriteAttempts-1 {
			time.Sleep(time.Duration(20*(attempt+1)) * time.Millisecond)
		}
	}
	return fmt.Errorf("persist Hailuo recovery journal: %w", lastErr)
}

func loadHailuoRecoveryJournal(taskID string) (hailuoRecoveryJournalRecord, error) {
	path, err := hailuoRecoveryJournalPath(taskID)
	if err != nil {
		return hailuoRecoveryJournalRecord{}, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return hailuoRecoveryJournalRecord{}, err
	}
	if !info.Mode().IsRegular() {
		return hailuoRecoveryJournalRecord{}, errors.New("Hailuo recovery journal is not a regular file")
	}
	file, err := openRecoveryJournalFile(path)
	if err != nil {
		return hailuoRecoveryJournalRecord{}, err
	}
	data, readErr := io.ReadAll(io.LimitReader(file, maxHailuoRecoveryJournalBytes+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil {
		return hailuoRecoveryJournalRecord{}, errors.Join(readErr, closeErr)
	}
	if len(data) > maxHailuoRecoveryJournalBytes {
		return hailuoRecoveryJournalRecord{}, errors.New("Hailuo recovery journal is too large")
	}
	var disk hailuoRecoveryJournalDiskEnvelope
	if err := strictHailuoJSON(data, &disk); err != nil || disk.Version != hailuoRecoveryJournalDiskVersion ||
		strings.TrimSpace(disk.Ciphertext) == "" {
		return hailuoRecoveryJournalRecord{}, errors.New("invalid Hailuo recovery journal envelope")
	}
	action, err := hailuo.ParseAction(disk.Action)
	if err != nil {
		return hailuoRecoveryJournalRecord{}, errors.New("invalid Hailuo recovery journal action")
	}
	plaintext, err := asyncTaskDecryptBound(
		disk.Ciphertext, hailuoRecoveryJournalBinding(taskID, action),
	)
	if err != nil {
		return hailuoRecoveryJournalRecord{}, errors.New("decrypt Hailuo recovery journal")
	}
	var record hailuoRecoveryJournalRecord
	if err := strictHailuoJSON([]byte(plaintext), &record); err != nil ||
		record.TaskID != taskID || record.Action != action {
		return hailuoRecoveryJournalRecord{}, errors.New("decode Hailuo recovery journal")
	}
	if err := validateHailuoRecoveryJournalRecord(record); err != nil {
		return hailuoRecoveryJournalRecord{}, err
	}
	return record, nil
}

func validateHailuoRecoveryJournalRecord(record hailuoRecoveryJournalRecord) error {
	if record.Family != "hailuo" || record.Platform != hailuoTaskPlatform {
		return errors.New("invalid Hailuo recovery provenance")
	}
	if !validHailuoAction(record.Action) {
		return errors.New("invalid Hailuo recovery action")
	}
	if _, err := hailuoRecoveryJournalPath(record.TaskID); err != nil {
		return err
	}
	if record.UserID <= 0 || record.ChannelID <= 0 ||
		!validHailuoRecoveryOpaqueID(record.ReservationID, 64) ||
		!validHailuoRecoveryOpaqueID(record.ProviderTaskID, hailuo.MaxProviderTaskIDBytes) {
		return errors.New("invalid Hailuo recovery identity")
	}
	return nil
}

func validHailuoRecoveryOpaqueID(value string, maxBytes int) bool {
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

func removeHailuoRecoveryJournal(taskID string) error {
	path, err := hailuoRecoveryJournalPath(taskID)
	if err != nil {
		return err
	}
	var lastErr error
	for attempt := 0; attempt < hailuoRecoveryJournalWriteAttempts; attempt++ {
		if err := removeVideoRecoveryJournalOnce(path); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if attempt < hailuoRecoveryJournalWriteAttempts-1 {
			time.Sleep(time.Duration(20*(attempt+1)) * time.Millisecond)
		}
	}
	return fmt.Errorf("remove Hailuo recovery journal: %w", lastErr)
}

// PromoteHailuoTaskRecoveryJournalsContext imports node-local accepted task ids
// before the cluster-wide reconciler runs. Journal evidence is never expired;
// it is removed only after the same provider identity is durable in the DB.
func PromoteHailuoTaskRecoveryJournalsContext(ctx context.Context) error {
	return promoteHailuoRecoveryJournalRecords(ctx, hailuoRecoveryJournalPromotionLimit)
}

func promoteHailuoRecoveryJournalRecords(ctx context.Context, limit int) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if limit <= 0 || limit > maxHailuoRecoveryJournalPromotionLimit {
		limit = hailuoRecoveryJournalPromotionLimit
	}
	directory, err := validatedHailuoRecoveryJournalDirectory()
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
		return fmt.Errorf("read Hailuo recovery journal directory: %w", err)
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
		if _, err := hailuoRecoveryJournalPath(taskID); err != nil {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			promotionErrors = append(promotionErrors, errors.New("inspect Hailuo recovery journal file"))
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
		start := int((hailuoRecoveryJournalPromotionCursor.Add(uint64(limit)) - uint64(limit)) % uint64(len(files)))
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
		record, err := loadHailuoRecoveryJournal(file.taskID)
		if err != nil {
			promotionErrors = append(promotionErrors,
				fmt.Errorf("promote Hailuo recovery %s: unreadable encrypted journal", file.taskID))
			continue
		}
		remove, err := promoteHailuoRecoveryJournalRecord(ctx, record)
		if err != nil {
			promotionErrors = append(promotionErrors,
				fmt.Errorf("promote Hailuo recovery %s: durable transition failed", file.taskID))
			continue
		}
		if remove {
			if err := removeHailuoRecoveryJournal(file.taskID); err != nil {
				promotionErrors = append(promotionErrors,
					fmt.Errorf("promote Hailuo recovery %s: cleanup failed", file.taskID))
			}
		}
	}
	return errors.Join(promotionErrors...)
}

func promoteHailuoRecoveryJournalRecord(ctx context.Context, record hailuoRecoveryJournalRecord) (bool, error) {
	if model.DB == nil {
		return false, errors.New("Hailuo recovery database is unavailable")
	}
	var operation model.TaskOperation
	if err := model.DB.WithContext(ctx).
		Where("task_id = ? AND reservation_id = ? AND platform = ?", record.TaskID,
			record.ReservationID, hailuoTaskPlatform).First(&operation).Error; err != nil {
		return false, err
	}
	if operation.UserID != record.UserID || operation.ChannelID != record.ChannelID {
		return false, errors.New("Hailuo recovery operation identity mismatch")
	}
	var tasks []model.Task
	if err := model.DB.WithContext(ctx).Where("task_id = ? AND platform = ?", record.TaskID,
		hailuoTaskPlatform).Limit(2).Find(&tasks).Error; err != nil {
		return false, err
	}
	if len(tasks) != 1 || !hailuoTaskOperationIdentityMatches(&tasks[0], &operation) ||
		tasks[0].UserId != record.UserID || tasks[0].ChannelId != record.ChannelID {
		return false, errors.New("Hailuo recovery task identity is unavailable or ambiguous")
	}
	properties, err := decodeHailuoTaskProperties(tasks[0].Properties)
	if err != nil || properties.Action != record.Action {
		return false, errors.New("Hailuo recovery action mismatch")
	}
	if hailuoRecoveryProviderIDIsDurable(record, &tasks[0], &operation) {
		return true, nil
	}
	if operation.LeaseOwner != "" {
		return false, nil
	}
	switch operation.State {
	case model.TaskOperationDispatching, model.TaskOperationSubmitted,
		model.TaskOperationUnknown, model.TaskOperationManualReview:
		if err := hailuoRecoveryProviderIDsDoNotConflict(record, &tasks[0], &operation); err != nil {
			return false, err
		}
		if err := persistAcceptedHailuoFallback(&tasks[0], record.ReservationID, record.ProviderTaskID); err != nil {
			return false, err
		}
		var freshTask model.Task
		var freshOperation model.TaskOperation
		if err := model.DB.WithContext(ctx).Where("id = ?", tasks[0].ID).First(&freshTask).Error; err != nil {
			return false, err
		}
		if err := model.DB.WithContext(ctx).Where("id = ?", operation.ID).First(&freshOperation).Error; err != nil {
			return false, err
		}
		return hailuoRecoveryProviderIDIsDurable(record, &freshTask, &freshOperation), nil
	case model.TaskOperationTerminal, model.TaskOperationReversed:
		return false, errors.New("Hailuo recovery provider identity is missing from terminal state")
	case model.TaskOperationPrepared, model.TaskOperationRefunded:
		return false, errors.New("Hailuo recovery journal conflicts with a non-dispatched operation")
	default:
		return false, errors.New("Hailuo recovery operation state is not promotable")
	}
}

func hailuoRecoveryProviderIDsDoNotConflict(
	record hailuoRecoveryJournalRecord,
	task *model.Task,
	operation *model.TaskOperation,
) error {
	privateData, err := decodeHailuoTaskPrivateData(task.PrivateData)
	if err != nil || privateData.RelayReservationID != record.ReservationID {
		return errors.New("Hailuo recovery private identity mismatch")
	}
	binding := hailuoProviderTaskBinding(
		record.TaskID, record.ReservationID, record.UserID, record.ChannelID,
	)
	for _, encoded := range []string{operation.EncryptedProviderTaskID, privateData.EncryptedProviderTaskID} {
		if encoded == "" {
			continue
		}
		providerID, err := asyncTaskDecryptBound(encoded, binding)
		if err != nil || providerID != record.ProviderTaskID {
			return errors.New("Hailuo recovery provider identity conflicts with durable state")
		}
	}
	return nil
}

func hailuoRecoveryProviderIDIsDurable(
	record hailuoRecoveryJournalRecord,
	task *model.Task,
	operation *model.TaskOperation,
) bool {
	if task == nil || operation == nil || operation.EncryptedProviderTaskID == "" ||
		!hailuoTaskOperationIdentityMatches(task, operation) {
		return false
	}
	privateData, err := decodeHailuoTaskPrivateData(task.PrivateData)
	if err != nil || privateData.EncryptedProviderTaskID == "" ||
		privateData.SettlementPending != operation.SettlementPending {
		return false
	}
	binding := hailuoProviderTaskBinding(
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
