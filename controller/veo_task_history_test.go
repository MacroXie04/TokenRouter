package controller

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tokenrouter/tokenrouter/model"
)

func TestSafeVeoTaskHistoryDataEnforcesTerminalOutputInvariant(t *testing.T) {
	valid := []string{
		`{"state":"processing"}`,
		`{"state":"failed","error_code":"POLICY"}`,
		`{"state":"succeeded","result_url":"https://result.example/video.mp4"}`,
		`{"state":"succeeded","has_inline_video":true,"inline_mime_type":"video/mp4","inline_bytes":24}`,
	}
	for _, raw := range valid {
		assert.NotEqual(t, "null", string(safeVeoTaskHistoryData(raw)), raw)
	}
	invalid := []string{
		`{"state":"submitted"}`,
		`{"state":"processing","result_url":"https://result.example/video.mp4"}`,
		`{"state":"failed","has_inline_video":true,"inline_mime_type":"video/mp4","inline_bytes":24}`,
		`{"state":"succeeded"}`,
		`{"state":"succeeded","result_url":"https://result.example/a.mp4","has_inline_video":true,"inline_mime_type":"video/mp4","inline_bytes":24}`,
		`{"state":"succeeded","has_inline_video":true,"inline_mime_type":"text/plain","inline_bytes":24}`,
	}
	for _, raw := range invalid {
		assert.Equal(t, "null", string(safeVeoTaskHistoryData(raw)), raw)
		assert.Empty(t, taskResultURL(model.Task{Platform: model.TaskOperationPlatformGeminiVeo, Data: raw}), raw)
	}
}
