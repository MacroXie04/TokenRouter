package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/model"
)

func setupChannelUpstreamUpdateTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	t.Setenv("SSRF_DISABLE", "true")
	common.InitSSRF()
	t.Cleanup(common.InitSSRF)
	dsn := "file:" + filepath.Join(t.TempDir(), "upstream-models.db") + "?_pragma=busy_timeout(5000)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Ability{}, &model.SystemTask{}, &model.SystemTaskLock{}))
	model.DB = db
	model.LOG_DB = db
	require.NoError(t, InitAbilityCache())
	return db
}

func channelUpstreamSettingsJSON(t *testing.T, settings ChannelUpstreamModelSettings, extras map[string]any) string {
	t.Helper()
	raw := map[string]json.RawMessage{}
	for key, value := range extras {
		encoded, err := common.Marshal(value)
		require.NoError(t, err)
		raw[key] = encoded
	}
	result, err := encodeChannelUpstreamModelSettings(settings, raw)
	require.NoError(t, err)
	return result
}

func createChannelForUpstreamUpdate(t *testing.T, serverURL, name, models, settings string) model.Channel {
	t.Helper()
	priority := int64(0)
	weight := uint(1)
	channel := model.Channel{
		Type: int(constant.ChannelTypeOpenAI), Key: "secret-" + name,
		BaseURL: serverURL, Name: name, Models: models, Group: GroupDefault,
		Status: constant.ChannelStatusEnabled, Priority: &priority, Weight: &weight,
		OtherSettings: settings,
	}
	require.NoError(t, CreateChannelWithAbilities(&channel))
	return channel
}

func TestCollectChannelUpstreamModelChangesHonorsMappingsAndIgnoreRules(t *testing.T) {
	local, err := normalizeUpstreamModelNames([]string{" alias ", "old", "shared"})
	require.NoError(t, err)
	upstream, err := normalizeUpstreamModelNames([]string{"mapped", "shared", "skip-exact", "skip-regex", "new"})
	require.NoError(t, err)
	add, remove := collectChannelUpstreamModelChanges(local, upstream,
		[]string{"skip-exact", "regex:^skip-.*$", "regex:[invalid"},
		map[string]string{"alias": "mapped"})
	assert.Equal(t, []string{"new"}, add)
	assert.Equal(t, []string{"old"}, remove)
}

func TestChannelUpstreamDetectionAndApplyAreAtomicAndPreserveUnknownSettings(t *testing.T) {
	setupChannelUpstreamUpdateTestDB(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/models", r.URL.Path)
		assert.Equal(t, "Bearer secret-model-lifecycle", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"mapped-target"},{"id":"new-model"}]}`))
	}))
	t.Cleanup(server.Close)

	settings := channelUpstreamSettingsJSON(t, ChannelUpstreamModelSettings{CheckEnabled: true}, map[string]any{
		"future_provider_flag": map[string]any{"enabled": true},
	})
	channel := createChannelForUpstreamUpdate(t, server.URL, "model-lifecycle", "alias-model,old-model", settings)
	channel.ModelMapping = `{"alias-model":"mapped-target"}`
	require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", channel.Id).Update("model_mapping", channel.ModelMapping).Error)

	detected, changed, err := DetectChannelUpstreamModelUpdates(context.Background(), channel.Id, true, false)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, []string{"new-model"}, detected.AddModels)
	assert.Equal(t, []string{"old-model"}, detected.RemoveModels)
	assert.Positive(t, detected.LastCheckTime)

	application, err := ApplyChannelUpstreamModelUpdates(context.Background(), channel.Id,
		[]string{"new-model", "forged-model"}, nil, []string{"old-model", "forged-remove"})
	require.NoError(t, err)
	assert.Equal(t, []string{"new-model"}, application.AddedModels)
	assert.Equal(t, []string{"old-model"}, application.RemovedModels)
	assert.Equal(t, "alias-model,new-model", application.Models)
	assert.Empty(t, application.RemainingModels)
	assert.Empty(t, application.RemainingRemoveModels)

	var reloaded model.Channel
	require.NoError(t, model.DB.First(&reloaded, channel.Id).Error)
	assert.Equal(t, "alias-model,new-model", reloaded.Models)
	assert.NotContains(t, reloaded.Models, "forged")
	var rawSettings map[string]any
	require.NoError(t, common.UnmarshalJsonStr(reloaded.OtherSettings, &rawSettings))
	assert.Contains(t, rawSettings, "future_provider_flag")

	var abilities []model.Ability
	require.NoError(t, model.DB.Where("channel_id = ?", channel.Id).Order("model asc").Find(&abilities).Error)
	require.Len(t, abilities, 2)
	assert.Equal(t, "alias-model", abilities[0].Model)
	assert.Equal(t, "new-model", abilities[1].Model)

	// A stale replay cannot inject a model after the staged list is consumed.
	replayed, err := ApplyChannelUpstreamModelUpdates(context.Background(), channel.Id,
		[]string{"new-model", "forged-model"}, nil, []string{"old-model"})
	require.NoError(t, err)
	assert.False(t, replayed.ModelsChanged)
	assert.Empty(t, replayed.AddedModels)
	assert.Empty(t, replayed.RemovedModels)
}

func TestChannelUpstreamApplyRollbackIncludesAbilitiesAndSettings(t *testing.T) {
	db := setupChannelUpstreamUpdateTestDB(t)
	settings := channelUpstreamSettingsJSON(t, ChannelUpstreamModelSettings{
		CheckEnabled: true, LastDetectedModels: []string{"new-model"}, LastRemovedModels: []string{"old-model"},
	}, map[string]any{"future": "preserve"})
	channel := createChannelForUpstreamUpdate(t, "https://example.invalid", "rollback", "old-model", settings)
	require.NoError(t, db.Exec(`CREATE TRIGGER reject_new_model BEFORE INSERT ON abilities
		WHEN NEW.model = 'new-model' BEGIN SELECT RAISE(ABORT, 'injected ability failure'); END`).Error)

	_, err := ApplyChannelUpstreamModelUpdates(context.Background(), channel.Id,
		[]string{"new-model"}, nil, []string{"old-model"})
	require.ErrorContains(t, err, "injected ability failure")

	var reloaded model.Channel
	require.NoError(t, db.First(&reloaded, channel.Id).Error)
	assert.Equal(t, "old-model", reloaded.Models)
	assert.JSONEq(t, settings, reloaded.OtherSettings)
	var abilities []model.Ability
	require.NoError(t, db.Where("channel_id = ?", channel.Id).Find(&abilities).Error)
	require.Len(t, abilities, 1)
	assert.Equal(t, "old-model", abilities[0].Model)
}

func TestChannelUpstreamDetectionFailurePreservesPendingChanges(t *testing.T) {
	setupChannelUpstreamUpdateTestDB(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"access_token":"must-not-surface"}`))
	}))
	t.Cleanup(server.Close)
	settings := channelUpstreamSettingsJSON(t, ChannelUpstreamModelSettings{
		CheckEnabled: true, LastDetectedModels: []string{"pending-add"}, LastRemovedModels: []string{"pending-remove"},
	}, nil)
	channel := createChannelForUpstreamUpdate(t, server.URL, "failure", "stable-model", settings)

	_, _, err := DetectChannelUpstreamModelUpdates(context.Background(), channel.Id, true, false)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), channel.Key)
	assert.NotContains(t, err.Error(), "must-not-surface")

	var reloaded model.Channel
	require.NoError(t, model.DB.First(&reloaded, channel.Id).Error)
	persisted, _, decodeErr := decodeChannelUpstreamModelSettings(reloaded.OtherSettings)
	require.NoError(t, decodeErr)
	assert.Positive(t, persisted.LastCheckTime)
	assert.Equal(t, []string{"pending-add"}, persisted.LastDetectedModels)
	assert.Equal(t, []string{"pending-remove"}, persisted.LastRemovedModels)
	assert.Equal(t, "stable-model", reloaded.Models)
}

func TestChannelUpstreamDetectionRejectsResponseFromStaleEndpoint(t *testing.T) {
	setupChannelUpstreamUpdateTestDB(t)
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		started <- struct{}{}
		<-release
		_, _ = w.Write([]byte(`{"data":[{"id":"stale-provider-model"}]}`))
	}))
	t.Cleanup(server.Close)
	channel := createChannelForUpstreamUpdate(t, server.URL, "stale-endpoint", "local-model",
		channelUpstreamSettingsJSON(t, ChannelUpstreamModelSettings{CheckEnabled: true}, nil))

	type outcome struct {
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		_, _, err := DetectChannelUpstreamModelUpdates(context.Background(), channel.Id, true, false)
		done <- outcome{err: err}
	}()
	<-started
	require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", channel.Id).
		Update("base_url", "https://new-endpoint.invalid").Error)
	close(release)
	result := <-done
	require.ErrorIs(t, result.err, ErrChannelChangedDuringDiscovery)

	var reloaded model.Channel
	require.NoError(t, model.DB.First(&reloaded, channel.Id).Error)
	settings, _, err := decodeChannelUpstreamModelSettings(reloaded.OtherSettings)
	require.NoError(t, err)
	assert.Zero(t, settings.LastCheckTime)
	assert.Empty(t, settings.LastDetectedModels)
	assert.Equal(t, "local-model", reloaded.Models)
}

func TestScheduledChannelUpstreamDetectionAutoAddsOnlyAndHonorsInterval(t *testing.T) {
	setupChannelUpstreamUpdateTestDB(t)
	var mu sync.Mutex
	models := []string{"old-model", "new-model"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		current := append([]string(nil), models...)
		mu.Unlock()
		parts := make([]string, 0, len(current))
		for _, modelName := range current {
			parts = append(parts, fmt.Sprintf(`{"id":%q}`, modelName))
		}
		_, _ = w.Write([]byte(`{"data":[` + strings.Join(parts, ",") + `]}`))
	}))
	t.Cleanup(server.Close)
	settings := channelUpstreamSettingsJSON(t, ChannelUpstreamModelSettings{
		CheckEnabled: true, AutoSyncEnabled: true,
	}, nil)
	channel := createChannelForUpstreamUpdate(t, server.URL, "auto", "old-model,removed-model", settings)

	first, changed, err := DetectChannelUpstreamModelUpdates(context.Background(), channel.Id, false, true)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, 1, first.AutoAddedModels)
	assert.Empty(t, first.AddModels)
	assert.Equal(t, []string{"removed-model"}, first.RemoveModels, "scheduled auto-sync never auto-removes")

	mu.Lock()
	models = []string{"old-model", "new-model", "later-model"}
	mu.Unlock()
	second, changed, err := DetectChannelUpstreamModelUpdates(context.Background(), channel.Id, false, true)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, first.LastCheckTime, second.LastCheckTime)
	assert.Empty(t, second.AddModels, "minimum interval skips the second upstream request result")

	manual, changed, err := DetectChannelUpstreamModelUpdates(context.Background(), channel.Id, true, false)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Equal(t, []string{"later-model"}, manual.AddModels)
}

func TestApplyAllChannelUpstreamModelUpdatesScopesAndConcurrentReplay(t *testing.T) {
	setupChannelUpstreamUpdateTestDB(t)
	pending := channelUpstreamSettingsJSON(t, ChannelUpstreamModelSettings{
		CheckEnabled: true, LastDetectedModels: []string{"new"}, LastRemovedModels: []string{"old"},
	}, nil)
	first := createChannelForUpstreamUpdate(t, "https://example.invalid", "bulk-first", "old", pending)
	second := createChannelForUpstreamUpdate(t, "https://example.invalid", "bulk-second", "old", pending)
	disabled := createChannelForUpstreamUpdate(t, "https://example.invalid", "bulk-disabled", "old", pending)
	require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", disabled.Id).Update("status", constant.ChannelStatusManuallyDisabled).Error)
	notOptedIn := createChannelForUpstreamUpdate(t, "https://example.invalid", "bulk-off", "old",
		channelUpstreamSettingsJSON(t, ChannelUpstreamModelSettings{LastDetectedModels: []string{"new"}}, nil))

	var wg sync.WaitGroup
	results := make(chan *ApplyAllChannelUpstreamSummary, 2)
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := ApplyAllChannelUpstreamModelUpdates(context.Background())
			results <- result
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	processed := 0
	for err := range errs {
		require.NoError(t, err)
	}
	for result := range results {
		processed += result.ProcessedChannels
	}
	assert.Equal(t, 2, processed, "concurrent replay consumes each staged channel exactly once")
	for _, channel := range []model.Channel{first, second} {
		var reloaded model.Channel
		require.NoError(t, model.DB.First(&reloaded, channel.Id).Error)
		assert.Equal(t, "new", reloaded.Models)
	}
	for _, channel := range []model.Channel{disabled, notOptedIn} {
		var reloaded model.Channel
		require.NoError(t, model.DB.First(&reloaded, channel.Id).Error)
		assert.Equal(t, "old", reloaded.Models)
	}
}

func TestChannelUpstreamModelUpdateDurableTaskLifecycle(t *testing.T) {
	setupChannelUpstreamUpdateTestDB(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"old"},{"id":"new"}]}`))
	}))
	t.Cleanup(server.Close)
	settings := channelUpstreamSettingsJSON(t, ChannelUpstreamModelSettings{CheckEnabled: true, AutoSyncEnabled: true}, nil)
	channel := createChannelForUpstreamUpdate(t, server.URL, "task", "old", settings)

	task, created, err := EnqueueSystemTask(model.SystemTaskTypeModelUpdate, modelUpdateTaskPayload{Manual: true})
	require.NoError(t, err)
	require.True(t, created)
	require.NoError(t, RunPendingSystemTasksOnce())

	reloadedTask, err := model.GetSystemTaskByTaskID(task.TaskID)
	require.NoError(t, err)
	require.NotNil(t, reloadedTask)
	assert.Equal(t, model.SystemTaskStatusSucceeded, reloadedTask.Status, reloadedTask.Error)
	var summary ChannelUpstreamUpdateSummary
	require.NoError(t, common.UnmarshalJsonStr(reloadedTask.Result, &summary))
	assert.Equal(t, 1, summary.CheckedChannels)
	assert.Equal(t, 1, summary.ChangedChannels)
	assert.Equal(t, 1, summary.DetectedAddModels)
	assert.Equal(t, 0, summary.AutoAddedModels, "manual task must stage rather than auto-apply")
	assert.Contains(t, reloadedTask.State, `"progress":100`)

	var reloadedChannel model.Channel
	require.NoError(t, model.DB.First(&reloadedChannel, channel.Id).Error)
	assert.Equal(t, "old", reloadedChannel.Models)
	persisted, _, err := decodeChannelUpstreamModelSettings(reloadedChannel.OtherSettings)
	require.NoError(t, err)
	assert.Equal(t, []string{"new"}, persisted.LastDetectedModels)
}

func TestScheduledChannelUpstreamDetectionRejectsConcurrentOptOut(t *testing.T) {
	setupChannelUpstreamUpdateTestDB(t)
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		started <- struct{}{}
		<-release
		_, _ = w.Write([]byte(`{"data":[{"id":"new-model"}]}`))
	}))
	t.Cleanup(server.Close)
	channel := createChannelForUpstreamUpdate(t, server.URL, "concurrent-opt-out", "old-model",
		channelUpstreamSettingsJSON(t, ChannelUpstreamModelSettings{CheckEnabled: true, AutoSyncEnabled: true}, nil))

	done := make(chan error, 1)
	go func() {
		_, _, err := detectChannelUpstreamModelUpdates(context.Background(), channel.Id, true, true, false, true)
		done <- err
	}()
	<-started
	disabledSettings := channelUpstreamSettingsJSON(t, ChannelUpstreamModelSettings{}, map[string]any{"preserved": true})
	require.NoError(t, model.DB.Model(&model.Channel{}).Where("id = ?", channel.Id).Update("settings", disabledSettings).Error)
	close(release)
	require.ErrorIs(t, <-done, ErrChannelChangedDuringDiscovery)

	var reloaded model.Channel
	require.NoError(t, model.DB.First(&reloaded, channel.Id).Error)
	assert.Equal(t, "old-model", reloaded.Models)
	assert.JSONEq(t, disabledSettings, reloaded.OtherSettings)
}

func TestModelUpdateTaskWithNoEligibleChannelsCompletesAtOneHundredPercent(t *testing.T) {
	setupChannelUpstreamUpdateTestDB(t)
	task, created, err := EnqueueSystemTask(model.SystemTaskTypeModelUpdate, modelUpdateTaskPayload{Manual: true})
	require.NoError(t, err)
	require.True(t, created)
	require.NoError(t, RunPendingSystemTasksOnce())

	reloaded, err := model.GetSystemTaskByTaskID(task.TaskID)
	require.NoError(t, err)
	require.NotNil(t, reloaded)
	assert.Equal(t, model.SystemTaskStatusSucceeded, reloaded.Status, reloaded.Error)
	assert.JSONEq(t, `{"processed":0,"total":0,"progress":100}`, reloaded.State)
	assert.JSONEq(t, `{"checked_channels":0,"changed_channels":0,"detected_add_models":0,"detected_remove_models":0,"failed_channels":0,"auto_added_models":0}`, reloaded.Result)

	err = persistChannelUpstreamCheckTime(context.Background(), nil, common.NowTimestamp(), true)
	require.ErrorContains(t, err, "snapshot is nil")
}

func TestChannelUpstreamModelInputBoundsAndCancellation(t *testing.T) {
	setupChannelUpstreamUpdateTestDB(t)
	tooLong := strings.Repeat("m", maxChannelUpstreamModelNameBytes+1)
	_, err := normalizeUpstreamModelNames([]string{tooLong})
	require.ErrorContains(t, err, "at most")
	_, err = normalizeUpstreamModelNames([]string{"valid\ninvalid"})
	require.ErrorContains(t, err, "control")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err = DetectChannelUpstreamModelUpdates(ctx, 1, true, false)
	require.ErrorIs(t, err, context.Canceled)

	// A provider that outlives the request cannot cause a post-cancellation
	// write. The shared client observes the request context.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)
	channel := createChannelForUpstreamUpdate(t, server.URL, "cancel", "stable",
		channelUpstreamSettingsJSON(t, ChannelUpstreamModelSettings{CheckEnabled: true}, nil))
	timed, stop := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer stop()
	_, _, err = DetectChannelUpstreamModelUpdates(timed, channel.Id, true, false)
	require.Error(t, err)
	var reloaded model.Channel
	require.NoError(t, model.DB.First(&reloaded, channel.Id).Error)
	persisted, _, decodeErr := decodeChannelUpstreamModelSettings(reloaded.OtherSettings)
	require.NoError(t, decodeErr)
	assert.Zero(t, persisted.LastCheckTime)
}
