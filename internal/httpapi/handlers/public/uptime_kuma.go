package public

import (
	"context"
	"errors"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/tokenrouter/tokenrouter/internal/platform/httpx"
	"github.com/tokenrouter/tokenrouter/internal/platform/jsonutil"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	uptimeKumaRequestTimeout     = 12 * time.Second
	uptimeKumaHTTPTimeout        = 5 * time.Second
	uptimeKumaMaxResponseBytes   = 1 << 20
	uptimeKumaMaxPublicGroups    = 100
	uptimeKumaMaxMonitors        = 2_000
	uptimeKumaMaxHeartbeatKeys   = 4_000
	uptimeKumaMaxHeartbeatsPerID = 100
	uptimeKumaMaxNameBytes       = 256
	uptimeKumaMaxPublicJSONBytes = 2 << 20
)

// UptimeKumaMonitor is the small, bounded monitor shape exposed publicly.
type UptimeKumaMonitor struct {
	Name   string  `json:"name"`
	Uptime float64 `json:"uptime"`
	Status int     `json:"status"`
	Group  string  `json:"group,omitempty"`
}

// UptimeKumaGroupResult preserves the configured category order even when an
// individual remote status page is unavailable.
type UptimeKumaGroupResult struct {
	CategoryName string              `json:"categoryName"`
	Monitors     []UptimeKumaMonitor `json:"monitors"`
}

var uptimeKumaHTTPClient = &http.Client{
	Timeout: uptimeKumaHTTPTimeout,
	Transport: &http.Transport{
		Proxy:                 nil,
		DialContext:           httpx.SafeDialContext,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 5 * time.Second,
		IdleConnTimeout:       30 * time.Second,
	},
	CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

type uptimeKumaStatusPayload struct {
	PublicGroupList []struct {
		Name        string `json:"name"`
		MonitorList []struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		} `json:"monitorList"`
	} `json:"publicGroupList"`
}

type uptimeKumaHeartbeatPayload struct {
	HeartbeatList map[string][]struct {
		Status int `json:"status"`
	} `json:"heartbeatList"`
	UptimeList map[string]float64 `json:"uptimeList"`
}

// GetUptimeKumaStatus resolves every enabled status-page group independently.
// Remote failures are represented by an empty group and are never reflected
// to unauthenticated callers.
func GetUptimeKumaStatus(c *gin.Context) {
	consoleContent := setting.GetConsoleContentSetting()
	if !consoleContent.UptimeKumaEnabled || len(consoleContent.UptimeKumaGroups) == 0 {
		writeUptimeKumaResponse(c, []UptimeKumaGroupResult{})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), uptimeKumaRequestTimeout)
	defer cancel()
	results := make([]UptimeKumaGroupResult, len(consoleContent.UptimeKumaGroups))
	var wait sync.WaitGroup
	wait.Add(len(consoleContent.UptimeKumaGroups))
	for index := range consoleContent.UptimeKumaGroups {
		index := index
		go func() {
			defer wait.Done()
			results[index] = fetchUptimeKumaGroup(ctx, consoleContent.UptimeKumaGroups[index])
		}()
	}
	wait.Wait()
	writeUptimeKumaResponse(c, results)
}

func writeUptimeKumaResponse(c *gin.Context, results []UptimeKumaGroupResult) {
	payload := struct {
		Success bool                    `json:"success"`
		Message string                  `json:"message"`
		Data    []UptimeKumaGroupResult `json:"data"`
	}{Success: true, Message: "", Data: results}
	encoded, err := jsonutil.Marshal(payload)
	if err != nil || len(encoded) > uptimeKumaMaxPublicJSONBytes {
		payload.Data = []UptimeKumaGroupResult{}
		encoded, _ = jsonutil.Marshal(payload)
	}
	c.Data(http.StatusOK, "application/json; charset=utf-8", encoded)
}

func fetchUptimeKumaGroup(ctx context.Context, group setting.ConsoleUptimeKumaGroup) UptimeKumaGroupResult {
	result := UptimeKumaGroupResult{CategoryName: group.CategoryName, Monitors: []UptimeKumaMonitor{}}
	baseURL := strings.TrimSuffix(group.URL, "/")
	statusURL := baseURL + "/api/status-page/" + group.Slug
	heartbeatURL := baseURL + "/api/status-page/heartbeat/" + group.Slug
	groupContext, cancelGroup := context.WithCancel(ctx)
	defer cancelGroup()

	var status uptimeKumaStatusPayload
	var heartbeat uptimeKumaHeartbeatPayload
	var statusErr, heartbeatErr error
	var wait sync.WaitGroup
	wait.Add(2)
	go func() {
		defer wait.Done()
		statusErr = getBoundedUptimeKumaJSON(groupContext, statusURL, &status)
		if statusErr != nil {
			cancelGroup()
		}
	}()
	go func() {
		defer wait.Done()
		heartbeatErr = getBoundedUptimeKumaJSON(groupContext, heartbeatURL, &heartbeat)
		if heartbeatErr != nil {
			cancelGroup()
		}
	}()
	wait.Wait()
	if statusErr != nil || heartbeatErr != nil || !validUptimeKumaPayloads(status, heartbeat) {
		return result
	}

	for _, publicGroup := range status.PublicGroupList {
		for _, monitor := range publicGroup.MonitorList {
			monitorID := strconv.FormatInt(monitor.ID, 10)
			item := UptimeKumaMonitor{Name: monitor.Name, Group: publicGroup.Name}
			if uptime, exists := heartbeat.UptimeList[monitorID+"_24"]; exists {
				item.Uptime = uptime
			}
			if heartbeats := heartbeat.HeartbeatList[monitorID]; len(heartbeats) > 0 {
				item.Status = heartbeats[0].Status
			}
			result.Monitors = append(result.Monitors, item)
		}
	}
	return result
}

func getBoundedUptimeKumaJSON(ctx context.Context, endpoint string, destination any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	response, err := uptimeKumaHTTPClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return errors.New("uptime provider returned a non-success response")
	}
	if response.ContentLength > uptimeKumaMaxResponseBytes {
		return errors.New("uptime provider response exceeds the safe size limit")
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, uptimeKumaMaxResponseBytes+1))
	if err != nil {
		return err
	}
	if len(payload) > uptimeKumaMaxResponseBytes {
		return errors.New("uptime provider response exceeds the safe size limit")
	}
	if err := jsonutil.Unmarshal(payload, destination); err != nil {
		return fmt.Errorf("decode uptime provider response: %w", err)
	}
	return nil
}

func validUptimeKumaPayloads(status uptimeKumaStatusPayload, heartbeat uptimeKumaHeartbeatPayload) bool {
	if status.PublicGroupList == nil || heartbeat.HeartbeatList == nil || heartbeat.UptimeList == nil ||
		len(status.PublicGroupList) > uptimeKumaMaxPublicGroups ||
		len(heartbeat.HeartbeatList) > uptimeKumaMaxHeartbeatKeys ||
		len(heartbeat.UptimeList) > uptimeKumaMaxHeartbeatKeys {
		return false
	}
	monitorCount := 0
	seenIDs := make(map[int64]struct{})
	for _, publicGroup := range status.PublicGroupList {
		if !validUptimeKumaText(publicGroup.Name, true) {
			return false
		}
		monitorCount += len(publicGroup.MonitorList)
		if monitorCount > uptimeKumaMaxMonitors {
			return false
		}
		for _, monitor := range publicGroup.MonitorList {
			if monitor.ID <= 0 || monitor.ID > 1<<53-1 || !validUptimeKumaText(monitor.Name, false) {
				return false
			}
			if _, duplicate := seenIDs[monitor.ID]; duplicate {
				return false
			}
			seenIDs[monitor.ID] = struct{}{}
		}
	}
	for key, values := range heartbeat.HeartbeatList {
		if len(key) > 32 || len(values) > uptimeKumaMaxHeartbeatsPerID {
			return false
		}
		for _, value := range values {
			if value.Status < 0 || value.Status > 3 {
				return false
			}
		}
	}
	for key, uptime := range heartbeat.UptimeList {
		if len(key) > 40 || math.IsNaN(uptime) || math.IsInf(uptime, 0) || uptime < 0 || uptime > 1 {
			return false
		}
	}
	return true
}

func validUptimeKumaText(value string, allowEmpty bool) bool {
	if len(value) > uptimeKumaMaxNameBytes || !utf8.ValidString(value) || value != strings.TrimSpace(value) {
		return false
	}
	if value == "" {
		return allowEmpty
	}
	for _, character := range value {
		if character < 0x20 || (character >= 0x7f && character <= 0x9f) || character == 0x061c ||
			character == 0x200e || character == 0x200f ||
			(character >= 0x202a && character <= 0x202e) ||
			(character >= 0x2066 && character <= 0x2069) {
			return false
		}
	}
	return true
}
