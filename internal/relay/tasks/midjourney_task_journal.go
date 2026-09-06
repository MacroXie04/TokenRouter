package tasks

import (
	"context"
	"errors"
	"fmt"
	"github.com/tokenrouter/tokenrouter/internal/platform/jsonutil"
	"github.com/tokenrouter/tokenrouter/internal/relay/providers/midjourney"
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
	maxMidjourneyRecoveryJournalBytes          = 64 * 1024
	midjourneyRecoveryJournalDiskVersion       = 1
	midjourneyRecoveryJournalWriteAttempts     = 3
	midjourneyRecoveryJournalPromotionLimit    = 100
	maxMidjourneyRecoveryJournalPromotionLimit = 1000
)

type midjourneyRecoveryJournalRecord struct {
	Family         string            `json:"family"`
	Platform       string            `json:"platform"`
	TaskID         string            `json:"task_id"`
	ReservationID  string            `json:"reservation_id"`
	UserID         int               `json:"user_id"`
	ChannelID      int               `json:"channel_id"`
	Action         midjourney.Action `json:"action"`
	ProviderTaskID string            `json:"provider_task_id"`
}

type midjourneyRecoveryJournalDiskEnvelope struct {
	Version    int    `json:"version"`
	Action     string `json:"action"`
	Ciphertext string `json:"ciphertext"`
}

var midjourneyRecoveryJournalPromotionCursor atomic.Uint64

func midjourneyRecoveryJournalDirectory() string {
	if configured := strings.TrimSpace(os.Getenv("MIDJOURNEY_TASK_RECOVERY_DIR")); configured != "" {
		return configured
	}
	if sqlitePath := strings.TrimSpace(os.Getenv("SQLITE_PATH")); sqlitePath != "" {
		return filepath.Join(filepath.Dir(sqlitePath), ".tokenrouter-midjourney-recovery")
	}
	return ".tokenrouter-midjourney-recovery"
}

func validatedMidjourneyRecoveryJournalDirectory() (string, error) {
	return validateRecoveryJournalDirectory(midjourneyRecoveryJournalDirectory(), "Midjourney")
}

func midjourneyRecoveryJournalPath(taskID string) (string, error) {
	if validateMidjourneyTaskPublicID(taskID) != nil || filepath.Base(taskID) != taskID {
		return "", errors.New("invalid Midjourney recovery task id")
	}
	directory, err := validatedMidjourneyRecoveryJournalDirectory()
	if err != nil {
		return "", err
	}
	return filepath.Join(directory, taskID+".json"), nil
}

func midjourneyRecoveryJournalBinding(taskID string, action midjourney.Action) string {
	return "midjourney:recovery-journal:v1:" + taskID + ":" + string(action)
}

func persistAcceptedMidjourneyRecoveryJournal(task *model.Task, reservationID, providerTaskID string,
	action midjourney.Action) error {
	if task == nil || task.Platform != midjourneyTaskPlatform {
		return errors.New("invalid Midjourney recovery task")
	}
	record := midjourneyRecoveryJournalRecord{
		Family: "midjourney", Platform: midjourneyTaskPlatform, TaskID: task.TaskID,
		ReservationID: strings.TrimSpace(reservationID), UserID: task.UserId,
		ChannelID: task.ChannelId, Action: action, ProviderTaskID: strings.TrimSpace(providerTaskID),
	}
	if err := validateMidjourneyRecoveryJournalRecord(record); err != nil {
		return err
	}
	path, err := midjourneyRecoveryJournalPath(record.TaskID)
	if err != nil {
		return err
	}
	plaintext, err := jsonutil.Marshal(record)
	if err != nil {
		return errors.New("encode Midjourney recovery journal")
	}
	ciphertext, err := asyncTaskEncryptBound(string(plaintext),
		midjourneyRecoveryJournalBinding(record.TaskID, record.Action))
	if err != nil {
		return errors.New("encrypt Midjourney recovery journal")
	}
	data, err := jsonutil.Marshal(midjourneyRecoveryJournalDiskEnvelope{
		Version: midjourneyRecoveryJournalDiskVersion, Action: string(record.Action), Ciphertext: ciphertext,
	})
	if err != nil || len(data) > maxMidjourneyRecoveryJournalBytes {
		return errors.New("encode Midjourney recovery journal envelope")
	}
	var lastErr error
	for attempt := 0; attempt < midjourneyRecoveryJournalWriteAttempts; attempt++ {
		if err := persistVideoRecoveryJournalOnce(path, data); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if attempt < midjourneyRecoveryJournalWriteAttempts-1 {
			time.Sleep(time.Duration(20*(attempt+1)) * time.Millisecond)
		}
	}
	return fmt.Errorf("persist Midjourney recovery journal: %w", lastErr)
}

func loadMidjourneyRecoveryJournal(taskID string) (midjourneyRecoveryJournalRecord, error) {
	path, err := midjourneyRecoveryJournalPath(taskID)
	if err != nil {
		return midjourneyRecoveryJournalRecord{}, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return midjourneyRecoveryJournalRecord{}, err
	}
	if !info.Mode().IsRegular() {
		return midjourneyRecoveryJournalRecord{}, errors.New("Midjourney recovery journal is not a regular file")
	}
	file, err := openRecoveryJournalFile(path)
	if err != nil {
		return midjourneyRecoveryJournalRecord{}, err
	}
	data, readErr := io.ReadAll(io.LimitReader(file, maxMidjourneyRecoveryJournalBytes+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil {
		return midjourneyRecoveryJournalRecord{}, errors.Join(readErr, closeErr)
	}
	if len(data) > maxMidjourneyRecoveryJournalBytes {
		return midjourneyRecoveryJournalRecord{}, errors.New("Midjourney recovery journal is too large")
	}
	var disk midjourneyRecoveryJournalDiskEnvelope
	if err := strictMidjourneyTaskJSON(data, &disk); err != nil ||
		disk.Version != midjourneyRecoveryJournalDiskVersion || strings.TrimSpace(disk.Ciphertext) == "" {
		return midjourneyRecoveryJournalRecord{}, errors.New("invalid Midjourney recovery journal envelope")
	}
	action := midjourney.Action(disk.Action)
	if _, ok := midjourney.ModelForAction(action); !ok {
		return midjourneyRecoveryJournalRecord{}, errors.New("invalid Midjourney recovery journal action")
	}
	plaintext, err := asyncTaskDecryptBound(disk.Ciphertext,
		midjourneyRecoveryJournalBinding(taskID, action))
	if err != nil {
		return midjourneyRecoveryJournalRecord{}, errors.New("decrypt Midjourney recovery journal")
	}
	var record midjourneyRecoveryJournalRecord
	if err := strictMidjourneyTaskJSON([]byte(plaintext), &record); err != nil ||
		record.TaskID != taskID || record.Action != action {
		return midjourneyRecoveryJournalRecord{}, errors.New("decode Midjourney recovery journal")
	}
	if err := validateMidjourneyRecoveryJournalRecord(record); err != nil {
		return midjourneyRecoveryJournalRecord{}, err
	}
	return record, nil
}

func validateMidjourneyRecoveryJournalRecord(record midjourneyRecoveryJournalRecord) error {
	if record.Family != "midjourney" || record.Platform != midjourneyTaskPlatform ||
		validateMidjourneyTaskPublicID(record.TaskID) != nil || record.UserID <= 0 || record.ChannelID <= 0 ||
		!validMidjourneyRecoveryOpaqueID(record.ReservationID, 64) ||
		!validMidjourneyRecoveryOpaqueID(record.ProviderTaskID, midjourney.MaxTaskIDBytes) {
		return errors.New("invalid Midjourney recovery identity")
	}
	modelName, ok := midjourney.ModelForAction(record.Action)
	if !ok || modelName == "" {
		return errors.New("invalid Midjourney recovery action")
	}
	_, err := midjourneyRecoveryJournalPath(record.TaskID)
	return err
}

func validMidjourneyRecoveryOpaqueID(value string, maximum int) bool {
	if value == "" || strings.TrimSpace(value) != value || len(value) > maximum || !utf8.ValidString(value) {
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

func removeMidjourneyRecoveryJournal(taskID string) error {
	path, err := midjourneyRecoveryJournalPath(taskID)
	if err != nil {
		return err
	}
	var lastErr error
	for attempt := 0; attempt < midjourneyRecoveryJournalWriteAttempts; attempt++ {
		if err := removeVideoRecoveryJournalOnce(path); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if attempt < midjourneyRecoveryJournalWriteAttempts-1 {
			time.Sleep(time.Duration(20*(attempt+1)) * time.Millisecond)
		}
	}
	return fmt.Errorf("remove Midjourney recovery journal: %w", lastErr)
}

// PromoteMidjourneyTaskRecoveryJournalsContext imports node-local accepted
// provider identities before cluster-wide polling. Evidence is removed only
// after the same encrypted provider identity is durable in both DB records.
func PromoteMidjourneyTaskRecoveryJournalsContext(ctx context.Context) error {
	return promoteMidjourneyRecoveryJournalRecords(ctx, midjourneyRecoveryJournalPromotionLimit)
}

func promoteMidjourneyRecoveryJournalRecords(ctx context.Context, limit int) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if limit <= 0 || limit > maxMidjourneyRecoveryJournalPromotionLimit {
		limit = midjourneyRecoveryJournalPromotionLimit
	}
	directory, err := validatedMidjourneyRecoveryJournalDirectory()
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
		return fmt.Errorf("read Midjourney recovery journal directory: %w", err)
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
		if _, err := midjourneyRecoveryJournalPath(taskID); err != nil {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			promotionErrors = append(promotionErrors, errors.New("inspect Midjourney recovery journal file"))
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
		start := int((midjourneyRecoveryJournalPromotionCursor.Add(uint64(limit)) - uint64(limit)) % uint64(len(files)))
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
		record, err := loadMidjourneyRecoveryJournal(file.taskID)
		if err != nil {
			promotionErrors = append(promotionErrors,
				fmt.Errorf("promote Midjourney recovery %s: unreadable encrypted journal", file.taskID))
			continue
		}
		remove, err := promoteMidjourneyRecoveryJournalRecord(ctx, record)
		if err != nil {
			promotionErrors = append(promotionErrors,
				fmt.Errorf("promote Midjourney recovery %s: durable transition failed", file.taskID))
			continue
		}
		if remove {
			if err := removeMidjourneyRecoveryJournal(file.taskID); err != nil {
				promotionErrors = append(promotionErrors,
					fmt.Errorf("promote Midjourney recovery %s: cleanup failed", file.taskID))
			}
		}
	}
	return errors.Join(promotionErrors...)
}

func promoteMidjourneyRecoveryJournalRecord(ctx context.Context,
	record midjourneyRecoveryJournalRecord) (bool, error) {
	if model.DB == nil {
		return false, errors.New("Midjourney recovery database is unavailable")
	}
	var operation model.TaskOperation
	if err := model.DB.WithContext(ctx).
		Where("task_id = ? AND reservation_id = ? AND platform = ?", record.TaskID,
			record.ReservationID, midjourneyTaskPlatform).First(&operation).Error; err != nil {
		return false, err
	}
	if operation.UserID != record.UserID || operation.ChannelID != record.ChannelID {
		return false, errors.New("Midjourney recovery operation identity mismatch")
	}
	var tasks []model.Task
	if err := model.DB.WithContext(ctx).Where("task_id = ? AND platform = ?", record.TaskID,
		midjourneyTaskPlatform).Limit(2).Find(&tasks).Error; err != nil {
		return false, err
	}
	var mirrors []model.Midjourney
	if err := model.DB.WithContext(ctx).Where("mj_id = ? AND user_id = ? AND channel_id = ?",
		record.TaskID, record.UserID, record.ChannelID).Limit(2).Find(&mirrors).Error; err != nil {
		return false, err
	}
	if len(tasks) != 1 || len(mirrors) != 1 ||
		!midjourneyTaskOperationIdentityMatches(&tasks[0], &mirrors[0], &operation, record.ReservationID) {
		return false, errors.New("Midjourney recovery task identity is unavailable or ambiguous")
	}
	properties, err := decodeMidjourneyTaskProperties(tasks[0].Properties)
	if err != nil || properties.Action != record.Action {
		return false, errors.New("Midjourney recovery action mismatch")
	}
	if midjourneyRecoveryProviderIDIsDurable(record, &tasks[0], &operation) {
		return true, nil
	}
	if operation.LeaseOwner != "" {
		return false, nil
	}
	switch operation.State {
	case model.TaskOperationDispatching, model.TaskOperationSubmitted, model.TaskOperationManualReview:
		if err := midjourneyRecoveryProviderIDsDoNotConflict(record, &tasks[0], &operation); err != nil {
			return false, err
		}
		if err := persistAcceptedMidjourneyFallback(&tasks[0], record.ReservationID,
			record.ProviderTaskID); err != nil {
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
		return midjourneyRecoveryProviderIDIsDurable(record, &freshTask, &freshOperation), nil
	case model.TaskOperationTerminal:
		return false, errors.New("Midjourney recovery provider identity is missing from terminal state")
	case model.TaskOperationPrepared, model.TaskOperationRefunded:
		return false, errors.New("Midjourney recovery journal conflicts with a non-dispatched operation")
	default:
		return false, errors.New("Midjourney recovery operation state is not promotable")
	}
}

func midjourneyRecoveryProviderIDsDoNotConflict(record midjourneyRecoveryJournalRecord,
	task *model.Task, operation *model.TaskOperation) error {
	privateData, err := decodeMidjourneyTaskPrivateData(task.PrivateData)
	if err != nil || privateData.RelayReservationID != record.ReservationID {
		return errors.New("Midjourney recovery private identity mismatch")
	}
	binding := midjourneyProviderTaskBinding(record.TaskID, record.ReservationID,
		record.UserID, record.ChannelID, record.Action)
	for _, encoded := range []string{operation.EncryptedProviderTaskID, privateData.EncryptedProviderTaskID} {
		if encoded == "" {
			continue
		}
		providerID, err := asyncTaskDecryptBound(encoded, binding)
		if err != nil || providerID != record.ProviderTaskID {
			return errors.New("Midjourney recovery provider identity conflicts with durable state")
		}
	}
	return nil
}

func midjourneyRecoveryProviderIDIsDurable(record midjourneyRecoveryJournalRecord,
	task *model.Task, operation *model.TaskOperation) bool {
	if task == nil || operation == nil || operation.EncryptedProviderTaskID == "" {
		return false
	}
	privateData, err := decodeMidjourneyTaskPrivateData(task.PrivateData)
	if err != nil || privateData.EncryptedProviderTaskID == "" ||
		privateData.SettlementPending != operation.SettlementPending {
		return false
	}
	binding := midjourneyProviderTaskBinding(record.TaskID, record.ReservationID,
		record.UserID, record.ChannelID, record.Action)
	for _, encoded := range []string{operation.EncryptedProviderTaskID, privateData.EncryptedProviderTaskID} {
		providerID, err := asyncTaskDecryptBound(encoded, binding)
		if err != nil || providerID != record.ProviderTaskID {
			return false
		}
	}
	return true
}
