package catalog

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	wallclock "github.com/tokenrouter/tokenrouter/internal/platform/clock"
	"github.com/tokenrouter/tokenrouter/internal/platform/env"
	"github.com/tokenrouter/tokenrouter/internal/platform/httpx"
	"github.com/tokenrouter/tokenrouter/internal/platform/jsonutil"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	defaultModelSyncBase = "https://basellm.github.io/llm-metadata"

	defaultModelSyncRetries              = 3
	maxModelSyncRetries                  = 8
	defaultModelSyncTimeoutSeconds       = 10
	maxModelSyncTimeoutSeconds           = 60
	defaultModelSyncMaxBytes       int64 = 10 << 20
	maxModelSyncMaxBytes           int64 = 16 << 20
	modelSyncHeaderLimit                 = 64 << 10
)

type ModelSyncOverwrite struct {
	ModelName string   `json:"model_name"`
	Fields    []string `json:"fields"`
}

type ModelSyncRequest struct {
	Overwrite []ModelSyncOverwrite `json:"overwrite"`
	Locale    string               `json:"locale"`
}

type ModelSyncSource struct {
	Locale     string `json:"locale"`
	ModelsURL  string `json:"models_url"`
	VendorsURL string `json:"vendors_url"`
}

type ModelSyncConflictField struct {
	Field    string `json:"field"`
	Local    any    `json:"local"`
	Upstream any    `json:"upstream"`
}

type ModelSyncConflict struct {
	ModelName string                   `json:"model_name"`
	Fields    []ModelSyncConflictField `json:"fields"`
}

type ModelSyncPreview struct {
	Missing   []string            `json:"missing"`
	Conflicts []ModelSyncConflict `json:"conflicts"`
	Source    ModelSyncSource     `json:"source"`
}

type ModelSyncResult struct {
	CreatedModels  int             `json:"created_models"`
	CreatedVendors int             `json:"created_vendors"`
	UpdatedModels  int             `json:"updated_models"`
	SkippedModels  []string        `json:"skipped_models"`
	CreatedList    []string        `json:"created_list"`
	UpdatedList    []string        `json:"updated_list"`
	Source         ModelSyncSource `json:"source"`
}

type ModelSyncUpstreamError struct {
	Source ModelSyncSource
	Err    error
}

func (err *ModelSyncUpstreamError) Error() string { return err.Err.Error() }
func (err *ModelSyncUpstreamError) Unwrap() error { return err.Err }

type upstreamModelMetadata struct {
	Description string          `json:"description"`
	Endpoints   json.RawMessage `json:"endpoints"`
	Icon        string          `json:"icon"`
	ModelName   string          `json:"model_name"`
	NameRule    int             `json:"name_rule"`
	Status      *int            `json:"status"`
	Tags        string          `json:"tags"`
	VendorName  string          `json:"vendor_name"`
}

type upstreamVendorMetadata struct {
	Description string `json:"description"`
	Icon        string `json:"icon"`
	Name        string `json:"name"`
	Status      *int   `json:"status"`
}

type modelSyncCacheEntry struct {
	ETag string
	Body []byte
}

var modelSyncHTTPCache = struct {
	sync.RWMutex
	entries map[string]modelSyncCacheEntry
}{entries: make(map[string]modelSyncCacheEntry)}

func PreviewModelMetadataSync(ctx context.Context, locale string) (*ModelSyncPreview, error) {
	models, _, source, err := fetchModelSyncCatalog(ctx, locale)
	if err != nil {
		return nil, &ModelSyncUpstreamError{Source: source, Err: err}
	}
	missing, err := model.GetMissingModelNamesContext(ctx)
	if err != nil {
		return nil, err
	}
	upstreamModels := indexUpstreamModels(models)
	var previewMissing []string
	for _, name := range missing {
		if _, ok := upstreamModels[name]; ok {
			previewMissing = append(previewMissing, name)
		}
	}
	sort.Strings(previewMissing)

	var localModels []model.Model
	if err := model.DB.WithContext(ctx).Find(&localModels).Error; err != nil {
		return nil, err
	}
	vendorNames, err := localVendorNames(ctx)
	if err != nil {
		return nil, err
	}

	var conflicts []ModelSyncConflict
	for i := range localModels {
		local := &localModels[i]
		upstream, ok := upstreamModels[local.ModelName]
		if !ok || local.SyncOfficial == 0 {
			continue
		}
		fields := compareModelMetadata(local, upstream, vendorNames[local.VendorID])
		if len(fields) > 0 {
			conflicts = append(conflicts, ModelSyncConflict{ModelName: local.ModelName, Fields: fields})
		}
	}
	sort.Slice(conflicts, func(i, j int) bool { return conflicts[i].ModelName < conflicts[j].ModelName })
	return &ModelSyncPreview{Missing: previewMissing, Conflicts: conflicts, Source: source}, nil
}

func ApplyModelMetadataSync(ctx context.Context, request ModelSyncRequest) (*ModelSyncResult, error) {
	source := modelSyncSource(request.Locale)
	missing, err := model.GetMissingModelNamesContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("获取模型列表失败，请稍后重试: %w", err)
	}
	result := &ModelSyncResult{Source: source, SkippedModels: []string{}, CreatedList: []string{}, UpdatedList: []string{}}
	if len(missing) == 0 && len(request.Overwrite) == 0 {
		return result, nil
	}

	models, vendors, source, err := fetchModelSyncCatalog(ctx, request.Locale)
	result.Source = source
	if err != nil {
		return nil, &ModelSyncUpstreamError{Source: source, Err: err}
	}
	upstreamModels := indexUpstreamModels(models)
	upstreamVendors := indexUpstreamVendors(vendors)
	skipped := make(map[string]struct{})

	sort.Strings(missing)
	for _, name := range missing {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		upstream, ok := upstreamModels[name]
		if !ok {
			skipped[name] = struct{}{}
			continue
		}
		createdVendor, err := createMissingModelFromUpstream(ctx, upstream, upstreamVendors)
		if err != nil {
			if errors.Is(err, model.ErrModelNameExists) {
				skipped[name] = struct{}{}
				continue
			}
			return nil, err
		}
		result.CreatedModels++
		result.CreatedList = append(result.CreatedList, name)
		if createdVendor {
			result.CreatedVendors++
		}
	}

	for _, overwrite := range request.Overwrite {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		updated, createdVendor, err := overwriteModelFromUpstream(ctx, overwrite, upstreamModels, upstreamVendors)
		if err != nil {
			return nil, err
		}
		if !updated {
			continue
		}
		result.UpdatedModels++
		result.UpdatedList = append(result.UpdatedList, overwrite.ModelName)
		if createdVendor {
			result.CreatedVendors++
		}
	}

	for name := range skipped {
		result.SkippedModels = append(result.SkippedModels, name)
	}
	sort.Strings(result.SkippedModels)
	return result, nil
}

func createMissingModelFromUpstream(ctx context.Context, upstream upstreamModelMetadata, vendors map[string]upstreamVendorMetadata) (bool, error) {
	var createdVendor bool
	err := model.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing model.Model
		err := tx.Where("model_name = ?", upstream.ModelName).First(&existing).Error
		if err == nil {
			return model.ErrModelNameExists
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		vendorID, created, err := ensureSyncVendor(tx, upstream.VendorName, vendors[upstream.VendorName])
		if err != nil {
			return err
		}
		createdVendor = created
		metadata := model.Model{
			ModelName:    strings.TrimSpace(upstream.ModelName),
			Description:  strings.TrimSpace(upstream.Description),
			Icon:         strings.TrimSpace(upstream.Icon),
			Tags:         strings.TrimSpace(upstream.Tags),
			VendorID:     vendorID,
			Status:       modelSyncStatus(upstream.Status, 0, 1),
			SyncOfficial: 0,
			NameRule:     upstream.NameRule,
			CreatedTime:  wallclock.NowTimestamp(),
			UpdatedTime:  wallclock.NowTimestamp(),
		}
		if err := ValidateModelMetadata(&metadata, false); err != nil {
			return err
		}
		status, syncOfficial := metadata.Status, metadata.SyncOfficial
		result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&metadata)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return model.ErrModelNameExists
		}
		// Reference-compatible schema defaults make raw inserts enabled and
		// official. An upstream sync can explicitly supply zero for both fields,
		// so restore those values before this transaction becomes visible.
		if err := tx.Model(&model.Model{}).Where("id = ?", metadata.Id).Updates(map[string]any{
			"status": status, "sync_official": syncOfficial,
		}).Error; err != nil {
			return err
		}
		return nil
	})
	return createdVendor, err
}

func overwriteModelFromUpstream(ctx context.Context, overwrite ModelSyncOverwrite, upstreamModels map[string]upstreamModelMetadata, vendors map[string]upstreamVendorMetadata) (bool, bool, error) {
	upstream, ok := upstreamModels[overwrite.ModelName]
	if !ok {
		return false, false, nil
	}
	fields := normalizedOverwriteFields(overwrite.Fields)
	if len(fields) == 0 {
		return false, false, nil
	}
	var createdVendor bool
	var didUpdate bool
	err := model.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var local model.Model
		if err := tx.Where("model_name = ?", overwrite.ModelName).First(&local).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		if local.SyncOfficial == 0 {
			return nil
		}
		updates := make(map[string]any)
		if _, ok := fields["description"]; ok {
			updates["description"] = upstream.Description
		}
		if _, ok := fields["icon"]; ok {
			updates["icon"] = upstream.Icon
		}
		if _, ok := fields["tags"]; ok {
			updates["tags"] = upstream.Tags
		}
		if _, ok := fields["name_rule"]; ok {
			updates["name_rule"] = upstream.NameRule
		}
		if _, ok := fields["status"]; ok {
			updates["status"] = modelSyncStatus(upstream.Status, local.Status, local.Status)
		}
		if _, ok := fields["vendor"]; ok {
			vendorID, created, err := ensureSyncVendor(tx, upstream.VendorName, vendors[upstream.VendorName])
			if err != nil {
				return err
			}
			createdVendor = created
			updates["vendor_id"] = vendorID
		}
		if len(updates) == 0 {
			return nil
		}
		updates["updated_time"] = wallclock.NowTimestamp()
		result := tx.Model(&model.Model{}).Where("id = ?", local.Id).Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		didUpdate = true
		return nil
	})
	if err != nil {
		return false, false, err
	}
	return didUpdate, createdVendor, nil
}

func ensureSyncVendor(tx *gorm.DB, name string, upstream upstreamVendorMetadata) (int, bool, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return 0, false, nil
	}
	if utf8.RuneCountInString(name) > 128 {
		return 0, false, errors.New("供应商名称不能超过 128 个字符")
	}
	var vendor model.Vendor
	if err := tx.Where("name = ?", name).First(&vendor).Error; err == nil {
		return vendor.Id, false, nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, false, err
	}
	status := modelSyncStatus(upstream.Status, 0, 1)
	vendor = model.Vendor{
		Name:        name,
		Description: strings.TrimSpace(upstream.Description),
		Icon:        strings.TrimSpace(upstream.Icon),
		Status:      status,
		CreatedTime: wallclock.NowTimestamp(),
		UpdatedTime: wallclock.NowTimestamp(),
	}
	if len(vendor.Description) > 1<<20 || utf8.RuneCountInString(vendor.Icon) > 128 {
		return 0, false, errors.New("供应商元数据无效")
	}
	intendedStatus := vendor.Status
	result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&vendor)
	if result.Error != nil {
		return 0, false, result.Error
	}
	if result.RowsAffected == 0 {
		if err := tx.Where("name = ?", name).First(&vendor).Error; err != nil {
			return 0, false, err
		}
		return vendor.Id, false, nil
	}
	if err := tx.Model(&model.Vendor{}).Where("id = ?", vendor.Id).
		Update("status", intendedStatus).Error; err != nil {
		return 0, false, err
	}
	return vendor.Id, true, nil
}

func compareModelMetadata(local *model.Model, upstream upstreamModelMetadata, localVendor string) []ModelSyncConflictField {
	fields := make([]ModelSyncConflictField, 0, 6)
	addStringConflict := func(field, localValue, upstreamValue string) {
		if strings.TrimSpace(localValue) != strings.TrimSpace(upstreamValue) {
			fields = append(fields, ModelSyncConflictField{Field: field, Local: localValue, Upstream: upstreamValue})
		}
	}
	addStringConflict("description", local.Description, upstream.Description)
	addStringConflict("icon", local.Icon, upstream.Icon)
	addStringConflict("tags", local.Tags, upstream.Tags)
	addStringConflict("vendor", localVendor, upstream.VendorName)
	if local.NameRule != upstream.NameRule {
		fields = append(fields, ModelSyncConflictField{Field: "name_rule", Local: local.NameRule, Upstream: upstream.NameRule})
	}
	if upstream.Status != nil && local.Status != *upstream.Status {
		fields = append(fields, ModelSyncConflictField{Field: "status", Local: local.Status, Upstream: *upstream.Status})
	}
	return fields
}

func modelSyncStatus(upstream *int, local, fallback int) int {
	if upstream != nil {
		return *upstream
	}
	if local != 0 {
		return local
	}
	return fallback
}

func normalizedOverwriteFields(fields []string) map[string]struct{} {
	allowed := map[string]struct{}{
		"description": {}, "icon": {}, "tags": {}, "vendor": {}, "name_rule": {}, "status": {},
	}
	result := make(map[string]struct{})
	for _, field := range fields {
		field = strings.ToLower(strings.TrimSpace(field))
		if _, ok := allowed[field]; ok {
			result[field] = struct{}{}
		}
	}
	return result
}

func localVendorNames(ctx context.Context) (map[int]string, error) {
	var vendors []model.Vendor
	if err := model.DB.WithContext(ctx).Find(&vendors).Error; err != nil {
		return nil, err
	}
	result := make(map[int]string, len(vendors))
	for _, vendor := range vendors {
		result[vendor.Id] = vendor.Name
	}
	return result, nil
}

func indexUpstreamModels(items []upstreamModelMetadata) map[string]upstreamModelMetadata {
	result := make(map[string]upstreamModelMetadata, len(items))
	for _, item := range items {
		if item.ModelName != "" {
			result[item.ModelName] = item
		}
	}
	return result
}

func indexUpstreamVendors(items []upstreamVendorMetadata) map[string]upstreamVendorMetadata {
	result := make(map[string]upstreamVendorMetadata, len(items))
	for _, item := range items {
		if item.Name != "" {
			result[item.Name] = item
		}
	}
	return result
}

func modelSyncSource(locale string) ModelSyncSource {
	base := strings.TrimRight(env.GetEnv("SYNC_UPSTREAM_BASE", defaultModelSyncBase), "/")
	normalized := strings.ToLower(strings.TrimSpace(locale))
	path := "/api/newapi/"
	switch normalized {
	case "en", "ja", "zh-cn", "zh-tw":
		path = "/api/i18n/" + normalized + "/newapi/"
	}
	return ModelSyncSource{
		Locale:     locale,
		ModelsURL:  base + path + "models.json",
		VendorsURL: base + path + "vendors.json",
	}
}

func fetchModelSyncCatalog(ctx context.Context, locale string) ([]upstreamModelMetadata, []upstreamVendorMetadata, ModelSyncSource, error) {
	source := modelSyncSource(locale)
	var models []upstreamModelMetadata
	var vendors []upstreamVendorMetadata
	var modelErr error
	var vendorErr error
	var wait sync.WaitGroup
	wait.Add(2)
	go func() {
		defer wait.Done()
		models, modelErr = fetchModelSyncJSON[upstreamModelMetadata](ctx, source.ModelsURL)
	}()
	go func() {
		defer wait.Done()
		vendors, vendorErr = fetchModelSyncJSON[upstreamVendorMetadata](ctx, source.VendorsURL)
	}()
	wait.Wait()
	if modelErr != nil {
		return nil, nil, source, fmt.Errorf("models catalog: %w", modelErr)
	}
	if vendorErr != nil {
		return nil, nil, source, fmt.Errorf("vendors catalog: %w", vendorErr)
	}
	return models, vendors, source, nil
}

func fetchModelSyncJSON[T any](ctx context.Context, url string) ([]T, error) {
	body, err := fetchModelSyncBody(ctx, url)
	if err != nil {
		return nil, err
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return nil, errors.New("empty upstream response")
	}
	if trimmed[0] == '[' {
		var items []T
		if err := jsonutil.Unmarshal(trimmed, &items); err != nil {
			return nil, err
		}
		return items, nil
	}
	var envelope struct {
		Success *bool  `json:"success"`
		Message string `json:"message"`
		Data    []T    `json:"data"`
	}
	if err := jsonutil.Unmarshal(trimmed, &envelope); err != nil {
		return nil, err
	}
	if envelope.Success != nil && !*envelope.Success {
		message := strings.TrimSpace(envelope.Message)
		if message == "" {
			message = "upstream catalog reported failure"
		}
		return nil, errors.New(message)
	}
	return envelope.Data, nil
}

func fetchModelSyncBody(ctx context.Context, url string) ([]byte, error) {
	if err := validateModelSyncURL(url); err != nil {
		return nil, err
	}
	retries, timeout, maxBytes := modelSyncHTTPPolicy()
	client := newModelSyncHTTPClient(timeout)

	var lastErr error
	for attempt := 0; attempt < retries; attempt++ {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		modelSyncHTTPCache.RLock()
		cached, hasCache := modelSyncHTTPCache.entries[url]
		modelSyncHTTPCache.RUnlock()
		if hasCache && cached.ETag != "" {
			request.Header.Set("If-None-Match", cached.ETag)
		}
		response, err := client.Do(request)
		if err != nil {
			lastErr = err
		} else {
			body, readErr := readBoundedModelSyncBody(response.Body, maxBytes)
			_ = response.Body.Close()
			if readErr != nil {
				lastErr = readErr
			} else if response.StatusCode == http.StatusNotModified {
				if !hasCache {
					lastErr = errors.New("cache miss for 304 response")
				} else {
					return append([]byte(nil), cached.Body...), nil
				}
			} else if response.StatusCode != http.StatusOK {
				lastErr = fmt.Errorf("upstream returned status %s", response.Status)
			} else {
				entry := modelSyncCacheEntry{ETag: response.Header.Get("ETag"), Body: append([]byte(nil), body...)}
				modelSyncHTTPCache.Lock()
				modelSyncHTTPCache.entries[url] = entry
				modelSyncHTTPCache.Unlock()
				return body, nil
			}
		}
		if attempt+1 < retries {
			timer := time.NewTimer(time.Duration(50*(1<<attempt)) * time.Millisecond)
			select {
			case <-ctx.Done():
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				return nil, ctx.Err()
			case <-timer.C:
			}
		}
	}
	return nil, lastErr
}

func modelSyncHTTPPolicy() (int, time.Duration, int64) {
	retries := env.GetEnvInt("SYNC_HTTP_RETRY", defaultModelSyncRetries)
	if strings.TrimSpace(os.Getenv("SYNC_HTTP_RETRY")) == "" {
		// Keep the earlier TokenRouter alias working while giving the reference
		// variable deterministic precedence when both are present.
		retries = env.GetEnvInt("SYNC_HTTP_RETRY_COUNT", defaultModelSyncRetries)
	}
	if retries < 1 {
		retries = 1
	} else if retries > maxModelSyncRetries {
		retries = maxModelSyncRetries
	}

	timeoutSeconds := env.GetEnvInt("SYNC_HTTP_TIMEOUT_SECONDS", defaultModelSyncTimeoutSeconds)
	if timeoutSeconds < 1 {
		timeoutSeconds = 1
	} else if timeoutSeconds > maxModelSyncTimeoutSeconds {
		timeoutSeconds = maxModelSyncTimeoutSeconds
	}

	maxBytes := defaultModelSyncMaxBytes
	if strings.TrimSpace(os.Getenv("SYNC_HTTP_MAX_MB")) != "" {
		maxMB := env.GetEnvInt("SYNC_HTTP_MAX_MB", int(defaultModelSyncMaxBytes>>20))
		if maxMB < 1 {
			maxMB = 1
		} else if int64(maxMB) > maxModelSyncMaxBytes>>20 {
			maxMB = int(maxModelSyncMaxBytes >> 20)
		}
		maxBytes = int64(maxMB) << 20
	} else {
		// Compatibility with the byte-valued alias used by earlier target builds.
		configured := int64(env.GetEnvInt("SYNC_HTTP_MAX_BYTES", int(defaultModelSyncMaxBytes)))
		if configured < 1024 {
			configured = 1024
		} else if configured > maxModelSyncMaxBytes {
			configured = maxModelSyncMaxBytes
		}
		maxBytes = configured
	}
	return retries, time.Duration(timeoutSeconds) * time.Second, maxBytes
}

func newModelSyncHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy:                  nil,
			DialContext:            httpx.SafeDialContext,
			ForceAttemptHTTP2:      true,
			MaxIdleConns:           20,
			MaxIdleConnsPerHost:    4,
			ResponseHeaderTimeout:  timeout,
			TLSHandshakeTimeout:    timeout,
			IdleConnTimeout:        90 * time.Second,
			MaxResponseHeaderBytes: modelSyncHeaderLimit,
			ExpectContinueTimeout:  time.Second,
			TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12},
		},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func validateModelSyncURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid model-sync URL: %w", err)
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" ||
		parsed.User != nil || parsed.Opaque != "" || parsed.Fragment != "" {
		return errors.New("invalid model-sync URL")
	}
	return nil
}

func readBoundedModelSyncBody(reader io.Reader, maxBytes int64) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(reader, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > maxBytes {
		return nil, fmt.Errorf("upstream response exceeds %d bytes", maxBytes)
	}
	return body, nil
}
