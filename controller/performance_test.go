package controller_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/router"
	"github.com/tokenrouter/tokenrouter/service"
)

const managedPerformanceCacheDirectory = "tokenrouter-body-cache"

func writePerformanceFixture(t *testing.T, path, contents string, modTime time.Time) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte(contents), 0o600))
	require.NoError(t, os.Chtimes(path, modTime, modTime))
}

func assertPathExists(t *testing.T, path string) {
	t.Helper()
	_, err := os.Lstat(path)
	require.NoError(t, err, path)
}

func assertPathMissing(t *testing.T, path string) {
	t.Helper()
	_, err := os.Lstat(path)
	require.ErrorIs(t, err, os.ErrNotExist, path)
}

func TestPerformanceManagementRoutesRequireRoot(t *testing.T) {
	routes := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/performance/stats"},
		{http.MethodDelete, "/api/performance/disk_cache"},
		{http.MethodPost, "/api/performance/reset_stats"},
		{http.MethodPost, "/api/performance/gc"},
		{http.MethodGet, "/api/performance/logs"},
		{http.MethodDelete, "/api/performance/logs?mode=by_count&value=1"},
	}

	unauthenticated := router.SetUpRouter()
	for _, route := range routes {
		recorder := httptest.NewRecorder()
		unauthenticated.ServeHTTP(recorder, httptest.NewRequest(route.method, route.path, nil))
		assert.Equal(t, http.StatusUnauthorized, recorder.Code, route.method+" "+route.path)
	}

	_, adminRequest, _ := setupChannelRead(t, constant.RoleAdminUser)
	for _, route := range routes {
		recorder := adminRequest(route.method, route.path, "")
		assert.Equal(t, http.StatusForbidden, recorder.Code, route.method+" "+route.path)
	}
}

func TestPerformanceStatsResetAndGCContracts(t *testing.T) {
	cacheBase := t.TempDir()
	cacheDirectory := filepath.Join(cacheBase, managedPerformanceCacheDirectory)
	require.NoError(t, os.Mkdir(cacheDirectory, 0o700))
	writePerformanceFixture(t, filepath.Join(cacheDirectory, "body-a.tmp"), "abc", time.Now())
	writePerformanceFixture(t, filepath.Join(cacheDirectory, "body-b.tmp"), "12345", time.Now())
	require.NoError(t, os.Mkdir(filepath.Join(cacheDirectory, "nested"), 0o700))

	outside := filepath.Join(t.TempDir(), "outside-cache-value")
	writePerformanceFixture(t, outside, "must-not-be-counted", time.Now())
	if runtime.GOOS != "windows" {
		require.NoError(t, os.Symlink(outside, filepath.Join(cacheDirectory, "body-link.tmp")))
	}

	t.Setenv("TOKENROUTER_DISK_CACHE_PATH", cacheBase)
	t.Setenv("TOKENROUTER_PERFORMANCE_SCAN_LIMIT", "64")
	t.Setenv("TOKENROUTER_LOG_DIR", "")
	t.Setenv("LOG_DIR", "")
	service.ResetPerformanceStats()
	t.Cleanup(service.ResetPerformanceStats)
	service.RecordPerformanceDiskCacheHit()
	service.RecordPerformanceDiskCacheHit()
	service.RecordPerformanceMemoryCacheHit()

	_, do, _ := setupChannelRead(t, constant.RoleRootUser)
	recorder := do(http.MethodGet, "/api/performance/stats", "")
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	assert.Contains(t, recorder.Header().Get("Cache-Control"), "no-store")
	body := decodeBody(t, recorder)
	require.Equal(t, true, body["success"])
	data := body["data"].(map[string]any)
	cacheInfo := data["disk_cache_info"].(map[string]any)
	assert.Equal(t, cacheDirectory, cacheInfo["path"])
	assert.Equal(t, true, cacheInfo["exists"])
	assert.EqualValues(t, 2, cacheInfo["file_count"])
	assert.EqualValues(t, 8, cacheInfo["total_size"])
	cacheStats := data["cache_stats"].(map[string]any)
	assert.EqualValues(t, 2, cacheStats["active_disk_files"])
	assert.EqualValues(t, 8, cacheStats["current_disk_usage_bytes"])
	assert.EqualValues(t, 2, cacheStats["disk_cache_hits"])
	assert.EqualValues(t, 1, cacheStats["memory_cache_hits"])
	assert.EqualValues(t, 1024<<20, cacheStats["disk_cache_max_bytes"])
	assert.EqualValues(t, 10<<20, cacheStats["disk_cache_threshold_bytes"])
	memoryStats := data["memory_stats"].(map[string]any)
	beforeGC := uint32(memoryStats["num_gc"].(float64))
	assert.Greater(t, memoryStats["num_goroutine"].(float64), float64(0))
	diskSpace := data["disk_space_info"].(map[string]any)
	switch runtime.GOOS {
	case "aix", "darwin", "dragonfly", "freebsd", "linux", "netbsd", "openbsd", "solaris":
		assert.Greater(t, diskSpace["total"].(float64), float64(0))
	}
	config := data["config"].(map[string]any)
	assert.Equal(t, cacheBase, config["disk_cache_path"])
	assert.Equal(t, false, config["disk_cache_enabled"])
	assert.EqualValues(t, 90, config["monitor_cpu_threshold"])
	assert.EqualValues(t, 95, config["monitor_disk_threshold"])

	recorder = do(http.MethodPost, "/api/performance/reset_stats", "")
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	body = decodeBody(t, recorder)
	assert.Equal(t, true, body["success"])
	assert.Equal(t, "统计信息已重置", body["message"])
	recorder = do(http.MethodGet, "/api/performance/stats", "")
	cacheStats = decodeBody(t, recorder)["data"].(map[string]any)["cache_stats"].(map[string]any)
	assert.EqualValues(t, 0, cacheStats["disk_cache_hits"])
	assert.EqualValues(t, 0, cacheStats["memory_cache_hits"])
	assert.EqualValues(t, 2, cacheStats["active_disk_files"], "reset must not erase live-usage observations")

	recorder = do(http.MethodPost, "/api/performance/gc", "")
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	body = decodeBody(t, recorder)
	assert.Equal(t, true, body["success"])
	assert.Equal(t, "GC 已执行", body["message"])
	recorder = do(http.MethodGet, "/api/performance/stats", "")
	afterGC := uint32(decodeBody(t, recorder)["data"].(map[string]any)["memory_stats"].(map[string]any)["num_gc"].(float64))
	assert.Greater(t, afterGC, beforeGC)
}

func TestPerformanceDiskCacheCleanupIsBoundedAndSymlinkSafe(t *testing.T) {
	cacheBase := t.TempDir()
	cacheDirectory := filepath.Join(cacheBase, managedPerformanceCacheDirectory)
	require.NoError(t, os.Mkdir(cacheDirectory, 0o700))
	oldTime := time.Now().Add(-11 * time.Minute)
	oldFile := filepath.Join(cacheDirectory, "old.tmp")
	recentFile := filepath.Join(cacheDirectory, "recent.tmp")
	writePerformanceFixture(t, oldFile, "old", oldTime)
	writePerformanceFixture(t, recentFile, "recent", time.Now().Add(-5*time.Minute))
	nestedDirectory := filepath.Join(cacheDirectory, "nested")
	require.NoError(t, os.Mkdir(nestedDirectory, 0o700))
	nestedFile := filepath.Join(nestedDirectory, "old.tmp")
	writePerformanceFixture(t, nestedFile, "nested", oldTime)

	outsideFile := filepath.Join(t.TempDir(), "outside.tmp")
	writePerformanceFixture(t, outsideFile, "outside", oldTime)
	linkPath := filepath.Join(cacheDirectory, "outside-link.tmp")
	if runtime.GOOS != "windows" {
		require.NoError(t, os.Symlink(outsideFile, linkPath))
	}

	t.Setenv("TOKENROUTER_DISK_CACHE_PATH", cacheBase)
	t.Setenv("TOKENROUTER_PERFORMANCE_SCAN_LIMIT", "64")
	_, do, _ := setupChannelRead(t, constant.RoleRootUser)
	recorder := do(http.MethodDelete, "/api/performance/disk_cache", "")
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	body := decodeBody(t, recorder)
	assert.Equal(t, true, body["success"])
	assert.Equal(t, "不活跃的磁盘缓存已清理", body["message"])
	assertPathMissing(t, oldFile)
	assertPathExists(t, recentFile)
	assertPathExists(t, nestedFile)
	assertPathExists(t, outsideFile)
	if runtime.GOOS != "windows" {
		assertPathExists(t, linkPath)
	}

	// A traversal spelling is rejected before opening or deleting anything.
	outsideBase := filepath.Join(cacheBase, "outside-base")
	outsideCache := filepath.Join(outsideBase, managedPerformanceCacheDirectory)
	require.NoError(t, os.MkdirAll(outsideCache, 0o700))
	outsideCacheFile := filepath.Join(outsideCache, "old.tmp")
	writePerformanceFixture(t, outsideCacheFile, "keep", oldTime)
	t.Setenv("TOKENROUTER_DISK_CACHE_PATH", cacheBase+string(filepath.Separator)+"safe"+
		string(filepath.Separator)+".."+string(filepath.Separator)+"outside-base")
	recorder = do(http.MethodDelete, "/api/performance/disk_cache", "")
	body = decodeBody(t, recorder)
	assert.Equal(t, false, body["success"])
	assert.Equal(t, "unsafe directory configuration", body["message"])
	assert.NotContains(t, recorder.Body.String(), cacheBase)
	assertPathExists(t, outsideCacheFile)

	// A configured cache directory that is itself a symlink is never followed.
	if runtime.GOOS != "windows" {
		symlinkBase := t.TempDir()
		symlinkTarget := t.TempDir()
		targetFile := filepath.Join(symlinkTarget, "old.tmp")
		writePerformanceFixture(t, targetFile, "keep", oldTime)
		require.NoError(t, os.Symlink(symlinkTarget, filepath.Join(symlinkBase, managedPerformanceCacheDirectory)))
		t.Setenv("TOKENROUTER_DISK_CACHE_PATH", symlinkBase)
		recorder = do(http.MethodGet, "/api/performance/stats", "")
		body = decodeBody(t, recorder)
		assert.Equal(t, false, body["success"])
		assert.Equal(t, "unsafe directory configuration", body["message"])
		recorder = do(http.MethodDelete, "/api/performance/disk_cache", "")
		body = decodeBody(t, recorder)
		assert.Equal(t, false, body["success"])
		assert.Equal(t, "unsafe directory configuration", body["message"])
		assertPathExists(t, targetFile)
	}

	// Entry limits are a preflight gate: exceeding one never partially cleans.
	boundedBase := t.TempDir()
	boundedCache := filepath.Join(boundedBase, managedPerformanceCacheDirectory)
	require.NoError(t, os.Mkdir(boundedCache, 0o700))
	for _, name := range []string{"one.tmp", "two.tmp", "three.tmp"} {
		writePerformanceFixture(t, filepath.Join(boundedCache, name), name, oldTime)
	}
	t.Setenv("TOKENROUTER_DISK_CACHE_PATH", boundedBase)
	t.Setenv("TOKENROUTER_PERFORMANCE_SCAN_LIMIT", "2")
	recorder = do(http.MethodGet, "/api/performance/stats", "")
	body = decodeBody(t, recorder)
	assert.Equal(t, false, body["success"])
	assert.Equal(t, "directory entry limit exceeded", body["message"])
	recorder = do(http.MethodDelete, "/api/performance/disk_cache", "")
	body = decodeBody(t, recorder)
	assert.Equal(t, false, body["success"])
	assert.Equal(t, "directory entry limit exceeded", body["message"])
	for _, name := range []string{"one.tmp", "two.tmp", "three.tmp"} {
		assertPathExists(t, filepath.Join(boundedCache, name))
	}
}

func TestPerformanceLogListAndCleanupContracts(t *testing.T) {
	logDirectory := t.TempDir()
	oldTime := time.Now().Add(-7 * 24 * time.Hour)
	newTime := time.Now().Add(-time.Hour)
	newest := filepath.Join(logDirectory, "oneapi-20260905020000.log")
	middle := filepath.Join(logDirectory, "oneapi-20260904020000.log")
	oldest := filepath.Join(logDirectory, "oneapi-20260903020000.log")
	writePerformanceFixture(t, newest, "newest", newTime)
	writePerformanceFixture(t, middle, "middle", oldTime)
	writePerformanceFixture(t, oldest, "old", oldTime.Add(-time.Hour))
	ignored := filepath.Join(logDirectory, "tokenrouter-debug.log")
	writePerformanceFixture(t, ignored, "ignored", oldTime)
	nested := filepath.Join(logDirectory, "oneapi-nested.log")
	require.NoError(t, os.Mkdir(nested, 0o700))

	outside := filepath.Join(t.TempDir(), "outside.log")
	writePerformanceFixture(t, outside, "outside", oldTime)
	logLink := filepath.Join(logDirectory, "oneapi-99999999999999.log")
	if runtime.GOOS != "windows" {
		require.NoError(t, os.Symlink(outside, logLink))
	}

	t.Setenv("TOKENROUTER_LOG_DIR", logDirectory)
	t.Setenv("TOKENROUTER_PERFORMANCE_SCAN_LIMIT", "64")
	_, do, _ := setupChannelRead(t, constant.RoleRootUser)
	recorder := do(http.MethodGet, "/api/performance/logs", "")
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	body := decodeBody(t, recorder)
	require.Equal(t, true, body["success"])
	data := body["data"].(map[string]any)
	assert.Equal(t, true, data["enabled"])
	assert.Equal(t, logDirectory, data["log_dir"])
	assert.EqualValues(t, 3, data["file_count"])
	assert.EqualValues(t, len("newest")+len("middle")+len("old"), data["total_size"])
	files := data["files"].([]any)
	require.Len(t, files, 3)
	assert.Equal(t, filepath.Base(newest), files[0].(map[string]any)["name"])
	assert.Equal(t, filepath.Base(middle), files[1].(map[string]any)["name"])
	assert.Equal(t, filepath.Base(oldest), files[2].(map[string]any)["name"])
	assert.NotEmpty(t, data["oldest_time"])
	assert.NotEmpty(t, data["newest_time"])

	recorder = do(http.MethodDelete, "/api/performance/logs?mode=by_count&value=1", "")
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	body = decodeBody(t, recorder)
	assert.Equal(t, true, body["success"])
	result := body["data"].(map[string]any)
	assert.EqualValues(t, 2, result["deleted_count"])
	assert.EqualValues(t, len("middle")+len("old"), result["freed_bytes"])
	assert.Empty(t, result["failed_files"])
	assertPathExists(t, newest)
	assertPathMissing(t, middle)
	assertPathMissing(t, oldest)
	assertPathExists(t, ignored)
	assertPathExists(t, nested)
	assertPathExists(t, outside)
	if runtime.GOOS != "windows" {
		assertPathExists(t, logLink)
	}

	oldByDays := filepath.Join(logDirectory, "oneapi-20260901000000.log")
	writePerformanceFixture(t, oldByDays, "days", oldTime)
	recorder = do(http.MethodDelete, "/api/performance/logs?mode=by_days&value=2", "")
	body = decodeBody(t, recorder)
	assert.Equal(t, true, body["success"])
	assert.EqualValues(t, 1, body["data"].(map[string]any)["deleted_count"])
	assertPathMissing(t, oldByDays)
	assertPathExists(t, newest)
}

func TestPerformanceLogMaintenanceRejectsTraversalSymlinksBoundsAndFailures(t *testing.T) {
	t.Setenv("TOKENROUTER_LOG_DIR", "")
	t.Setenv("LOG_DIR", "")
	_, do, _ := setupChannelRead(t, constant.RoleRootUser)

	recorder := do(http.MethodGet, "/api/performance/logs", "")
	body := decodeBody(t, recorder)
	assert.Equal(t, true, body["success"])
	data := body["data"].(map[string]any)
	assert.Equal(t, false, data["enabled"])
	assert.EqualValues(t, 0, data["file_count"])
	assert.Nil(t, data["files"])
	recorder = do(http.MethodDelete, "/api/performance/logs?mode=by_count&value=1", "")
	body = decodeBody(t, recorder)
	assert.Equal(t, false, body["success"])
	assert.Equal(t, "log directory not configured", body["message"])

	for _, path := range []string{
		"/api/performance/logs?mode=everything&value=1",
		"/api/performance/logs?mode=by_count&value=0",
		"/api/performance/logs?mode=by_count&value=../../tmp",
		"/api/performance/logs?mode=by_days&value=36501",
	} {
		recorder = do(http.MethodDelete, path, "")
		assert.Equal(t, false, decodeBody(t, recorder)["success"], path)
	}

	base := t.TempDir()
	outsideDirectory := filepath.Join(base, "outside")
	require.NoError(t, os.Mkdir(outsideDirectory, 0o700))
	outsideFile := filepath.Join(outsideDirectory, "oneapi-20200101000000.log")
	writePerformanceFixture(t, outsideFile, "keep", time.Now().Add(-24*time.Hour))
	t.Setenv("TOKENROUTER_LOG_DIR", base+string(filepath.Separator)+"safe"+
		string(filepath.Separator)+".."+string(filepath.Separator)+"outside")
	recorder = do(http.MethodDelete, "/api/performance/logs?mode=by_days&value=1", "")
	body = decodeBody(t, recorder)
	assert.Equal(t, false, body["success"])
	assert.Equal(t, "unsafe directory configuration", body["message"])
	assert.NotContains(t, recorder.Body.String(), base)
	assertPathExists(t, outsideFile)

	if runtime.GOOS != "windows" {
		link := filepath.Join(t.TempDir(), "logs-link")
		require.NoError(t, os.Symlink(outsideDirectory, link))
		t.Setenv("TOKENROUTER_LOG_DIR", link)
		recorder = do(http.MethodGet, "/api/performance/logs", "")
		body = decodeBody(t, recorder)
		assert.Equal(t, false, body["success"])
		assert.Equal(t, "unsafe directory configuration", body["message"])
		recorder = do(http.MethodDelete, "/api/performance/logs?mode=by_days&value=1", "")
		assert.Equal(t, false, decodeBody(t, recorder)["success"])
		assertPathExists(t, outsideFile)
	}

	// A regular file cannot be configured as the log directory, and the raw
	// path is not reflected to the client on failure.
	notDirectory := filepath.Join(t.TempDir(), "not-a-directory")
	writePerformanceFixture(t, notDirectory, "file", time.Now())
	t.Setenv("TOKENROUTER_LOG_DIR", notDirectory)
	recorder = do(http.MethodGet, "/api/performance/logs", "")
	body = decodeBody(t, recorder)
	assert.Equal(t, false, body["success"])
	assert.NotContains(t, recorder.Body.String(), notDirectory)

	boundedDirectory := t.TempDir()
	for _, name := range []string{
		"oneapi-20260903000000.log", "oneapi-20260904000000.log", "oneapi-20260905000000.log",
	} {
		writePerformanceFixture(t, filepath.Join(boundedDirectory, name), name, time.Now().Add(-24*time.Hour))
	}
	t.Setenv("TOKENROUTER_LOG_DIR", boundedDirectory)
	t.Setenv("TOKENROUTER_PERFORMANCE_SCAN_LIMIT", "2")
	recorder = do(http.MethodGet, "/api/performance/logs", "")
	body = decodeBody(t, recorder)
	assert.Equal(t, false, body["success"])
	assert.Equal(t, "directory entry limit exceeded", body["message"])
	recorder = do(http.MethodDelete, "/api/performance/logs?mode=by_count&value=1", "")
	body = decodeBody(t, recorder)
	assert.Equal(t, false, body["success"])
	assert.Equal(t, "directory entry limit exceeded", body["message"])
	for _, name := range []string{
		"oneapi-20260903000000.log", "oneapi-20260904000000.log", "oneapi-20260905000000.log",
	} {
		assertPathExists(t, filepath.Join(boundedDirectory, name))
	}
}
