package operations

import (
	"errors"
	"fmt"
	"github.com/tokenrouter/tokenrouter/internal/platform/httpx"
	"github.com/tokenrouter/tokenrouter/internal/platform/jsonutil"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	performanceSettingOption       = "performance_setting"
	performanceCacheDirectoryName  = "tokenrouter-body-cache"
	defaultPerformanceScanLimit    = 4096
	maximumPerformanceScanLimit    = 65536
	maximumPerformanceCleanupValue = 36500
	maximumPerformancePathBytes    = 4096
	maximumPerformanceCacheMB      = 1 << 20 // 1 TiB; keeps byte conversion bounded.
)

var (
	// These errors intentionally contain no configured path. Handlers may return
	// their category to a root operator without disclosing local filesystem
	// layout on a failed request.
	ErrPerformanceInvalidConfig    = errors.New("invalid performance configuration")
	ErrPerformanceUnsafePath       = errors.New("unsafe performance directory configuration")
	ErrPerformanceDirectoryLimit   = errors.New("performance directory entry limit exceeded")
	ErrPerformanceLogNotConfigured = errors.New("log directory not configured")

	performanceMutationMu sync.Mutex
	diskCacheHits         atomic.Int64
	memoryCacheHits       atomic.Int64
)

// PerformanceCacheStats is wire-compatible with the reference cache counter
// object. Active disk usage is derived from the bounded directory snapshot;
// the hit counters are process-local and can be fed by future cache users.
type PerformanceCacheStats struct {
	ActiveDiskFiles         int64 `json:"active_disk_files"`
	CurrentDiskUsageBytes   int64 `json:"current_disk_usage_bytes"`
	ActiveMemoryBuffers     int64 `json:"active_memory_buffers"`
	CurrentMemoryUsageBytes int64 `json:"current_memory_usage_bytes"`
	DiskCacheHits           int64 `json:"disk_cache_hits"`
	MemoryCacheHits         int64 `json:"memory_cache_hits"`
	DiskCacheMaxBytes       int64 `json:"disk_cache_max_bytes"`
	DiskCacheThresholdBytes int64 `json:"disk_cache_threshold_bytes"`
}

type PerformanceMemoryStats struct {
	Alloc        uint64 `json:"alloc"`
	TotalAlloc   uint64 `json:"total_alloc"`
	Sys          uint64 `json:"sys"`
	NumGC        uint32 `json:"num_gc"`
	NumGoroutine int    `json:"num_goroutine"`
}

type PerformanceDiskCacheInfo struct {
	Path      string `json:"path"`
	Exists    bool   `json:"exists"`
	FileCount int    `json:"file_count"`
	TotalSize int64  `json:"total_size"`
}

type PerformanceDiskSpaceInfo struct {
	Total       uint64  `json:"total"`
	Free        uint64  `json:"free"`
	Used        uint64  `json:"used"`
	UsedPercent float64 `json:"used_percent"`
}

type PerformanceConfig struct {
	DiskCacheEnabled       bool   `json:"disk_cache_enabled"`
	DiskCacheThresholdMB   int    `json:"disk_cache_threshold_mb"`
	DiskCacheMaxSizeMB     int    `json:"disk_cache_max_size_mb"`
	DiskCachePath          string `json:"disk_cache_path"`
	IsRunningInContainer   bool   `json:"is_running_in_container"`
	MonitorEnabled         bool   `json:"monitor_enabled"`
	MonitorCPUThreshold    int    `json:"monitor_cpu_threshold"`
	MonitorMemoryThreshold int    `json:"monitor_memory_threshold"`
	MonitorDiskThreshold   int    `json:"monitor_disk_threshold"`
}

type PerformanceStats struct {
	CacheStats    PerformanceCacheStats    `json:"cache_stats"`
	MemoryStats   PerformanceMemoryStats   `json:"memory_stats"`
	DiskCacheInfo PerformanceDiskCacheInfo `json:"disk_cache_info"`
	DiskSpaceInfo PerformanceDiskSpaceInfo `json:"disk_space_info"`
	Config        PerformanceConfig        `json:"config"`
}

type PerformanceLogFileInfo struct {
	Name    string    `json:"name"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mod_time"`
}

type PerformanceLogFilesResponse struct {
	LogDir     string                   `json:"log_dir"`
	Enabled    bool                     `json:"enabled"`
	FileCount  int                      `json:"file_count"`
	TotalSize  int64                    `json:"total_size"`
	OldestTime *time.Time               `json:"oldest_time,omitempty"`
	NewestTime *time.Time               `json:"newest_time,omitempty"`
	Files      []PerformanceLogFileInfo `json:"files"`
}

type PerformanceLogCleanupResult struct {
	DeletedCount int      `json:"deleted_count"`
	FreedBytes   int64    `json:"freed_bytes"`
	FailedFiles  []string `json:"failed_files"`
}

type performanceDirectoryEntry struct {
	name string
	info os.FileInfo
}

// RecordPerformanceDiskCacheHit increments the process-local disk-cache hit
// counter without allowing signed overflow.
func RecordPerformanceDiskCacheHit() {
	incrementPerformanceCounter(&diskCacheHits)
}

// RecordPerformanceMemoryCacheHit increments the process-local memory-cache
// hit counter without allowing signed overflow.
func RecordPerformanceMemoryCacheHit() {
	incrementPerformanceCounter(&memoryCacheHits)
}

func incrementPerformanceCounter(counter *atomic.Int64) {
	for {
		current := counter.Load()
		if current == math.MaxInt64 {
			return
		}
		if counter.CompareAndSwap(current, current+1) {
			return
		}
	}
}

// ResetPerformanceStats resets hit totals only. Active usage remains an
// observed property of the cache directory, matching the reference contract.
func ResetPerformanceStats() {
	diskCacheHits.Store(0)
	memoryCacheHits.Store(0)
}

func performanceConfig() (PerformanceConfig, error) {
	config := PerformanceConfig{
		DiskCacheEnabled:       false,
		DiskCacheThresholdMB:   10,
		DiskCacheMaxSizeMB:     1024,
		MonitorEnabled:         true,
		MonitorCPUThreshold:    90,
		MonitorMemoryThreshold: 90,
		MonitorDiskThreshold:   95,
	}
	if raw := strings.TrimSpace(setting.GetOption(performanceSettingOption)); raw != "" {
		if len(raw) > 64<<10 || jsonutil.UnmarshalJsonStr(raw, &config) != nil {
			return PerformanceConfig{}, ErrPerformanceInvalidConfig
		}
	}
	if path := firstNonEmptyEnv("TOKENROUTER_DISK_CACHE_PATH", "DISK_CACHE_PATH"); path != "" {
		config.DiskCachePath = path
	}
	if config.DiskCacheThresholdMB < 1 || config.DiskCacheThresholdMB > maximumPerformanceCacheMB ||
		config.DiskCacheMaxSizeMB < 1 || config.DiskCacheMaxSizeMB > maximumPerformanceCacheMB ||
		config.DiskCacheThresholdMB > config.DiskCacheMaxSizeMB ||
		!validPercent(config.MonitorCPUThreshold) || !validPercent(config.MonitorMemoryThreshold) ||
		!validPercent(config.MonitorDiskThreshold) {
		return PerformanceConfig{}, ErrPerformanceInvalidConfig
	}
	config.IsRunningInContainer = runningInContainer()
	return config, nil
}

func validPercent(value int) bool {
	return value >= 0 && value <= 100
}

func firstNonEmptyEnv(keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}

func performanceScanLimit() (int, error) {
	raw := strings.TrimSpace(os.Getenv("TOKENROUTER_PERFORMANCE_SCAN_LIMIT"))
	if raw == "" {
		return defaultPerformanceScanLimit, nil
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit < 1 || limit > maximumPerformanceScanLimit {
		return 0, ErrPerformanceInvalidConfig
	}
	return limit, nil
}

func validatedOperatorPath(raw string) (string, error) {
	if raw == "" || len(raw) > maximumPerformancePathBytes || strings.ContainsRune(raw, '\x00') {
		return "", ErrPerformanceUnsafePath
	}
	// Configuration is trusted operator input, but destructive maintenance must
	// still refuse traversal spellings so a typo cannot silently retarget it.
	normalized := strings.ReplaceAll(raw, "\\", "/")
	for _, component := range strings.Split(normalized, "/") {
		if component == ".." {
			return "", ErrPerformanceUnsafePath
		}
	}
	absolute, err := filepath.Abs(raw)
	if err != nil || filepath.Clean(absolute) == string(filepath.Separator) {
		return "", ErrPerformanceUnsafePath
	}
	return filepath.Clean(absolute), nil
}

func diskCacheDirectory(config PerformanceConfig) (directory string, base string, err error) {
	base = strings.TrimSpace(config.DiskCachePath)
	if base == "" {
		base = os.TempDir()
	}
	base, err = validatedOperatorPath(base)
	if err != nil {
		return "", "", err
	}
	if info, statErr := os.Lstat(base); statErr == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", "", ErrPerformanceUnsafePath
		}
	} else if !os.IsNotExist(statErr) {
		return "", "", statErr
	}
	return filepath.Join(base, performanceCacheDirectoryName), base, nil
}

func logDirectory() (string, bool, error) {
	raw := firstNonEmptyEnv("TOKENROUTER_LOG_DIR", "LOG_DIR")
	if raw == "" {
		return "", false, nil
	}
	directory, err := validatedOperatorPath(raw)
	if err != nil {
		return "", true, err
	}
	return directory, true, nil
}

// openAnchoredDirectory opens the directory through its parent os.Root. A
// leaf symlink is rejected, and subsequent operations cannot escape through a
// renamed parent or a malicious child symlink.
func openAnchoredDirectory(directory string, missingOK bool) (*os.Root, bool, error) {
	parent, name := filepath.Dir(directory), filepath.Base(directory)
	parentRoot, err := os.OpenRoot(parent)
	if err != nil {
		if missingOK && os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	defer parentRoot.Close()
	info, err := parentRoot.Lstat(name)
	if err != nil {
		if missingOK && os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, false, ErrPerformanceUnsafePath
	}
	root, err := parentRoot.OpenRoot(name)
	if err != nil {
		return nil, false, err
	}
	return root, true, nil
}

func readBoundedDirectory(root *os.Root, limit int) ([]performanceDirectoryEntry, error) {
	directory, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	entries, readErr := directory.ReadDir(limit + 1)
	closeErr := directory.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if len(entries) > limit {
		return nil, ErrPerformanceDirectoryLimit
	}
	result := make([]performanceDirectoryEntry, 0, len(entries))
	for _, entry := range entries {
		info, err := root.Lstat(entry.Name())
		if err != nil {
			return nil, err
		}
		result = append(result, performanceDirectoryEntry{name: entry.Name(), info: info})
	}
	return result, nil
}

func regularFileTotals(entries []performanceDirectoryEntry) (int, int64, error) {
	count := 0
	var total int64
	for _, entry := range entries {
		if !entry.info.Mode().IsRegular() {
			continue
		}
		size := entry.info.Size()
		if size < 0 || size > math.MaxInt64-total {
			return 0, 0, ErrPerformanceInvalidConfig
		}
		count++
		total += size
	}
	return count, total, nil
}

// GetPerformanceStats returns one bounded, internally consistent snapshot.
func GetPerformanceStats() (PerformanceStats, error) {
	config, err := performanceConfig()
	if err != nil {
		return PerformanceStats{}, err
	}
	limit, err := performanceScanLimit()
	if err != nil {
		return PerformanceStats{}, err
	}
	cacheDirectory, cacheBase, err := diskCacheDirectory(config)
	if err != nil {
		return PerformanceStats{}, err
	}
	cacheInfo := PerformanceDiskCacheInfo{Path: cacheDirectory}
	root, exists, err := openAnchoredDirectory(cacheDirectory, true)
	if err != nil {
		return PerformanceStats{}, err
	}
	if exists {
		defer root.Close()
		entries, err := readBoundedDirectory(root, limit)
		if err != nil {
			return PerformanceStats{}, err
		}
		cacheInfo.Exists = true
		cacheInfo.FileCount, cacheInfo.TotalSize, err = regularFileTotals(entries)
		if err != nil {
			return PerformanceStats{}, err
		}
	}

	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	stats := PerformanceStats{
		CacheStats: PerformanceCacheStats{
			ActiveDiskFiles:         int64(cacheInfo.FileCount),
			CurrentDiskUsageBytes:   cacheInfo.TotalSize,
			DiskCacheHits:           diskCacheHits.Load(),
			MemoryCacheHits:         memoryCacheHits.Load(),
			DiskCacheMaxBytes:       int64(config.DiskCacheMaxSizeMB) << 20,
			DiskCacheThresholdBytes: int64(config.DiskCacheThresholdMB) << 20,
		},
		MemoryStats: PerformanceMemoryStats{
			Alloc:        memory.Alloc,
			TotalAlloc:   memory.TotalAlloc,
			Sys:          memory.Sys,
			NumGC:        memory.NumGC,
			NumGoroutine: runtime.NumGoroutine(),
		},
		DiskCacheInfo: cacheInfo,
		DiskSpaceInfo: performanceDiskSpaceInfo(cacheBase),
		Config:        config,
	}
	return stats, nil
}

func runningInContainer() bool {
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	file, err := os.Open("/proc/1/cgroup")
	if err == nil {
		data, readErr := httpx.ReadAllLimited(file, 64<<10)
		_ = file.Close()
		if readErr == nil {
			content := strings.ToLower(string(data))
			for _, marker := range []string{"docker", "containerd", "kubepods", "/lxc/"} {
				if strings.Contains(content, marker) {
					return true
				}
			}
		}
	}
	for _, key := range []string{"KUBERNETES_SERVICE_HOST", "container", "CONTAINER"} {
		if strings.TrimSpace(os.Getenv(key)) != "" {
			return true
		}
	}
	return false
}

// CleanupInactiveDiskCache deletes only regular files older than maxAge from
// TokenRouter's managed cache subdirectory. It never recurses or follows a
// symlink and performs a complete bounded preflight before the first removal.
func CleanupInactiveDiskCache(maxAge time.Duration) (int, error) {
	if maxAge <= 0 {
		return 0, ErrPerformanceInvalidConfig
	}
	config, err := performanceConfig()
	if err != nil {
		return 0, err
	}
	limit, err := performanceScanLimit()
	if err != nil {
		return 0, err
	}
	directory, _, err := diskCacheDirectory(config)
	if err != nil {
		return 0, err
	}

	performanceMutationMu.Lock()
	defer performanceMutationMu.Unlock()
	root, exists, err := openAnchoredDirectory(directory, true)
	if err != nil || !exists {
		return 0, err
	}
	defer root.Close()
	entries, err := readBoundedDirectory(root, limit)
	if err != nil {
		return 0, err
	}
	cutoff := time.Now().Add(-maxAge)
	candidates := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.info.Mode().IsRegular() && entry.info.ModTime().Before(cutoff) {
			candidates = append(candidates, entry.name)
		}
	}
	deleted := 0
	failed := 0
	for _, name := range candidates {
		latest, statErr := root.Lstat(name)
		if statErr != nil {
			if os.IsNotExist(statErr) {
				continue
			}
			failed++
			continue
		}
		if !latest.Mode().IsRegular() || !latest.ModTime().Before(cutoff) {
			continue
		}
		if err := root.Remove(name); err != nil {
			failed++
			continue
		}
		deleted++
	}
	if failed > 0 {
		return deleted, fmt.Errorf("disk cache cleanup failed for %d file(s)", failed)
	}
	return deleted, nil
}

// GetPerformanceLogFiles returns the reference log-list response for regular
// oneapi-*.log files. Non-regular entries and nested directories are ignored.
func GetPerformanceLogFiles() (PerformanceLogFilesResponse, error) {
	directory, enabled, err := logDirectory()
	if err != nil {
		return PerformanceLogFilesResponse{}, err
	}
	if !enabled {
		return PerformanceLogFilesResponse{Enabled: false}, nil
	}
	limit, err := performanceScanLimit()
	if err != nil {
		return PerformanceLogFilesResponse{}, err
	}
	root, exists, err := openAnchoredDirectory(directory, false)
	if err != nil {
		return PerformanceLogFilesResponse{}, err
	}
	if !exists {
		return PerformanceLogFilesResponse{}, os.ErrNotExist
	}
	defer root.Close()
	entries, err := readBoundedDirectory(root, limit)
	if err != nil {
		return PerformanceLogFilesResponse{}, err
	}
	files := matchingLogFiles(entries)
	var total int64
	var oldest, newest time.Time
	for index, file := range files {
		if file.Size < 0 || file.Size > math.MaxInt64-total {
			return PerformanceLogFilesResponse{}, ErrPerformanceInvalidConfig
		}
		total += file.Size
		if index == 0 || file.ModTime.Before(oldest) {
			oldest = file.ModTime
		}
		if index == 0 || file.ModTime.After(newest) {
			newest = file.ModTime
		}
	}
	response := PerformanceLogFilesResponse{
		LogDir: directory, Enabled: true, FileCount: len(files), TotalSize: total, Files: files,
	}
	if len(files) > 0 {
		response.OldestTime = &oldest
		response.NewestTime = &newest
	}
	return response, nil
}

func matchingLogFiles(entries []performanceDirectoryEntry) []PerformanceLogFileInfo {
	var files []PerformanceLogFileInfo
	for _, entry := range entries {
		if !entry.info.Mode().IsRegular() || !strings.HasPrefix(entry.name, "oneapi-") ||
			!strings.HasSuffix(entry.name, ".log") {
			continue
		}
		files = append(files, PerformanceLogFileInfo{
			Name: entry.name, Size: entry.info.Size(), ModTime: entry.info.ModTime(),
		})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name > files[j].Name })
	return files
}

// CleanupPerformanceLogFiles removes matching regular files according to the
// reference by_count/by_days policies. The list is bounded and fully read
// before mutation; symlinks and directories are never deletion candidates.
func CleanupPerformanceLogFiles(mode string, value int) (PerformanceLogCleanupResult, error) {
	var result PerformanceLogCleanupResult
	if mode != "by_count" && mode != "by_days" {
		return result, ErrPerformanceInvalidConfig
	}
	if value < 1 || value > maximumPerformanceCleanupValue {
		return result, ErrPerformanceInvalidConfig
	}
	directory, enabled, err := logDirectory()
	if err != nil {
		return result, err
	}
	if !enabled {
		return result, ErrPerformanceLogNotConfigured
	}
	limit, err := performanceScanLimit()
	if err != nil {
		return result, err
	}

	performanceMutationMu.Lock()
	defer performanceMutationMu.Unlock()
	root, exists, err := openAnchoredDirectory(directory, false)
	if err != nil {
		return result, err
	}
	if !exists {
		return result, os.ErrNotExist
	}
	defer root.Close()
	entries, err := readBoundedDirectory(root, limit)
	if err != nil {
		return result, err
	}
	files := matchingLogFiles(entries)
	toDelete := make([]PerformanceLogFileInfo, 0, len(files))
	switch mode {
	case "by_count":
		if value < len(files) {
			toDelete = append(toDelete, files[value:]...)
		}
	case "by_days":
		cutoff := time.Now().AddDate(0, 0, -value)
		for _, file := range files {
			if file.ModTime.Before(cutoff) {
				toDelete = append(toDelete, file)
			}
		}
	}
	for _, file := range toDelete {
		latest, statErr := root.Lstat(file.Name)
		if statErr != nil || !latest.Mode().IsRegular() {
			result.FailedFiles = append(result.FailedFiles, file.Name)
			continue
		}
		if err := root.Remove(file.Name); err != nil {
			result.FailedFiles = append(result.FailedFiles, file.Name)
			continue
		}
		result.DeletedCount++
		if latest.Size() >= 0 && latest.Size() <= math.MaxInt64-result.FreedBytes {
			result.FreedBytes += latest.Size()
		}
	}
	return result, nil
}
