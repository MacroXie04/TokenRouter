package tasks

import (
	"context"
	"errors"
	"fmt"
	"github.com/tokenrouter/tokenrouter/internal/platform/jsonutil"
	geminiVeo "github.com/tokenrouter/tokenrouter/internal/relay/providers/task/gemini"
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
	maxGeminiVeoRecoveryJournalBytes          = 64 * 1024
	geminiVeoRecoveryJournalDiskVersion       = 1
	geminiVeoRecoveryJournalWriteAttempts     = 3
	geminiVeoRecoveryJournalPromotionLimit    = 100
	maxGeminiVeoRecoveryJournalPromotionLimit = 1000
)

type geminiVeoRecoveryJournalRecord struct {
	Family         string           `json:"family"`
	Platform       string           `json:"platform"`
	TaskID         string           `json:"task_id"`
	ReservationID  string           `json:"reservation_id"`
	UserID         int              `json:"user_id"`
	ChannelID      int              `json:"channel_id"`
	Action         geminiVeo.Action `json:"action"`
	ProviderTaskID string           `json:"provider_task_id"`
}

type geminiVeoRecoveryJournalDiskEnvelope struct {
	Version    int    `json:"version"`
	Platform   string `json:"platform"`
	Action     string `json:"action"`
	Ciphertext string `json:"ciphertext"`
}

var geminiVeoRecoveryJournalPromotionCursor atomic.Uint64

func geminiVeoRecoveryJournalDirectory() string {
	if configured := strings.TrimSpace(os.Getenv("GEMINI_VEO_TASK_RECOVERY_DIR")); configured != "" {
		return configured
	}
	if videoDirectory := strings.TrimSpace(os.Getenv("VIDEO_TASK_RECOVERY_DIR")); videoDirectory != "" {
		return filepath.Join(videoDirectory, "veo-task-v1")
	}
	if sqlitePath := strings.TrimSpace(os.Getenv("SQLITE_PATH")); sqlitePath != "" {
		return filepath.Join(filepath.Dir(sqlitePath), ".tokenrouter-gemini-veo-recovery")
	}
	return ".tokenrouter-gemini-veo-recovery"
}

func geminiVeoRecoveryJournalPath(taskID string) (string, error) {
	if validateGeminiVeoTaskPublicID(taskID) != nil || filepath.Base(taskID) != taskID {
		return "", errors.New("invalid GeminiVeo recovery task id")
	}
	directory, err := validatedGeminiVeoRecoveryJournalDirectory()
	if err != nil {
		return "", err
	}
	return filepath.Join(directory, taskID+".json"), nil
}

func validatedGeminiVeoRecoveryJournalDirectory() (string, error) {
	return validateRecoveryJournalDirectory(geminiVeoRecoveryJournalDirectory(), "GeminiVeo")
}

// persistAcceptedGeminiVeoRecoveryJournal records the minimum accepted-provider
// identity without database I/O. This closes the outage window after the
// provider accepts a task but before the primary database can store its id.
func persistAcceptedGeminiVeoRecoveryJournal(
	task *model.Task,
	reservationID, providerTaskID string,
	action geminiVeo.Action,
) error {
	if !isGeminiVeoTask(task) {
		return errors.New("invalid GeminiVeo recovery task")
	}
	descriptor, ok := veoTaskProviderByPlatform(task.Platform)
	if !ok {
		return errors.New("invalid Veo recovery provider")
	}
	record := geminiVeoRecoveryJournalRecord{
		Family: descriptor.Family, Platform: task.Platform, TaskID: task.TaskID,
		ReservationID: strings.TrimSpace(reservationID), UserID: task.UserId,
		ChannelID: task.ChannelId, Action: action,
		ProviderTaskID: strings.TrimSpace(providerTaskID),
	}
	return persistGeminiVeoRecoveryJournal(record)
}

func persistGeminiVeoRecoveryJournal(record geminiVeoRecoveryJournalRecord) error {
	if err := validateGeminiVeoRecoveryJournalRecord(record); err != nil {
		return err
	}
	path, err := geminiVeoRecoveryJournalPath(record.TaskID)
	if err != nil {
		return err
	}
	plaintext, err := jsonutil.Marshal(record)
	if err != nil {
		return errors.New("encode GeminiVeo recovery journal")
	}
	ciphertext, err := asyncTaskEncryptBound(
		string(plaintext), geminiVeoRecoveryJournalBinding(record.TaskID, record.Platform, record.Action),
	)
	if err != nil {
		return errors.New("encrypt GeminiVeo recovery journal")
	}
	data, err := jsonutil.Marshal(geminiVeoRecoveryJournalDiskEnvelope{
		Version: geminiVeoRecoveryJournalDiskVersion, Platform: record.Platform,
		Action: string(record.Action), Ciphertext: ciphertext,
	})
	if err != nil || len(data) > maxGeminiVeoRecoveryJournalBytes {
		return errors.New("encode GeminiVeo recovery journal envelope")
	}
	var lastErr error
	for attempt := 0; attempt < geminiVeoRecoveryJournalWriteAttempts; attempt++ {
		if err := persistVideoRecoveryJournalOnce(path, data); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if attempt < geminiVeoRecoveryJournalWriteAttempts-1 {
			time.Sleep(time.Duration(20*(attempt+1)) * time.Millisecond)
		}
	}
	return fmt.Errorf("persist GeminiVeo recovery journal: %w", lastErr)
}

func loadGeminiVeoRecoveryJournal(taskID string) (geminiVeoRecoveryJournalRecord, error) {
	path, err := geminiVeoRecoveryJournalPath(taskID)
	if err != nil {
		return geminiVeoRecoveryJournalRecord{}, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return geminiVeoRecoveryJournalRecord{}, err
	}
	if !info.Mode().IsRegular() {
		return geminiVeoRecoveryJournalRecord{}, errors.New("GeminiVeo recovery journal is not a regular file")
	}
	file, err := openRecoveryJournalFile(path)
	if err != nil {
		return geminiVeoRecoveryJournalRecord{}, err
	}
	data, readErr := io.ReadAll(io.LimitReader(file, maxGeminiVeoRecoveryJournalBytes+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil {
		return geminiVeoRecoveryJournalRecord{}, errors.Join(readErr, closeErr)
	}
	if len(data) > maxGeminiVeoRecoveryJournalBytes {
		return geminiVeoRecoveryJournalRecord{}, errors.New("GeminiVeo recovery journal is too large")
	}
	var disk geminiVeoRecoveryJournalDiskEnvelope
	if err := strictGeminiVeoJSON(data, &disk); err != nil || disk.Version != geminiVeoRecoveryJournalDiskVersion ||
		!model.IsVeoTaskOperationPlatform(disk.Platform) || strings.TrimSpace(disk.Ciphertext) == "" {
		return geminiVeoRecoveryJournalRecord{}, errors.New("invalid GeminiVeo recovery journal envelope")
	}
	action, err := geminiVeo.ParseAction(disk.Action)
	if err != nil {
		return geminiVeoRecoveryJournalRecord{}, errors.New("invalid GeminiVeo recovery journal action")
	}
	plaintext, err := asyncTaskDecryptBound(
		disk.Ciphertext, geminiVeoRecoveryJournalBinding(taskID, disk.Platform, action),
	)
	if err != nil {
		return geminiVeoRecoveryJournalRecord{}, errors.New("decrypt GeminiVeo recovery journal")
	}
	var record geminiVeoRecoveryJournalRecord
	if err := strictGeminiVeoJSON([]byte(plaintext), &record); err != nil ||
		record.TaskID != taskID || record.Platform != disk.Platform || record.Action != action {
		return geminiVeoRecoveryJournalRecord{}, errors.New("decode GeminiVeo recovery journal")
	}
	if err := validateGeminiVeoRecoveryJournalRecord(record); err != nil {
		return geminiVeoRecoveryJournalRecord{}, err
	}
	return record, nil
}

func validateGeminiVeoRecoveryJournalRecord(record geminiVeoRecoveryJournalRecord) error {
	descriptor, ok := veoTaskProviderByPlatform(record.Platform)
	if !ok || record.Family != descriptor.Family {
		return errors.New("invalid GeminiVeo recovery provenance")
	}
	if !validGeminiVeoAction(record.Action) {
		return errors.New("invalid GeminiVeo recovery action")
	}
	if _, err := geminiVeoRecoveryJournalPath(record.TaskID); err != nil {
		return err
	}
	if record.UserID <= 0 || record.ChannelID <= 0 ||
		!validGeminiVeoRecoveryOpaqueID(record.ReservationID, 64) ||
		!validGeminiVeoRecoveryOpaqueID(record.ProviderTaskID, descriptor.MaxProviderTaskIDBytes) {
		return errors.New("invalid GeminiVeo recovery identity")
	}
	return nil
}

func validGeminiVeoRecoveryOpaqueID(value string, maxBytes int) bool {
	if value == "" || strings.TrimSpace(value) != value || len(value) > maxBytes || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) || unicode.IsSpace(character) ||
			character == '\\' || character == '?' || character == '#' {
			return false
		}
	}
	return true
}

func removeGeminiVeoRecoveryJournal(taskID string) error {
	path, err := geminiVeoRecoveryJournalPath(taskID)
	if err != nil {
		return err
	}
	var lastErr error
	for attempt := 0; attempt < geminiVeoRecoveryJournalWriteAttempts; attempt++ {
		if err := removeVideoRecoveryJournalOnce(path); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if attempt < geminiVeoRecoveryJournalWriteAttempts-1 {
			time.Sleep(time.Duration(20*(attempt+1)) * time.Millisecond)
		}
	}
	return fmt.Errorf("remove GeminiVeo recovery journal: %w", lastErr)
}

// PromoteGeminiVeoTaskRecoveryJournalsContext imports node-local accepted task ids
// before the cluster-wide reconciler runs. Journal evidence is never expired;
// it is removed only after the same provider identity is durable in the DB.
func PromoteGeminiVeoTaskRecoveryJournalsContext(ctx context.Context) error {
	return promoteGeminiVeoRecoveryJournalRecords(ctx, geminiVeoRecoveryJournalPromotionLimit)
}

func promoteGeminiVeoRecoveryJournalRecords(ctx context.Context, limit int) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if limit <= 0 || limit > maxGeminiVeoRecoveryJournalPromotionLimit {
		limit = geminiVeoRecoveryJournalPromotionLimit
	}
	directory, err := validatedGeminiVeoRecoveryJournalDirectory()
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
		return fmt.Errorf("read GeminiVeo recovery journal directory: %w", err)
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
		if _, err := geminiVeoRecoveryJournalPath(taskID); err != nil {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			promotionErrors = append(promotionErrors, errors.New("inspect GeminiVeo recovery journal file"))
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
		start := int((geminiVeoRecoveryJournalPromotionCursor.Add(uint64(limit)) - uint64(limit)) % uint64(len(files)))
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
		record, err := loadGeminiVeoRecoveryJournal(file.taskID)
		if err != nil {
			promotionErrors = append(promotionErrors,
				fmt.Errorf("promote GeminiVeo recovery %s: unreadable encrypted journal", file.taskID))
			continue
		}
		remove, err := promoteGeminiVeoRecoveryJournalRecord(ctx, record)
		if err != nil {
			promotionErrors = append(promotionErrors,
				fmt.Errorf("promote GeminiVeo recovery %s: durable transition failed", file.taskID))
			continue
		}
		if remove {
			if err := removeGeminiVeoRecoveryJournal(file.taskID); err != nil {
				promotionErrors = append(promotionErrors,
					fmt.Errorf("promote GeminiVeo recovery %s: cleanup failed", file.taskID))
			}
		}
	}
	return errors.Join(promotionErrors...)
}

func promoteGeminiVeoRecoveryJournalRecord(ctx context.Context, record geminiVeoRecoveryJournalRecord) (bool, error) {
	if model.DB == nil {
		return false, errors.New("GeminiVeo recovery database is unavailable")
	}
	var operation model.TaskOperation
	if err := model.DB.WithContext(ctx).
		Where("task_id = ? AND reservation_id = ? AND platform = ?", record.TaskID,
			record.ReservationID, record.Platform).First(&operation).Error; err != nil {
		return false, err
	}
	if operation.UserID != record.UserID || operation.ChannelID != record.ChannelID {
		return false, errors.New("GeminiVeo recovery operation identity mismatch")
	}
	var tasks []model.Task
	if err := model.DB.WithContext(ctx).Where("task_id = ? AND platform = ?", record.TaskID,
		record.Platform).Limit(2).Find(&tasks).Error; err != nil {
		return false, err
	}
	if len(tasks) != 1 || !geminiVeoTaskOperationIdentityMatches(&tasks[0], &operation) ||
		tasks[0].UserId != record.UserID || tasks[0].ChannelId != record.ChannelID {
		return false, errors.New("GeminiVeo recovery task identity is unavailable or ambiguous")
	}
	properties, err := decodeGeminiVeoTaskProperties(tasks[0].Properties)
	if err != nil || properties.Action != record.Action {
		return false, errors.New("GeminiVeo recovery action mismatch")
	}
	if geminiVeoRecoveryProviderIDIsDurable(record, &tasks[0], &operation) {
		return true, nil
	}
	if operation.LeaseOwner != "" {
		return false, nil
	}
	switch operation.State {
	case model.TaskOperationDispatching, model.TaskOperationSubmitted,
		model.TaskOperationUnknown, model.TaskOperationManualReview:
		if err := geminiVeoRecoveryProviderIDsDoNotConflict(record, &tasks[0], &operation); err != nil {
			return false, err
		}
		if err := persistAcceptedGeminiVeoFallback(&tasks[0], record.ReservationID, record.ProviderTaskID); err != nil {
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
		return geminiVeoRecoveryProviderIDIsDurable(record, &freshTask, &freshOperation), nil
	case model.TaskOperationTerminal, model.TaskOperationReversed:
		return false, errors.New("GeminiVeo recovery provider identity is missing from terminal state")
	case model.TaskOperationPrepared, model.TaskOperationRefunded:
		return false, errors.New("GeminiVeo recovery journal conflicts with a non-dispatched operation")
	default:
		return false, errors.New("GeminiVeo recovery operation state is not promotable")
	}
}

func geminiVeoRecoveryProviderIDsDoNotConflict(
	record geminiVeoRecoveryJournalRecord,
	task *model.Task,
	operation *model.TaskOperation,
) error {
	privateData, err := decodeGeminiVeoTaskPrivateData(task.PrivateData)
	if err != nil || privateData.RelayReservationID != record.ReservationID {
		return errors.New("GeminiVeo recovery private identity mismatch")
	}
	binding := geminiVeoProviderTaskBinding(
		record.TaskID, record.ReservationID, record.Platform, record.UserID, record.ChannelID,
	)
	for _, encoded := range []string{operation.EncryptedProviderTaskID, privateData.EncryptedProviderTaskID} {
		if encoded == "" {
			continue
		}
		providerID, err := asyncTaskDecryptBound(encoded, binding)
		if err != nil || providerID != record.ProviderTaskID {
			return errors.New("GeminiVeo recovery provider identity conflicts with durable state")
		}
	}
	return nil
}

func geminiVeoRecoveryProviderIDIsDurable(
	record geminiVeoRecoveryJournalRecord,
	task *model.Task,
	operation *model.TaskOperation,
) bool {
	if task == nil || operation == nil || operation.EncryptedProviderTaskID == "" ||
		!geminiVeoTaskOperationIdentityMatches(task, operation) {
		return false
	}
	privateData, err := decodeGeminiVeoTaskPrivateData(task.PrivateData)
	if err != nil || privateData.EncryptedProviderTaskID == "" ||
		privateData.SettlementPending != operation.SettlementPending {
		return false
	}
	binding := geminiVeoProviderTaskBinding(
		record.TaskID, record.ReservationID, record.Platform, record.UserID, record.ChannelID,
	)
	for _, encoded := range []string{operation.EncryptedProviderTaskID, privateData.EncryptedProviderTaskID} {
		providerID, err := asyncTaskDecryptBound(encoded, binding)
		if err != nil || providerID != record.ProviderTaskID {
			return false
		}
	}
	return true
}
