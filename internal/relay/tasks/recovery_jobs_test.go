package tasks

import (
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"reflect"
	"testing"
)

func TestRecoveryJobsKeepProviderOrderingAndDatabaseOnlyJimengCallback(t *testing.T) {
	jobs := AsyncRecoveryJobs()
	names := make([]string, 0, len(jobs))
	for _, job := range jobs {
		names = append(names, job.Name)
		require.NotNil(t, job.Promote, job.Name)
		require.NotNil(t, job.Reconcile, job.Name)
	}
	assert.Equal(t, []string{"ali-wan", "doubao", "gemini-vertex-veo", "hailuo", "kling", "midjourney", "suno", "video", "vidu"}, names)
	jobs[0].Promote = nil
	require.NotNil(t, AsyncRecoveryJobs()[0].Promote, "callers must own their assembly slice")
	jimeng := JimengRecoveryJob()
	assert.Equal(t, "jimeng", jimeng.Name)
	assert.Equal(t, reflect.ValueOf(PromoteJimengTaskRecoveryContext).Pointer(), reflect.ValueOf(jimeng.Promote).Pointer())
	assert.Equal(t, reflect.ValueOf(reconcileJimengTaskOperationsDatabaseContext).Pointer(), reflect.ValueOf(jimeng.Reconcile).Pointer(), "leased recovery must not promote journals a second time")
}
