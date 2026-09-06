package relay

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/service"
)

const (
	maxJimengRecoveryRecordBytes = 64 * 1024
	jimengRecoveryAttempts       = 3
	jimengRecoveryDiskVersion    = 2
	jimengRecoveryCleanupLimit   = 100
	jimengRecoveryRetentionDays  = 7
)

type jimengRecoveryDiskEnvelope struct {
	Version    int    `json:"version"`
	Ciphertext string `json:"ciphertext"`
}

var (
	jimengRecoveryPromotionCursor atomic.Uint64
	jimengRecoveryCleanupCursor   atomic.Uint64
)

func jimengRecoveryDirectory() string {
	if configured := strings.TrimSpace(os.Getenv("JIMENG_RECOVERY_DIR")); configured != "" {
		return configured
	}
	if sqlitePath := strings.TrimSpace(os.Getenv("SQLITE_PATH")); sqlitePath != "" {
		return filepath.Join(filepath.Dir(sqlitePath), ".tokenrouter-jimeng-recovery")
	}
	return ".tokenrouter-jimeng-recovery"
}

func validatedJimengRecoveryDirectory() (string, error) {
	return validateRecoveryJournalDirectory(jimengRecoveryDirectory(), "Jimeng")
}

func jimengRecoveryPath(taskID string) (string, error) {
	if taskID == "" || filepath.Base(taskID) != taskID || !strings.HasPrefix(taskID, "task_") {
		return "", errors.New("invalid Jimeng recovery task ID")
	}
	for _, r := range taskID {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '_' {
			return "", errors.New("invalid Jimeng recovery task ID")
		}
	}
	directory, err := validatedJimengRecoveryDirectory()
	if err != nil {
		return "", err
	}
	return filepath.Join(directory, taskID+".json"), nil
}

func persistJimengRecovery(envelope jimengRecoveryEnvelope) error {
	if err := validateJimengRecoveryEnvelope(envelope); err != nil {
		return err
	}
	path, err := jimengRecoveryPath(envelope.TaskID)
	if err != nil {
		return err
	}
	plaintext, err := marshalJimengRecoveryEnvelope(envelope)
	if err != nil {
		return err
	}
	ciphertext, err := jimengEncrypt(string(plaintext))
	if err != nil {
		return errors.New("encrypt Jimeng recovery record")
	}
	data, err := common.Marshal(jimengRecoveryDiskEnvelope{
		Version: jimengRecoveryDiskVersion, Ciphertext: ciphertext,
	})
	if err != nil {
		return err
	}
	if len(data) > maxJimengRecoveryRecordBytes {
		return errors.New("Jimeng recovery record is too large")
	}
	var lastErr error
	for attempt := 0; attempt < jimengRecoveryAttempts; attempt++ {
		if err := persistJimengRecoveryOnce(path, data); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if attempt < jimengRecoveryAttempts-1 {
			time.Sleep(time.Duration(20*(attempt+1)) * time.Millisecond)
		}
	}
	return fmt.Errorf("persist Jimeng recovery record: %w", lastErr)
}

func persistJimengRecoveryOnce(path string, data []byte) (returnErr error) {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".jimeng-recovery-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	closed := false
	renamed := false
	defer func() {
		if !closed {
			if err := temporary.Close(); err != nil {
				returnErr = errors.Join(returnErr, fmt.Errorf("close Jimeng recovery temporary file: %w", err))
			}
		}
		if !renamed {
			if err := os.Remove(temporaryPath); err != nil && !errors.Is(err, os.ErrNotExist) {
				returnErr = errors.Join(returnErr, fmt.Errorf("remove Jimeng recovery temporary file: %w", err))
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
	closeErr := temporary.Close()
	closed = true
	if closeErr != nil {
		return closeErr
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	renamed = true
	return syncJimengRecoveryDirectory(directory)
}

func loadJimengRecovery(taskID string) (jimengRecoveryEnvelope, error) {
	path, err := jimengRecoveryPath(taskID)
	if err != nil {
		return jimengRecoveryEnvelope{}, err
	}
	file, err := openRecoveryJournalFile(path)
	if err != nil {
		return jimengRecoveryEnvelope{}, err
	}
	data, err := io.ReadAll(io.LimitReader(file, maxJimengRecoveryRecordBytes+1))
	closeErr := file.Close()
	if err != nil {
		return jimengRecoveryEnvelope{}, errors.Join(err, closeErr)
	}
	if closeErr != nil {
		return jimengRecoveryEnvelope{}, closeErr
	}
	if len(data) > maxJimengRecoveryRecordBytes {
		return jimengRecoveryEnvelope{}, errors.New("Jimeng recovery record is too large")
	}
	envelope, err := decodeJimengRecoveryDiskEnvelope(data)
	if err != nil {
		return jimengRecoveryEnvelope{}, err
	}
	if err := validateJimengRecoveryEnvelope(envelope); err != nil {
		return jimengRecoveryEnvelope{}, err
	}
	var disk jimengRecoveryDiskEnvelope
	versioned := common.Unmarshal(data, &disk) == nil && disk.Version != 0
	if !versioned {
		// Valid plaintext compatibility records are immediately rewritten through
		// the atomic 0600/versioned encryption path. A recent record that cannot
		// yet be promoted must not remain readable in plaintext until retention.
		if err := persistJimengRecovery(envelope); err != nil {
			return jimengRecoveryEnvelope{}, errors.New("upgrade legacy Jimeng recovery record")
		}
	}
	return envelope, nil
}

func decodeJimengRecoveryDiskEnvelope(data []byte) (jimengRecoveryEnvelope, error) {
	var disk jimengRecoveryDiskEnvelope
	if err := common.Unmarshal(data, &disk); err == nil && disk.Version != 0 {
		if disk.Version != jimengRecoveryDiskVersion || strings.TrimSpace(disk.Ciphertext) == "" {
			return jimengRecoveryEnvelope{}, errors.New("unsupported Jimeng recovery record version")
		}
		plaintext, err := jimengDecrypt(disk.Ciphertext)
		if err != nil {
			return jimengRecoveryEnvelope{}, errors.New("decrypt Jimeng recovery record")
		}
		return unmarshalJimengRecoveryEnvelope([]byte(plaintext))
	}
	// Read-only compatibility for node-local records written before encrypted
	// disk envelopes were introduced. Every subsequent write uses version 2.
	return unmarshalJimengRecoveryEnvelope(data)
}

func validateJimengRecoveryEnvelope(envelope jimengRecoveryEnvelope) error {
	if envelope.UserID <= 0 || envelope.ChannelID <= 0 {
		return errors.New("invalid Jimeng recovery owner or channel")
	}
	if _, err := jimengRecoveryPath(envelope.TaskID); err != nil {
		return err
	}
	if envelope.Status != model.TaskStatusUnknown && envelope.Status != model.TaskStatusSubmitted &&
		envelope.Status != model.TaskStatusFailure {
		return errors.New("invalid Jimeng recovery status")
	}
	if envelope.Status == model.TaskStatusSubmitted && strings.TrimSpace(envelope.UpstreamTaskID) == "" {
		return errors.New("submitted Jimeng recovery is missing upstream task ID")
	}
	return nil
}

func jimengRecoveryNotFound(err error) bool {
	return errors.Is(err, os.ErrNotExist)
}

func removeJimengRecovery(taskID string) error {
	path, err := jimengRecoveryPath(taskID)
	if err != nil {
		return err
	}
	var lastErr error
	for attempt := 0; attempt < jimengRecoveryAttempts; attempt++ {
		if err := removeJimengRecoveryOnce(path); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if attempt < jimengRecoveryAttempts-1 {
			time.Sleep(time.Duration(20*(attempt+1)) * time.Millisecond)
		}
	}
	return fmt.Errorf("remove Jimeng recovery record: %w", lastErr)
}

func removeJimengRecoveryOnce(path string) error {
	directory := filepath.Dir(path)
	if err := os.Remove(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return syncJimengRecoveryDirectory(directory)
		}
		return err
	}
	return syncJimengRecoveryDirectory(directory)
}

// promoteJimengRecoveryRecords copies an emergency same-node provider outcome
// into the primary database before cluster workers inspect ambiguous dispatches.
// A different node cannot see this directory, so an absent journal is never
// treated as evidence that the provider accepted or rejected the request.
func promoteJimengRecoveryRecords(now int64, limit int) error {
	if now <= 0 || model.DB == nil {
		return errors.New("invalid Jimeng recovery promotion state")
	}
	if limit <= 0 {
		limit = jimengRecoveryCleanupLimit
	}
	directory, err := validatedJimengRecoveryDirectory()
	if err != nil {
		return err
	}
	entries, err := readBoundedRecoveryJournalDirectory(
		nil, directory, maximumRecoveryJournalDirectoryReadEntries,
	)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read Jimeng recovery directory for promotion: %w", err)
	}
	type recoveryRecord struct {
		taskID string
		info   os.FileInfo
	}
	records := make([]recoveryRecord, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, "task_") || filepath.Ext(name) != ".json" {
			continue
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			continue
		}
		if info.Mode().IsRegular() {
			records = append(records, recoveryRecord{
				taskID: strings.TrimSuffix(name, ".json"), info: info,
			})
		}
	}
	sort.Slice(records, func(i, j int) bool {
		left, right := records[i].info.ModTime(), records[j].info.ModTime()
		if left.Equal(right) {
			return records[i].taskID < records[j].taskID
		}
		return left.Before(right)
	})
	if len(records) > limit {
		// Rotate the bounded window so unreadable, mismatched, or continuously
		// leased old records cannot monopolize every periodic pass.
		start := int((jimengRecoveryPromotionCursor.Add(uint64(limit)) - uint64(limit)) % uint64(len(records)))
		window := make([]recoveryRecord, 0, limit)
		for offset := 0; offset < limit; offset++ {
			window = append(window, records[(start+offset)%len(records)])
		}
		records = window
	}
	var promotionErrors []error
	for _, record := range records {
		envelope, loadErr := loadJimengRecovery(record.taskID)
		if loadErr != nil {
			promotionErrors = append(promotionErrors,
				fmt.Errorf("promote Jimeng recovery %s: unreadable encrypted record", record.taskID))
			continue
		}
		if envelope.Status != model.TaskStatusSubmitted && envelope.Status != model.TaskStatusFailure {
			continue
		}
		var operation model.JimengTaskOperation
		lookup := model.DB.Where("task_id = ?", record.taskID).First(&operation)
		if errors.Is(lookup.Error, gorm.ErrRecordNotFound) {
			continue
		}
		if lookup.Error != nil {
			promotionErrors = append(promotionErrors,
				fmt.Errorf("promote Jimeng recovery %s: operation lookup failed", record.taskID))
			continue
		}
		if operation.UserID != envelope.UserID || operation.ChannelID != envelope.ChannelID ||
			operation.TaskID != envelope.TaskID {
			promotionErrors = append(promotionErrors,
				fmt.Errorf("promote Jimeng recovery %s: identity mismatch", record.taskID))
			continue
		}
		if operation.State != model.JimengTaskOperationDispatching &&
			operation.State != model.JimengTaskOperationSubmitted &&
			operation.State != model.JimengTaskOperationManualReview {
			continue
		}
		claimed, claimedOK, claimErr := claimJimengTaskOperationForClient(
			operation.TaskID, operation.ReservationID,
			[]string{
				model.JimengTaskOperationDispatching,
				model.JimengTaskOperationSubmitted,
				model.JimengTaskOperationManualReview,
			},
		)
		if claimErr != nil {
			promotionErrors = append(promotionErrors,
				fmt.Errorf("promote Jimeng recovery %s: claim failed", record.taskID))
			continue
		}
		if !claimedOK {
			continue
		}
		var task model.Task
		if err := model.DB.Where("task_id = ? AND user_id = ?", operation.TaskID, operation.UserID).
			First(&task).Error; err != nil {
			_ = releaseClaimedJimengOperation(claimed, now, nil)
			promotionErrors = append(promotionErrors,
				fmt.Errorf("promote Jimeng recovery %s: task lookup failed", record.taskID))
			continue
		}
		reservation, restoreErr := service.RestoreRelayQuotaReservation(operation.ReservationID)
		if restoreErr != nil {
			_ = releaseClaimedJimengOperation(claimed, now, nil)
			promotionErrors = append(promotionErrors,
				fmt.Errorf("promote Jimeng recovery %s: reservation lookup failed", record.taskID))
			continue
		}
		var promoteErr error
		switch envelope.Status {
		case model.TaskStatusFailure:
			promoteErr = refundJimengTaskReservationWithFence(
				&task, reservation, "provider rejected submission before recovery",
				[]string{claimed.State}, claimed.LeaseOwner,
			)
		case model.TaskStatusSubmitted:
			privateData, decodeErr := decodeJimengTaskPrivateData(task.PrivateData)
			if decodeErr != nil || privateData.RelayReservationID != operation.ReservationID ||
				task.ChannelId != operation.ChannelID || strings.TrimSpace(envelope.UpstreamTaskID) == "" ||
				(privateData.UpstreamTaskID != "" && privateData.UpstreamTaskID != envelope.UpstreamTaskID) {
				promoteErr = errors.New("Jimeng accepted recovery metadata mismatch")
				break
			}
			privateData.UpstreamTaskID = envelope.UpstreamTaskID
			privateData.SettlementPending = false
			privateJSON, encodeErr := marshalJimengTaskPrivateData(privateData)
			if encodeErr != nil {
				promoteErr = encodeErr
				break
			}
			task.Status = model.TaskStatusSubmitted
			task.FailReason = ""
			promoteErr = settleJimengAcceptedTask(
				&task, reservation, privateJSON, []byte(task.Data), model.TaskStatusSubmitted, "",
				claimed.State, claimed.LeaseOwner,
			)
			if promoteErr == nil {
				claimed.State = model.JimengTaskOperationSubmitted
				claimed.SettlementPending = false
				releaseNow, clockErr := model.DatabaseUnixTimestamp(model.DB)
				if clockErr != nil {
					promoteErr = clockErr
				} else {
					released := model.DB.Model(&model.JimengTaskOperation{}).
						Where("id = ? AND state = ? AND lease_owner = ?", claimed.ID,
							model.JimengTaskOperationSubmitted, claimed.LeaseOwner).
						Updates(map[string]any{
							"lease_owner": "", "lease_expires_at": 0,
							"next_attempt_at": releaseNow, "updated_at": releaseNow,
						})
					if released.Error != nil {
						promoteErr = released.Error
					} else if released.RowsAffected != 1 {
						promoteErr = service.ErrRelayQuotaReservationBusy
					}
				}
			}
		}
		if promoteErr != nil {
			_ = releaseClaimedJimengOperation(claimed, now, nil)
			promotionErrors = append(promotionErrors,
				fmt.Errorf("promote Jimeng recovery %s: durable transition failed", record.taskID))
			continue
		}
		if removeErr := removeJimengRecovery(record.taskID); removeErr != nil {
			promotionErrors = append(promotionErrors,
				fmt.Errorf("promote Jimeng recovery %s: cleanup failed", record.taskID))
		}
	}
	return errors.Join(promotionErrors...)
}

// cleanupJimengRecoveryRecords bounds the lifetime of the node-local emergency
// journal. Primary-database terminal decisions win immediately; all remaining
// records expire after the retention horizon. Cleanup is bounded so a large or
// damaged directory cannot monopolize the periodic recovery job.
func cleanupJimengRecoveryRecords(now int64, limit int) error {
	if now <= 0 {
		return errors.New("invalid Jimeng recovery cleanup time")
	}
	if limit <= 0 {
		limit = jimengRecoveryCleanupLimit
	}
	directory, err := validatedJimengRecoveryDirectory()
	if err != nil {
		return err
	}
	entries, err := readBoundedRecoveryJournalDirectory(
		nil, directory, maximumRecoveryJournalDirectoryReadEntries,
	)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read Jimeng recovery directory: %w", err)
	}
	retentionDays := common.GetEnvInt("JIMENG_RECOVERY_RETENTION_DAYS", jimengRecoveryRetentionDays)
	if retentionDays < 1 || retentionDays > 90 {
		retentionDays = jimengRecoveryRetentionDays
	}
	cutoff := now - int64(retentionDays*24*60*60)
	var cleanupErrors []error
	type recoveryRecord struct {
		name      string
		info      os.FileInfo
		temporary bool
	}
	records := make([]recoveryRecord, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		journal := strings.HasPrefix(name, "task_") && filepath.Ext(name) == ".json"
		temporary := strings.HasPrefix(name, ".jimeng-recovery-")
		if entry.IsDir() || (!journal && !temporary) {
			continue
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("inspect Jimeng recovery record: %w", infoErr))
			continue
		}
		if !info.Mode().IsRegular() {
			continue
		}
		records = append(records, recoveryRecord{name: name, info: info, temporary: temporary})
	}
	// Oldest-first ordering prevents a stable prefix of recent active records
	// from starving expired or terminal records later in lexical order.
	sort.Slice(records, func(i, j int) bool {
		left, right := records[i].info.ModTime(), records[j].info.ModTime()
		if left.Equal(right) {
			return records[i].name < records[j].name
		}
		return left.Before(right)
	})
	if len(records) > limit {
		start := int((jimengRecoveryCleanupCursor.Add(uint64(limit)) - uint64(limit)) % uint64(len(records)))
		window := make([]recoveryRecord, 0, limit)
		for offset := 0; offset < limit; offset++ {
			window = append(window, records[(start+offset)%len(records)])
		}
		records = window
	}
	removedTemporary := false
	for _, record := range records {
		aged := record.info.ModTime().Unix() <= cutoff
		if record.temporary {
			if aged {
				if removeErr := os.Remove(filepath.Join(directory, record.name)); removeErr != nil &&
					!errors.Is(removeErr, os.ErrNotExist) {
					cleanupErrors = append(cleanupErrors,
						fmt.Errorf("remove stale Jimeng recovery temporary file: %w", removeErr))
				} else {
					removedTemporary = true
				}
			}
			continue
		}
		taskID := strings.TrimSuffix(record.name, ".json")
		remove := aged && model.DB == nil
		if model.DB != nil {
			var operation model.JimengTaskOperation
			lookup := model.DB.Where("task_id = ?", taskID).First(&operation)
			switch {
			case lookup.Error == nil:
				// An active operation can still depend on this secondary accepted
				// outcome after a prolonged primary-database outage or backlog. Its
				// durable state, not file age, decides retention.
				active := operation.State == model.JimengTaskOperationPrepared ||
					operation.State == model.JimengTaskOperationDispatching ||
					operation.State == model.JimengTaskOperationSubmitted
				remove = !active && (operation.State == model.JimengTaskOperationUnknown ||
					operation.State == model.JimengTaskOperationTerminal ||
					operation.State == model.JimengTaskOperationRefunded)
				if operation.State == model.JimengTaskOperationManualReview {
					// A different node may have quarantined an unresolved dispatch
					// without seeing this node's accepted/rejected journal. Keep the
					// record through its bounded retention window so promotion can win.
					remove = aged
				}
			case errors.Is(lookup.Error, gorm.ErrRecordNotFound):
				// A recent compatibility record may precede its task transaction;
				// retention cleanup handles orphaned rows without racing that write.
				remove = aged
			default:
				cleanupErrors = append(cleanupErrors, errors.New("query Jimeng recovery cleanup state"))
				continue
			}
		}
		if remove {
			if removeErr := removeJimengRecovery(taskID); removeErr != nil {
				cleanupErrors = append(cleanupErrors, removeErr)
			}
		}
	}
	if removedTemporary {
		if syncErr := syncJimengRecoveryDirectory(directory); syncErr != nil {
			cleanupErrors = append(cleanupErrors, syncErr)
		}
	}
	return errors.Join(cleanupErrors...)
}

func syncJimengRecoveryDirectory(directory string) (returnErr error) {
	dir, err := os.Open(directory)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	defer func() {
		if closeErr := dir.Close(); closeErr != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("close Jimeng recovery directory: %w", closeErr))
		}
	}()
	return dir.Sync()
}
