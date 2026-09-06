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
	"unicode/utf8"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/relay/channel/task/doubao"
)

const (
	maxDoubaoRecoveryJournalBytes          = 64 * 1024
	doubaoRecoveryJournalDiskVersion       = 1
	doubaoRecoveryJournalPromotionLimit    = 100
	maxDoubaoRecoveryJournalPromotionLimit = 1000
)

type doubaoRecoveryJournalRecord struct {
	Family         string        `json:"family"`
	Platform       string        `json:"platform"`
	TaskID         string        `json:"task_id"`
	ReservationID  string        `json:"reservation_id"`
	UserID         int           `json:"user_id"`
	ChannelID      int           `json:"channel_id"`
	Action         doubao.Action `json:"action"`
	ProviderTaskID string        `json:"provider_task_id"`
}

type doubaoRecoveryJournalDiskEnvelope struct {
	Version    int    `json:"version"`
	Platform   string `json:"platform"`
	Action     string `json:"action"`
	Ciphertext string `json:"ciphertext"`
}

var doubaoRecoveryJournalPromotionCursor atomic.Uint64

func doubaoRecoveryJournalDirectory() string {
	if configured := strings.TrimSpace(os.Getenv("DOUBAO_TASK_RECOVERY_DIR")); configured != "" {
		return configured
	}
	if shared := strings.TrimSpace(os.Getenv("VIDEO_TASK_RECOVERY_DIR")); shared != "" {
		return filepath.Join(shared, "doubao-video-v1")
	}
	if sqlitePath := strings.TrimSpace(os.Getenv("SQLITE_PATH")); sqlitePath != "" {
		return filepath.Join(filepath.Dir(sqlitePath), ".tokenrouter-doubao-recovery")
	}
	return ".tokenrouter-doubao-recovery"
}

func validatedDoubaoRecoveryJournalDirectory() (string, error) {
	return validateRecoveryJournalDirectory(doubaoRecoveryJournalDirectory(), "Doubao")
}

func doubaoRecoveryJournalPath(taskID string) (string, error) {
	if err := validateDoubaoTaskPublicID(taskID); err != nil || filepath.Base(taskID) != taskID {
		return "", errors.New("invalid Doubao recovery task ID")
	}
	directory, err := validatedDoubaoRecoveryJournalDirectory()
	if err != nil {
		return "", err
	}
	return filepath.Join(directory, taskID+".json"), nil
}

func persistAcceptedDoubaoRecoveryJournal(
	task *model.Task,
	reservationID, providerTaskID string,
	action doubao.Action,
) error {
	if task == nil || !model.IsDoubaoVideoTaskOperationPlatform(task.Platform) || task.TaskID == "" || task.UserId <= 0 || task.ChannelId <= 0 {
		return errors.New("invalid Doubao recovery task")
	}
	record := doubaoRecoveryJournalRecord{
		Family: "doubao", Platform: task.Platform, TaskID: task.TaskID,
		ReservationID: strings.TrimSpace(reservationID), UserID: task.UserId, ChannelID: task.ChannelId,
		Action: action, ProviderTaskID: strings.TrimSpace(providerTaskID),
	}
	return persistDoubaoRecoveryJournal(record)
}

func persistDoubaoRecoveryJournal(record doubaoRecoveryJournalRecord) error {
	if err := validateDoubaoRecoveryJournalRecord(record); err != nil {
		return err
	}
	path, err := doubaoRecoveryJournalPath(record.TaskID)
	if err != nil {
		return err
	}
	plaintext, err := common.Marshal(record)
	if err != nil {
		return err
	}
	ciphertext, err := asyncTaskEncryptBound(string(plaintext), doubaoRecoveryJournalBinding(record.TaskID, record.Platform, record.Action))
	if err != nil {
		return errors.New("encrypt Doubao recovery journal")
	}
	data, err := common.Marshal(doubaoRecoveryJournalDiskEnvelope{
		Version: doubaoRecoveryJournalDiskVersion, Platform: record.Platform,
		Action: string(record.Action), Ciphertext: ciphertext,
	})
	if err != nil {
		return err
	}
	if len(data) > maxDoubaoRecoveryJournalBytes {
		return errors.New("Doubao recovery journal is too large")
	}
	if err := persistVideoRecoveryJournalOnce(path, data); err != nil {
		return fmt.Errorf("persist Doubao recovery journal: %w", err)
	}
	return nil
}

func loadDoubaoRecoveryJournal(taskID string) (doubaoRecoveryJournalRecord, error) {
	path, err := doubaoRecoveryJournalPath(taskID)
	if err != nil {
		return doubaoRecoveryJournalRecord{}, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return doubaoRecoveryJournalRecord{}, err
	}
	if !info.Mode().IsRegular() {
		return doubaoRecoveryJournalRecord{}, errors.New("Doubao recovery journal is not a regular file")
	}
	file, err := openRecoveryJournalFile(path)
	if err != nil {
		return doubaoRecoveryJournalRecord{}, err
	}
	data, readErr := io.ReadAll(io.LimitReader(file, maxDoubaoRecoveryJournalBytes+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil {
		return doubaoRecoveryJournalRecord{}, errors.Join(readErr, closeErr)
	}
	if len(data) > maxDoubaoRecoveryJournalBytes {
		return doubaoRecoveryJournalRecord{}, errors.New("Doubao recovery journal is too large")
	}
	var disk doubaoRecoveryJournalDiskEnvelope
	if err := common.Unmarshal(data, &disk); err != nil || disk.Version != doubaoRecoveryJournalDiskVersion ||
		!model.IsDoubaoVideoTaskOperationPlatform(disk.Platform) ||
		strings.TrimSpace(disk.Ciphertext) == "" {
		return doubaoRecoveryJournalRecord{}, errors.New("invalid Doubao recovery journal envelope")
	}
	action := doubao.Action(disk.Action)
	if !validDoubaoAction(action) {
		return doubaoRecoveryJournalRecord{}, errors.New("invalid Doubao recovery journal action")
	}
	plaintext, err := asyncTaskDecryptBound(disk.Ciphertext, doubaoRecoveryJournalBinding(taskID, disk.Platform, action))
	if err != nil {
		return doubaoRecoveryJournalRecord{}, errors.New("decrypt Doubao recovery journal")
	}
	var record doubaoRecoveryJournalRecord
	if err := common.Unmarshal([]byte(plaintext), &record); err != nil || record.TaskID != taskID ||
		record.Platform != disk.Platform || record.Action != action {
		return doubaoRecoveryJournalRecord{}, errors.New("decode Doubao recovery journal")
	}
	if err := validateDoubaoRecoveryJournalRecord(record); err != nil {
		return doubaoRecoveryJournalRecord{}, err
	}
	return record, nil
}

func validateDoubaoRecoveryJournalRecord(record doubaoRecoveryJournalRecord) error {
	if record.Family != "doubao" || !model.IsDoubaoVideoTaskOperationPlatform(record.Platform) || !validDoubaoAction(record.Action) {
		return errors.New("invalid Doubao recovery provenance")
	}
	if _, err := doubaoRecoveryJournalPath(record.TaskID); err != nil {
		return err
	}
	if record.UserID <= 0 || record.ChannelID <= 0 ||
		!validDoubaoRecoveryOpaqueID(record.ReservationID, 64) ||
		!validDoubaoRecoveryOpaqueID(record.ProviderTaskID, doubao.MaxProviderTaskIDBytes) {
		return errors.New("invalid Doubao recovery identity")
	}
	return nil
}

func validDoubaoRecoveryOpaqueID(value string, maxBytes int) bool {
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

func removeDoubaoRecoveryJournal(taskID string) error {
	path, err := doubaoRecoveryJournalPath(taskID)
	if err != nil {
		return err
	}
	return removeVideoRecoveryJournalOnce(path)
}

// PromoteDoubaoTaskRecoveryJournalsContext imports node-local provider IDs
// before the cluster-wide poller. Evidence is never age-deleted.
func PromoteDoubaoTaskRecoveryJournalsContext(ctx context.Context) error {
	return promoteDoubaoRecoveryJournalRecords(ctx, doubaoRecoveryJournalPromotionLimit)
}

func promoteDoubaoRecoveryJournalRecords(ctx context.Context, limit int) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if limit <= 0 || limit > maxDoubaoRecoveryJournalPromotionLimit {
		limit = doubaoRecoveryJournalPromotionLimit
	}
	directory, err := validatedDoubaoRecoveryJournalDirectory()
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
		return fmt.Errorf("read Doubao recovery journal directory: %w", err)
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
		if _, err := doubaoRecoveryJournalPath(taskID); err != nil {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			promotionErrors = append(promotionErrors, errors.New("inspect Doubao recovery journal file"))
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
		start := int((doubaoRecoveryJournalPromotionCursor.Add(uint64(limit)) - uint64(limit)) % uint64(len(files)))
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
		record, err := loadDoubaoRecoveryJournal(file.taskID)
		if err != nil {
			promotionErrors = append(promotionErrors, fmt.Errorf("promote Doubao recovery %s: unreadable encrypted journal", file.taskID))
			continue
		}
		remove, err := promoteDoubaoRecoveryJournalRecord(ctx, record)
		if err != nil {
			promotionErrors = append(promotionErrors, fmt.Errorf("promote Doubao recovery %s: durable transition failed", file.taskID))
			continue
		}
		if remove {
			if err := removeDoubaoRecoveryJournal(file.taskID); err != nil {
				promotionErrors = append(promotionErrors, fmt.Errorf("promote Doubao recovery %s: cleanup failed", file.taskID))
			}
		}
	}
	return errors.Join(promotionErrors...)
}

func promoteDoubaoRecoveryJournalRecord(ctx context.Context, record doubaoRecoveryJournalRecord) (bool, error) {
	if model.DB == nil {
		return false, errors.New("Doubao recovery database is unavailable")
	}
	var operation model.TaskOperation
	if err := model.DB.WithContext(ctx).Where("task_id = ? AND reservation_id = ? AND platform = ?",
		record.TaskID, record.ReservationID, record.Platform).First(&operation).Error; err != nil {
		return false, err
	}
	if operation.UserID != record.UserID || operation.ChannelID != record.ChannelID {
		return false, errors.New("Doubao recovery operation identity mismatch")
	}
	task, err := loadUniqueDoubaoRecoveryTask(ctx, &operation)
	if err != nil {
		return false, err
	}
	properties, err := decodeDoubaoTaskProperties(task.Properties)
	if err != nil || properties.Action != record.Action {
		return false, errors.New("Doubao recovery action mismatch")
	}
	if doubaoRecoveryProviderIDIsDurable(record, task, &operation) {
		return true, nil
	}
	if operation.LeaseOwner != "" {
		return false, nil
	}
	switch operation.State {
	case model.TaskOperationDispatching, model.TaskOperationSubmitted, model.TaskOperationManualReview:
		provider := &doubao.Task{ProviderTaskID: record.ProviderTaskID, Status: doubao.StatusSubmitted}
		if err := persistDoubaoProviderStatePending(task, record.ReservationID, provider,
			operation.State, ""); err != nil {
			return false, err
		}
		var freshOperation model.TaskOperation
		if err := model.DB.WithContext(ctx).Where("id = ?", operation.ID).First(&freshOperation).Error; err != nil {
			return false, err
		}
		freshTask, err := loadUniqueDoubaoRecoveryTask(ctx, &freshOperation)
		return err == nil && doubaoRecoveryProviderIDIsDurable(record, freshTask, &freshOperation), err
	case model.TaskOperationTerminal, model.TaskOperationRefunded:
		return false, errors.New("Doubao recovery journal conflicts with terminal state")
	default:
		return false, errors.New("Doubao recovery operation state is not promotable")
	}
}

func doubaoRecoveryProviderIDIsDurable(
	record doubaoRecoveryJournalRecord,
	task *model.Task,
	operation *model.TaskOperation,
) bool {
	if task == nil || operation == nil || !doubaoTaskOperationIdentityMatches(task, operation, record.ReservationID) {
		return false
	}
	privateData, err := decodeDoubaoTaskPrivateData(task.PrivateData)
	if err != nil || privateData.EncryptedUpstreamTaskID == "" || operation.EncryptedProviderTaskID == "" {
		return false
	}
	binding := doubaoProviderTaskBinding(record.TaskID, record.ReservationID, record.Platform, record.UserID, record.ChannelID, record.Action)
	for _, encoded := range []string{privateData.EncryptedUpstreamTaskID, operation.EncryptedProviderTaskID} {
		providerID, err := asyncTaskDecryptBound(encoded, binding)
		if err != nil || providerID != record.ProviderTaskID {
			return false
		}
	}
	return true
}
