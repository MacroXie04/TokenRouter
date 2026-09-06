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
	aliWan "github.com/tokenrouter/tokenrouter/relay/channel/task/ali"
)

const (
	maxAliWanRecoveryJournalBytes          = 64 * 1024
	aliWanRecoveryJournalDiskVersion       = 1
	aliWanRecoveryJournalWriteAttempts     = 3
	aliWanRecoveryJournalPromotionLimit    = 100
	maxAliWanRecoveryJournalPromotionLimit = 1000
)

type aliWanRecoveryJournalRecord struct {
	Family         string       `json:"family"`
	Platform       string       `json:"platform"`
	TaskID         string       `json:"task_id"`
	ReservationID  string       `json:"reservation_id"`
	UserID         int          `json:"user_id"`
	ChannelID      int          `json:"channel_id"`
	Action         aliWanAction `json:"action"`
	ProviderTaskID string       `json:"provider_task_id"`
}

type aliWanRecoveryJournalDiskEnvelope struct {
	Version    int    `json:"version"`
	Action     string `json:"action"`
	Ciphertext string `json:"ciphertext"`
}

var aliWanRecoveryJournalPromotionCursor atomic.Uint64

func aliWanRecoveryJournalDirectory() string {
	if configured := strings.TrimSpace(os.Getenv("ALI_VIDEO_TASK_RECOVERY_DIR")); configured != "" {
		return configured
	}
	if videoDirectory := strings.TrimSpace(os.Getenv("VIDEO_TASK_RECOVERY_DIR")); videoDirectory != "" {
		return filepath.Join(videoDirectory, "ali-video-v1")
	}
	if sqlitePath := strings.TrimSpace(os.Getenv("SQLITE_PATH")); sqlitePath != "" {
		return filepath.Join(filepath.Dir(sqlitePath), ".tokenrouter-ali-video-recovery")
	}
	return ".tokenrouter-ali-video-recovery"
}

func aliWanRecoveryJournalPath(taskID string) (string, error) {
	if validateAliWanTaskPublicID(taskID) != nil || filepath.Base(taskID) != taskID {
		return "", errors.New("invalid AliWan recovery task id")
	}
	directory, err := validatedAliWanRecoveryJournalDirectory()
	if err != nil {
		return "", err
	}
	return filepath.Join(directory, taskID+".json"), nil
}

func validatedAliWanRecoveryJournalDirectory() (string, error) {
	return validateRecoveryJournalDirectory(aliWanRecoveryJournalDirectory(), "AliWan")
}

// persistAcceptedAliWanRecoveryJournal records the minimum accepted-provider
// identity without database I/O. This closes the outage window after the
// provider accepts a task but before the primary database can store its id.
func persistAcceptedAliWanRecoveryJournal(
	task *model.Task,
	reservationID, providerTaskID string,
	action aliWanAction,
) error {
	if task == nil || task.Platform != aliWanTaskPlatform {
		return errors.New("invalid AliWan recovery task")
	}
	record := aliWanRecoveryJournalRecord{
		Family: "ali_wan", Platform: aliWanTaskPlatform, TaskID: task.TaskID,
		ReservationID: strings.TrimSpace(reservationID), UserID: task.UserId,
		ChannelID: task.ChannelId, Action: action,
		ProviderTaskID: strings.TrimSpace(providerTaskID),
	}
	return persistAliWanRecoveryJournal(record)
}

func persistAliWanRecoveryJournal(record aliWanRecoveryJournalRecord) error {
	if err := validateAliWanRecoveryJournalRecord(record); err != nil {
		return err
	}
	path, err := aliWanRecoveryJournalPath(record.TaskID)
	if err != nil {
		return err
	}
	plaintext, err := common.Marshal(record)
	if err != nil {
		return errors.New("encode AliWan recovery journal")
	}
	ciphertext, err := asyncTaskEncryptBound(
		string(plaintext), aliWanRecoveryJournalBinding(record.TaskID, record.Action),
	)
	if err != nil {
		return errors.New("encrypt AliWan recovery journal")
	}
	data, err := common.Marshal(aliWanRecoveryJournalDiskEnvelope{
		Version: aliWanRecoveryJournalDiskVersion, Action: string(record.Action), Ciphertext: ciphertext,
	})
	if err != nil || len(data) > maxAliWanRecoveryJournalBytes {
		return errors.New("encode AliWan recovery journal envelope")
	}
	var lastErr error
	for attempt := 0; attempt < aliWanRecoveryJournalWriteAttempts; attempt++ {
		if err := persistVideoRecoveryJournalOnce(path, data); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if attempt < aliWanRecoveryJournalWriteAttempts-1 {
			time.Sleep(time.Duration(20*(attempt+1)) * time.Millisecond)
		}
	}
	return fmt.Errorf("persist AliWan recovery journal: %w", lastErr)
}

func loadAliWanRecoveryJournal(taskID string) (aliWanRecoveryJournalRecord, error) {
	path, err := aliWanRecoveryJournalPath(taskID)
	if err != nil {
		return aliWanRecoveryJournalRecord{}, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return aliWanRecoveryJournalRecord{}, err
	}
	if !info.Mode().IsRegular() {
		return aliWanRecoveryJournalRecord{}, errors.New("AliWan recovery journal is not a regular file")
	}
	file, err := openRecoveryJournalFile(path)
	if err != nil {
		return aliWanRecoveryJournalRecord{}, err
	}
	data, readErr := io.ReadAll(io.LimitReader(file, maxAliWanRecoveryJournalBytes+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil {
		return aliWanRecoveryJournalRecord{}, errors.Join(readErr, closeErr)
	}
	if len(data) > maxAliWanRecoveryJournalBytes {
		return aliWanRecoveryJournalRecord{}, errors.New("AliWan recovery journal is too large")
	}
	var disk aliWanRecoveryJournalDiskEnvelope
	if err := strictAliWanJSON(data, &disk); err != nil || disk.Version != aliWanRecoveryJournalDiskVersion ||
		strings.TrimSpace(disk.Ciphertext) == "" {
		return aliWanRecoveryJournalRecord{}, errors.New("invalid AliWan recovery journal envelope")
	}
	action, err := parseAliWanAction(disk.Action)
	if err != nil {
		return aliWanRecoveryJournalRecord{}, errors.New("invalid AliWan recovery journal action")
	}
	plaintext, err := asyncTaskDecryptBound(
		disk.Ciphertext, aliWanRecoveryJournalBinding(taskID, action),
	)
	if err != nil {
		return aliWanRecoveryJournalRecord{}, errors.New("decrypt AliWan recovery journal")
	}
	var record aliWanRecoveryJournalRecord
	if err := strictAliWanJSON([]byte(plaintext), &record); err != nil ||
		record.TaskID != taskID || record.Action != action {
		return aliWanRecoveryJournalRecord{}, errors.New("decode AliWan recovery journal")
	}
	if err := validateAliWanRecoveryJournalRecord(record); err != nil {
		return aliWanRecoveryJournalRecord{}, err
	}
	return record, nil
}

func validateAliWanRecoveryJournalRecord(record aliWanRecoveryJournalRecord) error {
	if record.Family != "ali_wan" || record.Platform != aliWanTaskPlatform {
		return errors.New("invalid AliWan recovery provenance")
	}
	if !validAliWanAction(record.Action) {
		return errors.New("invalid AliWan recovery action")
	}
	if _, err := aliWanRecoveryJournalPath(record.TaskID); err != nil {
		return err
	}
	if record.UserID <= 0 || record.ChannelID <= 0 ||
		!validAliWanRecoveryOpaqueID(record.ReservationID, 64) ||
		!validAliWanRecoveryOpaqueID(record.ProviderTaskID, aliWan.MaxProviderTaskIDBytes) {
		return errors.New("invalid AliWan recovery identity")
	}
	return nil
}

func validAliWanRecoveryOpaqueID(value string, maxBytes int) bool {
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

func removeAliWanRecoveryJournal(taskID string) error {
	path, err := aliWanRecoveryJournalPath(taskID)
	if err != nil {
		return err
	}
	var lastErr error
	for attempt := 0; attempt < aliWanRecoveryJournalWriteAttempts; attempt++ {
		if err := removeVideoRecoveryJournalOnce(path); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if attempt < aliWanRecoveryJournalWriteAttempts-1 {
			time.Sleep(time.Duration(20*(attempt+1)) * time.Millisecond)
		}
	}
	return fmt.Errorf("remove AliWan recovery journal: %w", lastErr)
}

// PromoteAliWanTaskRecoveryJournalsContext imports node-local accepted task ids
// before the cluster-wide reconciler runs. Journal evidence is never expired;
// it is removed only after the same provider identity is durable in the DB.
func PromoteAliWanTaskRecoveryJournalsContext(ctx context.Context) error {
	return promoteAliWanRecoveryJournalRecords(ctx, aliWanRecoveryJournalPromotionLimit)
}

func promoteAliWanRecoveryJournalRecords(ctx context.Context, limit int) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if limit <= 0 || limit > maxAliWanRecoveryJournalPromotionLimit {
		limit = aliWanRecoveryJournalPromotionLimit
	}
	directory, err := validatedAliWanRecoveryJournalDirectory()
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
		return fmt.Errorf("read AliWan recovery journal directory: %w", err)
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
		if _, err := aliWanRecoveryJournalPath(taskID); err != nil {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			promotionErrors = append(promotionErrors, errors.New("inspect AliWan recovery journal file"))
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
		start := int((aliWanRecoveryJournalPromotionCursor.Add(uint64(limit)) - uint64(limit)) % uint64(len(files)))
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
		record, err := loadAliWanRecoveryJournal(file.taskID)
		if err != nil {
			promotionErrors = append(promotionErrors,
				fmt.Errorf("promote AliWan recovery %s: unreadable encrypted journal", file.taskID))
			continue
		}
		remove, err := promoteAliWanRecoveryJournalRecord(ctx, record)
		if err != nil {
			promotionErrors = append(promotionErrors,
				fmt.Errorf("promote AliWan recovery %s: durable transition failed", file.taskID))
			continue
		}
		if remove {
			if err := removeAliWanRecoveryJournal(file.taskID); err != nil {
				promotionErrors = append(promotionErrors,
					fmt.Errorf("promote AliWan recovery %s: cleanup failed", file.taskID))
			}
		}
	}
	return errors.Join(promotionErrors...)
}

func promoteAliWanRecoveryJournalRecord(ctx context.Context, record aliWanRecoveryJournalRecord) (bool, error) {
	if model.DB == nil {
		return false, errors.New("AliWan recovery database is unavailable")
	}
	var operation model.TaskOperation
	if err := model.DB.WithContext(ctx).
		Where("task_id = ? AND reservation_id = ? AND platform = ?", record.TaskID,
			record.ReservationID, aliWanTaskPlatform).First(&operation).Error; err != nil {
		return false, err
	}
	if operation.UserID != record.UserID || operation.ChannelID != record.ChannelID {
		return false, errors.New("AliWan recovery operation identity mismatch")
	}
	var tasks []model.Task
	if err := model.DB.WithContext(ctx).Where("task_id = ? AND platform = ?", record.TaskID,
		aliWanTaskPlatform).Limit(2).Find(&tasks).Error; err != nil {
		return false, err
	}
	if len(tasks) != 1 || !aliWanTaskOperationIdentityMatches(&tasks[0], &operation) ||
		tasks[0].UserId != record.UserID || tasks[0].ChannelId != record.ChannelID {
		return false, errors.New("AliWan recovery task identity is unavailable or ambiguous")
	}
	properties, err := decodeAliWanTaskProperties(tasks[0].Properties)
	if err != nil || properties.Action != record.Action {
		return false, errors.New("AliWan recovery action mismatch")
	}
	if aliWanRecoveryProviderIDIsDurable(record, &tasks[0], &operation) {
		return true, nil
	}
	if operation.LeaseOwner != "" {
		return false, nil
	}
	switch operation.State {
	case model.TaskOperationDispatching, model.TaskOperationSubmitted,
		model.TaskOperationUnknown, model.TaskOperationManualReview:
		if err := aliWanRecoveryProviderIDsDoNotConflict(record, &tasks[0], &operation); err != nil {
			return false, err
		}
		if err := persistAcceptedAliWanFallback(&tasks[0], record.ReservationID, record.ProviderTaskID); err != nil {
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
		return aliWanRecoveryProviderIDIsDurable(record, &freshTask, &freshOperation), nil
	case model.TaskOperationTerminal, model.TaskOperationReversed:
		return false, errors.New("AliWan recovery provider identity is missing from terminal state")
	case model.TaskOperationPrepared, model.TaskOperationRefunded:
		return false, errors.New("AliWan recovery journal conflicts with a non-dispatched operation")
	default:
		return false, errors.New("AliWan recovery operation state is not promotable")
	}
}

func aliWanRecoveryProviderIDsDoNotConflict(
	record aliWanRecoveryJournalRecord,
	task *model.Task,
	operation *model.TaskOperation,
) error {
	privateData, err := decodeAliWanTaskPrivateData(task.PrivateData)
	if err != nil || privateData.RelayReservationID != record.ReservationID {
		return errors.New("AliWan recovery private identity mismatch")
	}
	binding := aliWanProviderTaskBinding(
		record.TaskID, record.ReservationID, record.UserID, record.ChannelID,
	)
	for _, encoded := range []string{operation.EncryptedProviderTaskID, privateData.EncryptedProviderTaskID} {
		if encoded == "" {
			continue
		}
		providerID, err := asyncTaskDecryptBound(encoded, binding)
		if err != nil || providerID != record.ProviderTaskID {
			return errors.New("AliWan recovery provider identity conflicts with durable state")
		}
	}
	return nil
}

func aliWanRecoveryProviderIDIsDurable(
	record aliWanRecoveryJournalRecord,
	task *model.Task,
	operation *model.TaskOperation,
) bool {
	if task == nil || operation == nil || operation.EncryptedProviderTaskID == "" ||
		!aliWanTaskOperationIdentityMatches(task, operation) {
		return false
	}
	privateData, err := decodeAliWanTaskPrivateData(task.PrivateData)
	if err != nil || privateData.EncryptedProviderTaskID == "" ||
		privateData.SettlementPending != operation.SettlementPending {
		return false
	}
	binding := aliWanProviderTaskBinding(
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
