package relay

import (
	"bufio"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
	geminiVeo "github.com/tokenrouter/tokenrouter/relay/channel/task/gemini"
)

const (
	geminiVeoInlineArtifactLegacyVersion  = 1
	geminiVeoInlineArtifactVersion        = 2
	geminiVeoInlineArtifactHeaderMaxBytes = 4 << 10
	geminiVeoInlineArtifactChunkBytes     = 1 << 20
)

var geminiVeoInlineArtifactMagic = [8]byte{'T', 'R', 'V', 'E', 'O', '0', '1', '\n'}

type geminiVeoInlineArtifactHeader struct {
	Version      int    `json:"version"`
	KeyID        string `json:"key_id"`
	ArtifactID   string `json:"artifact_id,omitempty"`
	TaskID       string `json:"task_id"`
	Platform     string `json:"platform"`
	UserID       int    `json:"user_id"`
	ChannelID    int    `json:"channel_id"`
	MIMEType     string `json:"mime_type"`
	DecodedBytes int64  `json:"decoded_bytes"`
}

func durableGeminiVeoTaskData(task *model.Task, provider *VeoProviderTask) (geminiVeoStoredTaskData, error) {
	if task == nil || provider == nil || !isGeminiVeoTask(task) {
		return geminiVeoStoredTaskData{}, errors.New("invalid Veo provider task")
	}
	hasInline := provider.InlineBase64 != ""
	providerArtifactID := ""
	if hasInline {
		if provider.Status != geminiVeo.StatusSucceeded || provider.ResultURL != "" ||
			provider.InlineMIMEType != "video/mp4" || provider.InlineBytes <= 0 ||
			provider.InlineBytes > geminiVeoTaskMaxInlineVideoBytes {
			return geminiVeoStoredTaskData{}, errors.New("invalid Veo inline result")
		}
		artifactID, err := persistGeminiVeoInlineArtifact(task, provider.InlineMIMEType,
			provider.InlineBase64, provider.InlineBytes)
		if err != nil {
			return geminiVeoStoredTaskData{}, err
		}
		providerArtifactID = artifactID
	} else if provider.InlineMIMEType != "" || provider.InlineBytes != 0 {
		return geminiVeoStoredTaskData{}, errors.New("incomplete Veo inline result")
	}
	data, err := normalizedGeminiVeoTaskData(provider)
	if err != nil {
		return geminiVeoStoredTaskData{}, err
	}
	if hasInline {
		data.InlineArtifactID = providerArtifactID
		if _, err := marshalGeminiVeoStoredTaskData(data); err != nil {
			return geminiVeoStoredTaskData{}, err
		}
	}
	return data, nil
}

func geminiVeoInlineArtifactDirectory() (string, error) {
	root, err := validatedGeminiVeoRecoveryJournalDirectory()
	if err != nil {
		return "", err
	}
	directory := filepath.Join(root, "inline-v1")
	if filepath.Clean(directory) == "." || filepath.Clean(directory) == string(filepath.Separator) {
		return "", errors.New("unsafe Veo inline artifact directory")
	}
	return directory, nil
}

func geminiVeoInlineArtifactPath(taskID string) (string, error) {
	return geminiVeoInlineArtifactPathForID(taskID, "")
}

func geminiVeoInlineArtifactPathForID(taskID, artifactID string) (string, error) {
	if validateGeminiVeoTaskPublicID(taskID) != nil || filepath.Base(taskID) != taskID {
		return "", errors.New("invalid Veo inline artifact task id")
	}
	if artifactID != "" && !validGeminiVeoInlineArtifactID(artifactID) {
		return "", errors.New("invalid Veo inline artifact id")
	}
	directory, err := geminiVeoInlineArtifactDirectory()
	if err != nil {
		return "", err
	}
	name := taskID
	if artifactID != "" {
		name += "." + artifactID
	}
	return filepath.Join(directory, name+".bin"), nil
}

func geminiVeoInlineArtifactID(keyID, encoded string) string {
	hash := sha256.New()
	_, _ = io.WriteString(hash, keyID)
	_, _ = hash.Write([]byte{0})
	_, _ = io.WriteString(hash, encoded)
	return hex.EncodeToString(hash.Sum(nil))
}

func ensureGeminiVeoInlineArtifactDirectory() (string, error) {
	directory, err := geminiVeoInlineArtifactDirectory()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", errors.New("create Veo inline artifact directory")
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("unsafe Veo inline artifact directory")
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return "", errors.New("protect Veo inline artifact directory")
	}
	return directory, nil
}

func persistGeminiVeoInlineArtifact(task *model.Task, mimeType, encoded string, decodedBytes int64) (
	artifactID string,
	returnErr error,
) {
	if task == nil || !isGeminiVeoTask(task) || mimeType != "video/mp4" || decodedBytes <= 0 ||
		decodedBytes > geminiVeoTaskMaxInlineVideoBytes || encoded == "" || strings.TrimSpace(encoded) != encoded ||
		strings.ContainsAny(encoded, " \t\r\n") ||
		len(encoded) > base64.StdEncoding.EncodedLen(geminiVeoTaskMaxInlineVideoBytes) {
		return "", errors.New("invalid Veo inline artifact")
	}
	keys, err := asyncTaskEncryptionKeyring()
	if err != nil || len(keys) == 0 {
		return "", errors.New("Veo inline artifact encryption key is unavailable")
	}
	gcm, err := asyncTaskGCM(keys[0].secret)
	if err != nil {
		return "", err
	}
	directory, err := ensureGeminiVeoInlineArtifactDirectory()
	if err != nil {
		return "", err
	}
	artifactID = geminiVeoInlineArtifactID(keys[0].id, encoded)
	path, err := geminiVeoInlineArtifactPathForID(task.TaskID, artifactID)
	if err != nil {
		return "", err
	}
	nonceID, err := common.SecureRandomUUID()
	if err != nil {
		return "", err
	}
	temporaryPath := filepath.Join(directory, "."+task.TaskID+"."+nonceID+".tmp")
	file, err := os.OpenFile(temporaryPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", errors.New("create Veo inline artifact")
	}
	closed := false
	defer func() {
		if !closed {
			if closeErr := file.Close(); closeErr != nil && returnErr == nil {
				returnErr = errors.New("close Veo inline artifact")
			}
		}
		if returnErr != nil {
			_ = os.Remove(temporaryPath)
		}
	}()
	header := geminiVeoInlineArtifactHeader{Version: geminiVeoInlineArtifactVersion, KeyID: keys[0].id,
		ArtifactID: artifactID,
		TaskID:     task.TaskID, Platform: task.Platform, UserID: task.UserId, ChannelID: task.ChannelId,
		MIMEType: mimeType, DecodedBytes: decodedBytes}
	headerJSON, err := common.Marshal(header)
	if err != nil || len(headerJSON) == 0 || len(headerJSON) > geminiVeoInlineArtifactHeaderMaxBytes {
		return "", errors.New("encode Veo inline artifact header")
	}
	writer := bufio.NewWriterSize(file, 64<<10)
	if _, err := writer.Write(geminiVeoInlineArtifactMagic[:]); err != nil ||
		binary.Write(writer, binary.BigEndian, uint32(len(headerJSON))) != nil {
		return "", errors.New("write Veo inline artifact header")
	}
	if _, err := writer.Write(headerJSON); err != nil {
		return "", errors.New("write Veo inline artifact header")
	}
	headerDigest := sha256.Sum256(headerJSON)
	binding := geminiVeoInlineArtifactBinding(task)
	decoder := base64.NewDecoder(base64.StdEncoding.Strict(), strings.NewReader(encoded))
	buffer := make([]byte, geminiVeoInlineArtifactChunkBytes)
	var total int64
	var chunk uint64
	var detected []byte
	for {
		read, readErr := io.ReadFull(decoder, buffer)
		if readErr != nil && !errors.Is(readErr, io.EOF) && !errors.Is(readErr, io.ErrUnexpectedEOF) {
			return "", errors.New("decode Veo inline artifact")
		}
		if read > 0 {
			total += int64(read)
			if total > decodedBytes || total > geminiVeoTaskMaxInlineVideoBytes {
				return "", errors.New("Veo inline artifact exceeds its declared size")
			}
			if len(detected) < 512 {
				need := min(512-len(detected), read)
				detected = append(detected, buffer[:need]...)
			}
			nonce, randomErr := common.SecureRandomBytes(gcm.NonceSize())
			if randomErr != nil {
				return "", randomErr
			}
			aad := geminiVeoInlineArtifactChunkAAD(binding, headerDigest, chunk)
			sealed := gcm.Seal(nonce, nonce, buffer[:read], aad)
			if len(sealed) > geminiVeoInlineArtifactChunkBytes+gcm.NonceSize()+gcm.Overhead() ||
				binary.Write(writer, binary.BigEndian, uint32(len(sealed))) != nil {
				return "", errors.New("write Veo inline artifact chunk")
			}
			if _, err := writer.Write(sealed); err != nil {
				return "", errors.New("write Veo inline artifact chunk")
			}
			chunk++
		}
		if errors.Is(readErr, io.EOF) || errors.Is(readErr, io.ErrUnexpectedEOF) {
			break
		}
	}
	if total != decodedBytes || len(detected) == 0 || http.DetectContentType(detected) != mimeType {
		return "", errors.New("Veo inline artifact content does not match its metadata")
	}
	if binary.Write(writer, binary.BigEndian, uint32(0)) != nil || writer.Flush() != nil || file.Sync() != nil {
		return "", errors.New("sync Veo inline artifact")
	}
	if err := file.Close(); err != nil {
		return "", errors.New("close Veo inline artifact")
	}
	closed = true
	if err := os.Rename(temporaryPath, path); err != nil {
		return "", errors.New("publish Veo inline artifact")
	}
	if err := syncGeminiVeoArtifactDirectory(directory); err != nil {
		return "", err
	}
	return artifactID, nil
}

func openGeminiVeoInlineArtifact(task *model.Task, data geminiVeoStoredTaskData) (*http.Response, error) {
	if task == nil || !isGeminiVeoTask(task) || !data.HasInlineVideo || data.InlineMIMEType != "video/mp4" ||
		data.InlineBytes <= 0 || data.InlineBytes > geminiVeoTaskMaxInlineVideoBytes {
		return nil, errors.New("invalid Veo inline artifact reference")
	}
	path, err := geminiVeoInlineArtifactPathForID(task.TaskID, data.InlineArtifactID)
	if err != nil {
		return nil, err
	}
	file, err := openRecoveryJournalFile(path)
	if err != nil {
		return nil, errors.New("open Veo inline artifact")
	}
	body, err := newGeminiVeoArtifactReader(file, task, data)
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	return &http.Response{StatusCode: http.StatusOK, ContentLength: data.InlineBytes,
		Header: http.Header{"Content-Type": []string{data.InlineMIMEType}}, Body: body}, nil
}

type geminiVeoArtifactReader struct {
	file         *os.File
	gcm          cipher.AEAD
	binding      string
	headerDigest [32]byte
	chunk        uint64
	remaining    int64
	pending      []byte
	verifiedEnd  bool
}

func newGeminiVeoArtifactReader(file *os.File, task *model.Task,
	data geminiVeoStoredTaskData) (*geminiVeoArtifactReader, error) {
	var magic [8]byte
	if _, err := io.ReadFull(file, magic[:]); err != nil || magic != geminiVeoInlineArtifactMagic {
		return nil, errors.New("invalid Veo inline artifact header")
	}
	var headerSize uint32
	if binary.Read(file, binary.BigEndian, &headerSize) != nil || headerSize == 0 ||
		headerSize > geminiVeoInlineArtifactHeaderMaxBytes {
		return nil, errors.New("invalid Veo inline artifact header")
	}
	headerJSON := make([]byte, headerSize)
	if _, err := io.ReadFull(file, headerJSON); err != nil {
		return nil, errors.New("invalid Veo inline artifact header")
	}
	var header geminiVeoInlineArtifactHeader
	if strictGeminiVeoJSON(headerJSON, &header) != nil {
		return nil, errors.New("Veo inline artifact identity mismatch")
	}
	legacyIdentity := header.Version == geminiVeoInlineArtifactLegacyVersion &&
		header.ArtifactID == "" && data.InlineArtifactID == ""
	versionedIdentity := header.Version == geminiVeoInlineArtifactVersion &&
		header.ArtifactID == data.InlineArtifactID && validGeminiVeoInlineArtifactID(header.ArtifactID)
	if (!legacyIdentity && !versionedIdentity) ||
		header.TaskID != task.TaskID || header.Platform != task.Platform || header.UserID != task.UserId ||
		header.ChannelID != task.ChannelId || header.MIMEType != data.InlineMIMEType ||
		header.DecodedBytes != data.InlineBytes || !validAsyncTaskKeyID(header.KeyID) {
		return nil, errors.New("Veo inline artifact identity mismatch")
	}
	keys, err := asyncTaskEncryptionKeyring()
	if err != nil {
		return nil, errors.New("Veo inline artifact encryption key is unavailable")
	}
	var gcm cipher.AEAD
	for _, key := range keys {
		if key.id == header.KeyID {
			gcm, err = asyncTaskGCM(key.secret)
			break
		}
	}
	if err != nil || gcm == nil {
		return nil, errors.New("Veo inline artifact encryption key is unavailable")
	}
	return &geminiVeoArtifactReader{file: file, gcm: gcm, binding: geminiVeoInlineArtifactBinding(task),
		headerDigest: sha256.Sum256(headerJSON), remaining: data.InlineBytes}, nil
}

func (reader *geminiVeoArtifactReader) Read(destination []byte) (int, error) {
	if len(destination) == 0 {
		return 0, nil
	}
	if len(reader.pending) == 0 {
		if reader.verifiedEnd {
			return 0, io.EOF
		}
		var sealedSize uint32
		if binary.Read(reader.file, binary.BigEndian, &sealedSize) != nil || sealedSize == 0 ||
			int(sealedSize) > geminiVeoInlineArtifactChunkBytes+reader.gcm.NonceSize()+reader.gcm.Overhead() {
			return 0, errors.New("invalid Veo inline artifact chunk")
		}
		sealed := make([]byte, sealedSize)
		if _, err := io.ReadFull(reader.file, sealed); err != nil || len(sealed) < reader.gcm.NonceSize()+reader.gcm.Overhead() {
			return 0, errors.New("invalid Veo inline artifact chunk")
		}
		aad := geminiVeoInlineArtifactChunkAAD(reader.binding, reader.headerDigest, reader.chunk)
		plain, err := reader.gcm.Open(nil, sealed[:reader.gcm.NonceSize()], sealed[reader.gcm.NonceSize():], aad)
		if err != nil || len(plain) == 0 || int64(len(plain)) > reader.remaining {
			return 0, errors.New("decrypt Veo inline artifact chunk")
		}
		reader.pending = plain
		reader.remaining -= int64(len(plain))
		reader.chunk++
		if reader.remaining == 0 {
			var terminator uint32
			if binary.Read(reader.file, binary.BigEndian, &terminator) != nil || terminator != 0 {
				return 0, errors.New("invalid Veo inline artifact terminator")
			}
			var probe [1]byte
			if read, err := reader.file.Read(probe[:]); read != 0 || !errors.Is(err, io.EOF) {
				return 0, errors.New("Veo inline artifact has trailing data")
			}
			reader.verifiedEnd = true
		}
	}
	read := copy(destination, reader.pending)
	reader.pending = reader.pending[read:]
	return read, nil
}

func (reader *geminiVeoArtifactReader) Close() error { return reader.file.Close() }

func geminiVeoInlineArtifactBinding(task *model.Task) string {
	return fmt.Sprintf("veo:inline-artifact:v1:%s:%s:%d:%d", task.TaskID, task.Platform, task.UserId, task.ChannelId)
}

func geminiVeoInlineArtifactChunkAAD(binding string, headerDigest [32]byte, chunk uint64) []byte {
	return []byte(fmt.Sprintf("%s:%x:%d", binding, headerDigest, chunk))
}

func syncGeminiVeoArtifactDirectory(directory string) (returnErr error) {
	handle, err := os.Open(directory)
	if err != nil {
		return errors.New("open Veo inline artifact directory")
	}
	defer func() {
		if closeErr := handle.Close(); closeErr != nil && returnErr == nil {
			returnErr = errors.New("close Veo inline artifact directory")
		}
	}()
	if err := handle.Sync(); err != nil {
		return errors.New("sync Veo inline artifact directory")
	}
	return nil
}

func removeGeminiVeoInlineArtifact(taskID string) error {
	path, err := geminiVeoInlineArtifactPath(taskID)
	if err != nil {
		return err
	}
	err = os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
