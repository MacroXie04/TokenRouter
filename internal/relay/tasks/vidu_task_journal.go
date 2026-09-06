package tasks

import (
	"context"
	"errors"
	"fmt"
	"github.com/tokenrouter/tokenrouter/internal/platform/jsonutil"
	"github.com/tokenrouter/tokenrouter/internal/relay/providers/vidu"
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
	maxViduRecoveryJournalBytes          = 64 * 1024
	viduRecoveryJournalDiskVersion       = 1
	viduRecoveryJournalWriteAttempts     = 3
	viduRecoveryJournalPromotionLimit    = 100
	maxViduRecoveryJournalPromotionLimit = 1000
)

type viduRecoveryJournalRecord struct {
	Family         string      `json:"family"`
	Platform       string      `json:"platform"`
	TaskID         string      `json:"task_id"`
	ReservationID  string      `json:"reservation_id"`
	UserID         int         `json:"user_id"`
	ChannelID      int         `json:"channel_id"`
	Action         vidu.Action `json:"action"`
	ProviderTaskID string      `json:"provider_task_id"`
}

type viduRecoveryJournalDiskEnvelope struct {
	Version    int    `json:"version"`
	Action     string `json:"action"`
	Ciphertext string `json:"ciphertext"`
}

var viduRecoveryJournalPromotionCursor atomic.Uint64

func viduRecoveryJournalDirectory() string {
	if configured := strings.TrimSpace(os.Getenv("VIDU_TASK_RECOVERY_DIR")); configured != "" {
		return configured
	}
	if videoDirectory := strings.TrimSpace(os.Getenv("VIDEO_TASK_RECOVERY_DIR")); videoDirectory != "" {
		return filepath.Join(videoDirectory, "vidu-task-v1")
	}
	if sqlitePath := strings.TrimSpace(os.Getenv("SQLITE_PATH")); sqlitePath != "" {
		return filepath.Join(filepath.Dir(sqlitePath), ".tokenrouter-vidu-recovery")
	}
	return ".tokenrouter-vidu-recovery"
}

func viduRecoveryJournalPath(taskID string) (string, error) {
	if validateViduTaskPublicID(taskID) != nil || filepath.Base(taskID) != taskID {
		return "", errors.New("invalid Vidu recovery task id")
	}
	directory, err := validatedViduRecoveryJournalDirectory()
	if err != nil {
		return "", err
	}
	return filepath.Join(directory, taskID+".json"), nil
}

func validatedViduRecoveryJournalDirectory() (string, error) {
	return validateRecoveryJournalDirectory(viduRecoveryJournalDirectory(), "Vidu")
}

// persistAcceptedViduRecoveryJournal records the minimum accepted-provider
// identity without database I/O. This closes the outage window after the
// provider accepts a task but before the primary database can store its id.
func persistAcceptedViduRecoveryJournal(
	task *model.Task,
	reservationID, providerTaskID string,
	action vidu.Action,
) error {
	if task == nil || task.Platform != viduTaskPlatform {
		return errors.New("invalid Vidu recovery task")
	}
	record := viduRecoveryJournalRecord{
		Family: "vidu", Platform: viduTaskPlatform, TaskID: task.TaskID,
		ReservationID: strings.TrimSpace(reservationID), UserID: task.UserId,
		ChannelID: task.ChannelId, Action: action,
		ProviderTaskID: strings.TrimSpace(providerTaskID),
	}
	return persistViduRecoveryJournal(record)
}

func persistViduRecoveryJournal(record viduRecoveryJournalRecord) error {
	if err := validateViduRecoveryJournalRecord(record); err != nil {
		return err
	}
	path, err := viduRecoveryJournalPath(record.TaskID)
	if err != nil {
		return err
	}
	plaintext, err := jsonutil.Marshal(record)
	if err != nil {
		return errors.New("encode Vidu recovery journal")
	}
	ciphertext, err := asyncTaskEncryptBound(
		string(plaintext), viduRecoveryJournalBinding(record.TaskID, record.Action),
	)
	if err != nil {
		return errors.New("encrypt Vidu recovery journal")
	}
	data, err := jsonutil.Marshal(viduRecoveryJournalDiskEnvelope{
		Version: viduRecoveryJournalDiskVersion, Action: string(record.Action), Ciphertext: ciphertext,
	})
	if err != nil || len(data) > maxViduRecoveryJournalBytes {
		return errors.New("encode Vidu recovery journal envelope")
	}
	var lastErr error
	for attempt := 0; attempt < viduRecoveryJournalWriteAttempts; attempt++ {
		if err := persistVideoRecoveryJournalOnce(path, data); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if attempt < viduRecoveryJournalWriteAttempts-1 {
			time.Sleep(time.Duration(20*(attempt+1)) * time.Millisecond)
		}
	}
	return fmt.Errorf("persist Vidu recovery journal: %w", lastErr)
}

func loadViduRecoveryJournal(taskID string) (viduRecoveryJournalRecord, error) {
	path, err := viduRecoveryJournalPath(taskID)
	if err != nil {
		return viduRecoveryJournalRecord{}, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return viduRecoveryJournalRecord{}, err
	}
	if !info.Mode().IsRegular() {
		return viduRecoveryJournalRecord{}, errors.New("Vidu recovery journal is not a regular file")
	}
	file, err := openRecoveryJournalFile(path)
	if err != nil {
		return viduRecoveryJournalRecord{}, err
	}
	data, readErr := io.ReadAll(io.LimitReader(file, maxViduRecoveryJournalBytes+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil {
		return viduRecoveryJournalRecord{}, errors.Join(readErr, closeErr)
	}
	if len(data) > maxViduRecoveryJournalBytes {
		return viduRecoveryJournalRecord{}, errors.New("Vidu recovery journal is too large")
	}
	var disk viduRecoveryJournalDiskEnvelope
	if err := strictViduJSON(data, &disk); err != nil || disk.Version != viduRecoveryJournalDiskVersion ||
		strings.TrimSpace(disk.Ciphertext) == "" {
		return viduRecoveryJournalRecord{}, errors.New("invalid Vidu recovery journal envelope")
	}
	action, err := vidu.ParseAction(disk.Action)
	if err != nil {
		return viduRecoveryJournalRecord{}, errors.New("invalid Vidu recovery journal action")
	}
	plaintext, err := asyncTaskDecryptBound(
		disk.Ciphertext, viduRecoveryJournalBinding(taskID, action),
	)
	if err != nil {
		return viduRecoveryJournalRecord{}, errors.New("decrypt Vidu recovery journal")
	}
	var record viduRecoveryJournalRecord
	if err := strictViduJSON([]byte(plaintext), &record); err != nil ||
		record.TaskID != taskID || record.Action != action {
		return viduRecoveryJournalRecord{}, errors.New("decode Vidu recovery journal")
	}
	if err := validateViduRecoveryJournalRecord(record); err != nil {
		return viduRecoveryJournalRecord{}, err
	}
	return record, nil
}

func validateViduRecoveryJournalRecord(record viduRecoveryJournalRecord) error {
	if record.Family != "vidu" || record.Platform != viduTaskPlatform {
		return errors.New("invalid Vidu recovery provenance")
	}
	if !validViduAction(record.Action) {
		return errors.New("invalid Vidu recovery action")
	}
	if _, err := viduRecoveryJournalPath(record.TaskID); err != nil {
		return err
	}
	if record.UserID <= 0 || record.ChannelID <= 0 ||
		!validViduRecoveryOpaqueID(record.ReservationID, 64) ||
		!validViduRecoveryOpaqueID(record.ProviderTaskID, vidu.MaxProviderTaskIDBytes) {
		return errors.New("invalid Vidu recovery identity")
	}
	return nil
}

func validViduRecoveryOpaqueID(value string, maxBytes int) bool {
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

func removeViduRecoveryJournal(taskID string) error {
	path, err := viduRecoveryJournalPath(taskID)
	if err != nil {
		return err
	}
	var lastErr error
	for attempt := 0; attempt < viduRecoveryJournalWriteAttempts; attempt++ {
		if err := removeVideoRecoveryJournalOnce(path); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if attempt < viduRecoveryJournalWriteAttempts-1 {
			time.Sleep(time.Duration(20*(attempt+1)) * time.Millisecond)
		}
	}
	return fmt.Errorf("remove Vidu recovery journal: %w", lastErr)
}

// PromoteViduTaskRecoveryJournalsContext imports node-local accepted task ids
// before the cluster-wide reconciler runs. Journal evidence is never expired;
// it is removed only after the same provider identity is durable in the DB.
func PromoteViduTaskRecoveryJournalsContext(ctx context.Context) error {
	return promoteViduRecoveryJournalRecords(ctx, viduRecoveryJournalPromotionLimit)
}

func promoteViduRecoveryJournalRecords(ctx context.Context, limit int) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if limit <= 0 || limit > maxViduRecoveryJournalPromotionLimit {
		limit = viduRecoveryJournalPromotionLimit
	}
	directory, err := validatedViduRecoveryJournalDirectory()
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
		return fmt.Errorf("read Vidu recovery journal directory: %w", err)
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
		if _, err := viduRecoveryJournalPath(taskID); err != nil {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			promotionErrors = append(promotionErrors, errors.New("inspect Vidu recovery journal file"))
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
		start := int((viduRecoveryJournalPromotionCursor.Add(uint64(limit)) - uint64(limit)) % uint64(len(files)))
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
		record, err := loadViduRecoveryJournal(file.taskID)
		if err != nil {
			promotionErrors = append(promotionErrors,
				fmt.Errorf("promote Vidu recovery %s: unreadable encrypted journal", file.taskID))
			continue
		}
		remove, err := promoteViduRecoveryJournalRecord(ctx, record)
		if err != nil {
			promotionErrors = append(promotionErrors,
				fmt.Errorf("promote Vidu recovery %s: durable transition failed", file.taskID))
			continue
		}
		if remove {
			if err := removeViduRecoveryJournal(file.taskID); err != nil {
				promotionErrors = append(promotionErrors,
					fmt.Errorf("promote Vidu recovery %s: cleanup failed", file.taskID))
			}
		}
	}
	return errors.Join(promotionErrors...)
}

func promoteViduRecoveryJournalRecord(ctx context.Context, record viduRecoveryJournalRecord) (bool, error) {
	if model.DB == nil {
		return false, errors.New("Vidu recovery database is unavailable")
	}
	var operation model.TaskOperation
	if err := model.DB.WithContext(ctx).
		Where("task_id = ? AND reservation_id = ? AND platform = ?", record.TaskID,
			record.ReservationID, viduTaskPlatform).First(&operation).Error; err != nil {
		return false, err
	}
	if operation.UserID != record.UserID || operation.ChannelID != record.ChannelID {
		return false, errors.New("Vidu recovery operation identity mismatch")
	}
	var tasks []model.Task
	if err := model.DB.WithContext(ctx).Where("task_id = ? AND platform = ?", record.TaskID,
		viduTaskPlatform).Limit(2).Find(&tasks).Error; err != nil {
		return false, err
	}
	if len(tasks) != 1 || !viduTaskOperationIdentityMatches(&tasks[0], &operation) ||
		tasks[0].UserId != record.UserID || tasks[0].ChannelId != record.ChannelID {
		return false, errors.New("Vidu recovery task identity is unavailable or ambiguous")
	}
	properties, err := decodeViduTaskProperties(tasks[0].Properties)
	if err != nil || properties.Action != record.Action {
		return false, errors.New("Vidu recovery action mismatch")
	}
	if viduRecoveryProviderIDIsDurable(record, &tasks[0], &operation) {
		return true, nil
	}
	if operation.LeaseOwner != "" {
		return false, nil
	}
	switch operation.State {
	case model.TaskOperationDispatching, model.TaskOperationSubmitted,
		model.TaskOperationUnknown, model.TaskOperationManualReview:
		if err := viduRecoveryProviderIDsDoNotConflict(record, &tasks[0], &operation); err != nil {
			return false, err
		}
		if err := persistAcceptedViduFallback(&tasks[0], record.ReservationID, record.ProviderTaskID); err != nil {
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
		return viduRecoveryProviderIDIsDurable(record, &freshTask, &freshOperation), nil
	case model.TaskOperationTerminal, model.TaskOperationReversed:
		return false, errors.New("Vidu recovery provider identity is missing from terminal state")
	case model.TaskOperationPrepared, model.TaskOperationRefunded:
		return false, errors.New("Vidu recovery journal conflicts with a non-dispatched operation")
	default:
		return false, errors.New("Vidu recovery operation state is not promotable")
	}
}

func viduRecoveryProviderIDsDoNotConflict(
	record viduRecoveryJournalRecord,
	task *model.Task,
	operation *model.TaskOperation,
) error {
	privateData, err := decodeViduTaskPrivateData(task.PrivateData)
	if err != nil || privateData.RelayReservationID != record.ReservationID {
		return errors.New("Vidu recovery private identity mismatch")
	}
	binding := viduProviderTaskBinding(
		record.TaskID, record.ReservationID, record.UserID, record.ChannelID,
	)
	for _, encoded := range []string{operation.EncryptedProviderTaskID, privateData.EncryptedProviderTaskID} {
		if encoded == "" {
			continue
		}
		providerID, err := asyncTaskDecryptBound(encoded, binding)
		if err != nil || providerID != record.ProviderTaskID {
			return errors.New("Vidu recovery provider identity conflicts with durable state")
		}
	}
	return nil
}

func viduRecoveryProviderIDIsDurable(
	record viduRecoveryJournalRecord,
	task *model.Task,
	operation *model.TaskOperation,
) bool {
	if task == nil || operation == nil || operation.EncryptedProviderTaskID == "" ||
		!viduTaskOperationIdentityMatches(task, operation) {
		return false
	}
	privateData, err := decodeViduTaskPrivateData(task.PrivateData)
	if err != nil || privateData.EncryptedProviderTaskID == "" ||
		privateData.SettlementPending != operation.SettlementPending {
		return false
	}
	binding := viduProviderTaskBinding(
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
