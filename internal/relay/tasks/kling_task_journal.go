package tasks

import (
	"context"
	"errors"
	"fmt"
	"github.com/tokenrouter/tokenrouter/internal/platform/jsonutil"
	"github.com/tokenrouter/tokenrouter/internal/relay/providers/kling"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"unicode/utf8"
)

const (
	maxKlingRecoveryJournalBytes          = 64 * 1024
	klingRecoveryJournalDiskVersion       = 1
	klingRecoveryJournalPromotionLimit    = 100
	maxKlingRecoveryJournalPromotionLimit = 1000
)

type klingRecoveryJournalRecord struct {
	Family         string       `json:"family"`
	Platform       string       `json:"platform"`
	TaskID         string       `json:"task_id"`
	ReservationID  string       `json:"reservation_id"`
	UserID         int          `json:"user_id"`
	ChannelID      int          `json:"channel_id"`
	Action         kling.Action `json:"action"`
	ProviderTaskID string       `json:"provider_task_id"`
}

type klingRecoveryJournalDiskEnvelope struct {
	Version    int    `json:"version"`
	Action     string `json:"action"`
	Ciphertext string `json:"ciphertext"`
}

var klingRecoveryJournalPromotionCursor atomic.Uint64

func klingRecoveryJournalDirectory() string {
	if configured := strings.TrimSpace(os.Getenv("KLING_TASK_RECOVERY_DIR")); configured != "" {
		return configured
	}
	if shared := strings.TrimSpace(os.Getenv("VIDEO_TASK_RECOVERY_DIR")); shared != "" {
		return filepath.Join(shared, "kling-video-v1")
	}
	if sqlitePath := strings.TrimSpace(os.Getenv("SQLITE_PATH")); sqlitePath != "" {
		return filepath.Join(filepath.Dir(sqlitePath), ".tokenrouter-kling-recovery")
	}
	return ".tokenrouter-kling-recovery"
}

func validatedKlingRecoveryJournalDirectory() (string, error) {
	return validateRecoveryJournalDirectory(klingRecoveryJournalDirectory(), "Kling")
}

func klingRecoveryJournalPath(taskID string) (string, error) {
	if err := validateKlingTaskPublicID(taskID); err != nil || filepath.Base(taskID) != taskID {
		return "", errors.New("invalid Kling recovery task ID")
	}
	directory, err := validatedKlingRecoveryJournalDirectory()
	if err != nil {
		return "", err
	}
	return filepath.Join(directory, taskID+".json"), nil
}

func persistAcceptedKlingRecoveryJournal(
	task *model.Task,
	reservationID, providerTaskID string,
	action kling.Action,
) error {
	if task == nil || task.Platform != klingTaskPlatform || task.TaskID == "" || task.UserId <= 0 || task.ChannelId <= 0 {
		return errors.New("invalid Kling recovery task")
	}
	record := klingRecoveryJournalRecord{
		Family: "kling", Platform: klingTaskPlatform, TaskID: task.TaskID,
		ReservationID: strings.TrimSpace(reservationID), UserID: task.UserId, ChannelID: task.ChannelId,
		Action: action, ProviderTaskID: strings.TrimSpace(providerTaskID),
	}
	return persistKlingRecoveryJournal(record)
}

func persistKlingRecoveryJournal(record klingRecoveryJournalRecord) error {
	if err := validateKlingRecoveryJournalRecord(record); err != nil {
		return err
	}
	path, err := klingRecoveryJournalPath(record.TaskID)
	if err != nil {
		return err
	}
	plaintext, err := jsonutil.Marshal(record)
	if err != nil {
		return err
	}
	ciphertext, err := asyncTaskEncryptBound(string(plaintext), klingRecoveryJournalBinding(record.TaskID, record.Action))
	if err != nil {
		return errors.New("encrypt Kling recovery journal")
	}
	data, err := jsonutil.Marshal(klingRecoveryJournalDiskEnvelope{
		Version: klingRecoveryJournalDiskVersion, Action: string(record.Action), Ciphertext: ciphertext,
	})
	if err != nil {
		return err
	}
	if len(data) > maxKlingRecoveryJournalBytes {
		return errors.New("Kling recovery journal is too large")
	}
	if err := persistVideoRecoveryJournalOnce(path, data); err != nil {
		return fmt.Errorf("persist Kling recovery journal: %w", err)
	}
	return nil
}

func loadKlingRecoveryJournal(taskID string) (klingRecoveryJournalRecord, error) {
	path, err := klingRecoveryJournalPath(taskID)
	if err != nil {
		return klingRecoveryJournalRecord{}, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return klingRecoveryJournalRecord{}, err
	}
	if !info.Mode().IsRegular() {
		return klingRecoveryJournalRecord{}, errors.New("Kling recovery journal is not a regular file")
	}
	file, err := openRecoveryJournalFile(path)
	if err != nil {
		return klingRecoveryJournalRecord{}, err
	}
	data, readErr := io.ReadAll(io.LimitReader(file, maxKlingRecoveryJournalBytes+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil {
		return klingRecoveryJournalRecord{}, errors.Join(readErr, closeErr)
	}
	if len(data) > maxKlingRecoveryJournalBytes {
		return klingRecoveryJournalRecord{}, errors.New("Kling recovery journal is too large")
	}
	var disk klingRecoveryJournalDiskEnvelope
	if err := jsonutil.Unmarshal(data, &disk); err != nil || disk.Version != klingRecoveryJournalDiskVersion ||
		strings.TrimSpace(disk.Ciphertext) == "" {
		return klingRecoveryJournalRecord{}, errors.New("invalid Kling recovery journal envelope")
	}
	action := kling.Action(disk.Action)
	if !validKlingAction(action) {
		return klingRecoveryJournalRecord{}, errors.New("invalid Kling recovery journal action")
	}
	plaintext, err := asyncTaskDecryptBound(disk.Ciphertext, klingRecoveryJournalBinding(taskID, action))
	if err != nil {
		return klingRecoveryJournalRecord{}, errors.New("decrypt Kling recovery journal")
	}
	var record klingRecoveryJournalRecord
	if err := jsonutil.Unmarshal([]byte(plaintext), &record); err != nil || record.TaskID != taskID || record.Action != action {
		return klingRecoveryJournalRecord{}, errors.New("decode Kling recovery journal")
	}
	if err := validateKlingRecoveryJournalRecord(record); err != nil {
		return klingRecoveryJournalRecord{}, err
	}
	return record, nil
}

func validateKlingRecoveryJournalRecord(record klingRecoveryJournalRecord) error {
	if record.Family != "kling" || record.Platform != klingTaskPlatform || !validKlingAction(record.Action) {
		return errors.New("invalid Kling recovery provenance")
	}
	if _, err := klingRecoveryJournalPath(record.TaskID); err != nil {
		return err
	}
	if record.UserID <= 0 || record.ChannelID <= 0 ||
		!validKlingRecoveryOpaqueID(record.ReservationID, 64) ||
		!validKlingRecoveryOpaqueID(record.ProviderTaskID, kling.MaxProviderTaskIDBytes) {
		return errors.New("invalid Kling recovery identity")
	}
	return nil
}

func validKlingRecoveryOpaqueID(value string, maxBytes int) bool {
	if value == "" || strings.TrimSpace(value) != value || len(value) > maxBytes || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if character < 0x21 || character == 0x7f || character == '/' || character == '\\' ||
			character == '?' || character == '#' {
			return false
		}
	}
	return true
}

func removeKlingRecoveryJournal(taskID string) error {
	path, err := klingRecoveryJournalPath(taskID)
	if err != nil {
		return err
	}
	return removeVideoRecoveryJournalOnce(path)
}

// PromoteKlingTaskRecoveryJournalsContext imports node-local provider IDs
// before the cluster-wide poller. Evidence is never age-deleted.
func PromoteKlingTaskRecoveryJournalsContext(ctx context.Context) error {
	return promoteKlingRecoveryJournalRecords(ctx, klingRecoveryJournalPromotionLimit)
}

func promoteKlingRecoveryJournalRecords(ctx context.Context, limit int) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if limit <= 0 || limit > maxKlingRecoveryJournalPromotionLimit {
		limit = klingRecoveryJournalPromotionLimit
	}
	directory, err := validatedKlingRecoveryJournalDirectory()
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
		return fmt.Errorf("read Kling recovery journal directory: %w", err)
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
		if _, err := klingRecoveryJournalPath(taskID); err != nil {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			promotionErrors = append(promotionErrors, errors.New("inspect Kling recovery journal file"))
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
		start := int((klingRecoveryJournalPromotionCursor.Add(uint64(limit)) - uint64(limit)) % uint64(len(files)))
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
		record, err := loadKlingRecoveryJournal(file.taskID)
		if err != nil {
			promotionErrors = append(promotionErrors, fmt.Errorf("promote Kling recovery %s: unreadable encrypted journal", file.taskID))
			continue
		}
		remove, err := promoteKlingRecoveryJournalRecord(ctx, record)
		if err != nil {
			promotionErrors = append(promotionErrors, fmt.Errorf("promote Kling recovery %s: durable transition failed", file.taskID))
			continue
		}
		if remove {
			if err := removeKlingRecoveryJournal(file.taskID); err != nil {
				promotionErrors = append(promotionErrors, fmt.Errorf("promote Kling recovery %s: cleanup failed", file.taskID))
			}
		}
	}
	return errors.Join(promotionErrors...)
}

func promoteKlingRecoveryJournalRecord(ctx context.Context, record klingRecoveryJournalRecord) (bool, error) {
	if model.DB == nil {
		return false, errors.New("Kling recovery database is unavailable")
	}
	var operation model.TaskOperation
	if err := model.DB.WithContext(ctx).Where("task_id = ? AND reservation_id = ? AND platform = ?",
		record.TaskID, record.ReservationID, klingTaskPlatform).First(&operation).Error; err != nil {
		return false, err
	}
	if operation.UserID != record.UserID || operation.ChannelID != record.ChannelID {
		return false, errors.New("Kling recovery operation identity mismatch")
	}
	task, err := loadUniqueKlingRecoveryTask(ctx, &operation)
	if err != nil {
		return false, err
	}
	properties, err := decodeKlingTaskProperties(task.Properties)
	if err != nil || properties.Action != record.Action {
		return false, errors.New("Kling recovery action mismatch")
	}
	if klingRecoveryProviderIDIsDurable(record, task, &operation) {
		return true, nil
	}
	if operation.LeaseOwner != "" {
		return false, nil
	}
	switch operation.State {
	case model.TaskOperationDispatching, model.TaskOperationSubmitted, model.TaskOperationManualReview:
		provider := &kling.Task{ProviderTaskID: record.ProviderTaskID, Status: kling.StatusSubmitted}
		if err := persistKlingProviderStatePending(task, record.ReservationID, provider,
			operation.State, ""); err != nil {
			return false, err
		}
		var freshOperation model.TaskOperation
		if err := model.DB.WithContext(ctx).Where("id = ?", operation.ID).First(&freshOperation).Error; err != nil {
			return false, err
		}
		freshTask, err := loadUniqueKlingRecoveryTask(ctx, &freshOperation)
		return err == nil && klingRecoveryProviderIDIsDurable(record, freshTask, &freshOperation), err
	case model.TaskOperationTerminal, model.TaskOperationRefunded:
		return false, errors.New("Kling recovery journal conflicts with terminal state")
	default:
		return false, errors.New("Kling recovery operation state is not promotable")
	}
}

func klingRecoveryProviderIDIsDurable(
	record klingRecoveryJournalRecord,
	task *model.Task,
	operation *model.TaskOperation,
) bool {
	if task == nil || operation == nil || !klingTaskOperationIdentityMatches(task, operation, record.ReservationID) {
		return false
	}
	privateData, err := decodeKlingTaskPrivateData(task.PrivateData)
	if err != nil || privateData.EncryptedUpstreamTaskID == "" || operation.EncryptedProviderTaskID == "" {
		return false
	}
	binding := klingProviderTaskBinding(record.TaskID, record.ReservationID, record.UserID, record.ChannelID, record.Action)
	for _, encoded := range []string{privateData.EncryptedUpstreamTaskID, operation.EncryptedProviderTaskID} {
		providerID, err := asyncTaskDecryptBound(encoded, binding)
		if err != nil || providerID != record.ProviderTaskID {
			return false
		}
	}
	return true
}
