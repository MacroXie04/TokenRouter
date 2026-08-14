package service

import (
	"crypto/tls"
	"net/http"
	"strings"
	"time"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/model"
)

// healthHTTPClient dials upstreams through the SSRF guard.
var healthHTTPClient = &http.Client{
	Timeout: 15 * time.Second,
	Transport: &http.Transport{
		DialContext:     common.SafeDialContext,
		TLSClientConfig: &tls.Config{InsecureSkipVerify: common.GetEnvBool("TLS_INSECURE_SKIP_VERIFY", false)},
	},
}

// TestChannelHealth performs a minimal chat-completion against a channel and
// reports success and latency. It is used by the channel-test endpoint and the
// periodic auto-disable job.
func TestChannelHealth(channelId int) (bool, int) {
	success, latency, _ := TestChannelHealthDetailed(channelId, "")
	return success, latency
}

// TestChannelHealthDetailed is TestChannelHealth plus an optional test-model
// override and a human-readable error message for the dashboard channel-test
// contract.
func TestChannelHealthDetailed(channelId int, modelOverride string) (bool, int, string) {
	channel, err := GetChannelByID(channelId)
	if err != nil {
		return false, 0, "channel not found"
	}
	modelName := strings.TrimSpace(modelOverride)
	if modelName == "" {
		modelName = channel.TestModel
	}
	if modelName == "" {
		modelName = firstModel(channel.Models)
	}
	if modelName == "" {
		return false, 0, "channel has no test model"
	}

	base := channel.BaseURL
	if base == "" {
		base = "https://api.openai.com"
	}
	url := strings.TrimSuffix(base, "/") + "/v1/chat/completions"
	body := `{"model":"` + modelName + `","messages":[{"role":"user","content":"ping"}],"max_tokens":1}`
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		return false, 0, err.Error()
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+channel.Key)

	start := time.Now()
	resp, err := healthHTTPClient.Do(req)
	latency := int(time.Since(start).Milliseconds())
	if err != nil {
		return false, latency, err.Error()
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return false, latency, "upstream returned status " + resp.Status
	}
	return true, latency, ""
}

func firstModel(models string) string {
	for _, m := range strings.Split(models, ",") {
		if m = strings.TrimSpace(m); m != "" {
			return m
		}
	}
	return ""
}

// testAndRecordChannel runs the health test for one channel and persists the
// outcome: response_time/test_time are always updated, and auto-ban channels
// are disabled on failure / re-enabled on recovery.
func testAndRecordChannel(ch *model.Channel, now int64) (bool, int) {
	success, latency := TestChannelHealth(ch.Id)
	_ = model.DB.Model(ch).Updates(map[string]any{"response_time": latency, "test_time": now}).Error
	if ch.AutoBan != nil && *ch.AutoBan > 0 {
		if !success && ch.Status == constant.ChannelStatusEnabled {
			_ = model.DB.Model(ch).Update("status", constant.ChannelStatusAutoDisabled).Error
		} else if success && ch.Status == constant.ChannelStatusAutoDisabled {
			_ = model.DB.Model(ch).Update("status", constant.ChannelStatusEnabled).Error
		}
	}
	return success, latency
}

// RunChannelHealthTests tests every channel that has a test model, records its
// response time, and auto-disables failing channels (or re-enables recovered
// ones) when the channel opts into auto-ban.
func RunChannelHealthTests() error {
	var channels []model.Channel
	if err := model.DB.Find(&channels).Error; err != nil {
		return err
	}
	now := common.NowTimestamp()
	for i := range channels {
		ch := &channels[i]
		if ch.TestModel == "" && ch.Models == "" {
			continue
		}
		testAndRecordChannel(ch, now)
	}
	return nil
}
