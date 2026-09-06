package public

import (
	"encoding/json"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tokenrouter/tokenrouter/internal/platform/httpx"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
)

type uptimeKumaRoundTripFunc func(*http.Request) (*http.Response, error)

func (roundTrip uptimeKumaRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

func uptimeKumaResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode:    status,
		Header:        make(http.Header),
		Body:          io.NopCloser(strings.NewReader(body)),
		ContentLength: -1,
	}
}

func TestUptimeKumaStatusFetchesBoundedGroupsAndIsolatesFailures(t *testing.T) {
	initSetupTestDB(t)
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.ConsoleUptimeKumaEnabledOption: "true",
		setting.ConsoleUptimeKumaGroupsOption: `[` +
			`{"categoryName":"Core","url":"https://status.example.test/root","slug":"public"},` +
			`{"categoryName":"Unavailable","url":"https://down.example.test","slug":"down"}` +
			`]`,
	}))

	productionClient := uptimeKumaHTTPClient
	t.Cleanup(func() { uptimeKumaHTTPClient = productionClient })
	var lock sync.Mutex
	paths := []string{}
	uptimeKumaHTTPClient = &http.Client{Transport: uptimeKumaRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		lock.Lock()
		paths = append(paths, request.URL.String())
		lock.Unlock()
		if request.URL.Host == "down.example.test" {
			return uptimeKumaResponse(http.StatusBadGateway, "upstream secret"), nil
		}
		switch request.URL.Path {
		case "/root/api/status-page/public":
			return uptimeKumaResponse(http.StatusOK, `{"publicGroupList":[{"name":"APIs","monitorList":[{"id":7,"name":"Chat API"},{"id":8,"name":"Image API"}]}]}`), nil
		case "/root/api/status-page/heartbeat/public":
			return uptimeKumaResponse(http.StatusOK, `{"heartbeatList":{"7":[{"status":1}],"8":[{"status":2}]},"uptimeList":{"7_24":0.9995,"8_24":0.975}}`), nil
		default:
			return uptimeKumaResponse(http.StatusNotFound, "not found"), nil
		}
	})}

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/api/uptime/status", GetUptimeKumaStatus)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/uptime/status", nil))
	require.Equal(t, http.StatusOK, recorder.Code)
	var payload struct {
		Success bool                    `json:"success"`
		Message string                  `json:"message"`
		Data    []UptimeKumaGroupResult `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
	assert.True(t, payload.Success)
	assert.Empty(t, payload.Message)
	require.Len(t, payload.Data, 2)
	assert.Equal(t, "Core", payload.Data[0].CategoryName)
	require.Len(t, payload.Data[0].Monitors, 2)
	assert.Equal(t, UptimeKumaMonitor{Name: "Chat API", Group: "APIs", Uptime: 0.9995, Status: 1}, payload.Data[0].Monitors[0])
	assert.Equal(t, "Unavailable", payload.Data[1].CategoryName)
	assert.Empty(t, payload.Data[1].Monitors)
	assert.NotContains(t, recorder.Body.String(), "upstream secret")
	assert.ElementsMatch(t, []string{
		"https://status.example.test/root/api/status-page/public",
		"https://status.example.test/root/api/status-page/heartbeat/public",
		"https://down.example.test/api/status-page/down",
		"https://down.example.test/api/status-page/heartbeat/down",
	}, paths)
}

func TestUptimeKumaStatusSkipsOutboundRequestsWhenDisabled(t *testing.T) {
	initSetupTestDB(t)
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.ConsoleUptimeKumaEnabledOption: "false",
		setting.ConsoleUptimeKumaGroupsOption:  `[{"categoryName":"Core","url":"https://status.example.test","slug":"public"}]`,
	}))
	productionClient := uptimeKumaHTTPClient
	t.Cleanup(func() { uptimeKumaHTTPClient = productionClient })
	calls := 0
	uptimeKumaHTTPClient = &http.Client{Transport: uptimeKumaRoundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return uptimeKumaResponse(http.StatusOK, `{}`), nil
	})}

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodGet, "/api/uptime/status", nil)
	GetUptimeKumaStatus(context)
	assert.JSONEq(t, `{"success":true,"message":"","data":[]}`, recorder.Body.String())
	assert.Zero(t, calls)
}

func TestUptimeKumaStatusFailsClosedForMalformedOrOversizedProviderData(t *testing.T) {
	productionClient := uptimeKumaHTTPClient
	t.Cleanup(func() { uptimeKumaHTTPClient = productionClient })
	group := setting.ConsoleUptimeKumaGroup{
		CategoryName: "Core", URL: "https://status.example.test", Slug: "public",
	}
	tests := []struct {
		name      string
		status    string
		heartbeat string
	}{
		{
			name:      "duplicate monitor id",
			status:    `{"publicGroupList":[{"name":"APIs","monitorList":[{"id":7,"name":"One"},{"id":7,"name":"Two"}]}]}`,
			heartbeat: `{"heartbeatList":{"7":[{"status":1}]},"uptimeList":{"7_24":1}}`,
		},
		{
			name:      "out of range heartbeat",
			status:    `{"publicGroupList":[{"name":"APIs","monitorList":[{"id":7,"name":"One"}]}]}`,
			heartbeat: `{"heartbeatList":{"7":[{"status":4}]},"uptimeList":{"7_24":1}}`,
		},
		{
			name:      "trailing json",
			status:    `{"publicGroupList":[]} {"second":true}`,
			heartbeat: `{"heartbeatList":{},"uptimeList":{}}`,
		},
		{
			// Automatic HTTP decompression presents an unknown-length expanded
			// reader to this layer. The body limit must remain authoritative even
			// when Content-Length cannot reject the response early.
			name:      "unknown length decompression-expanded response",
			status:    `{"publicGroupList":[],"padding":"` + strings.Repeat("x", uptimeKumaMaxResponseBytes) + `"}`,
			heartbeat: `{"heartbeatList":{},"uptimeList":{}}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			uptimeKumaHTTPClient = &http.Client{Transport: uptimeKumaRoundTripFunc(func(request *http.Request) (*http.Response, error) {
				if strings.Contains(request.URL.Path, "/heartbeat/") {
					return uptimeKumaResponse(http.StatusOK, test.heartbeat), nil
				}
				return uptimeKumaResponse(http.StatusOK, test.status), nil
			})}
			result := fetchUptimeKumaGroup(t.Context(), group)
			assert.Equal(t, "Core", result.CategoryName)
			assert.Empty(t, result.Monitors)
		})
	}
}

func TestUptimeKumaPublicResponseHasAnEncodedSizeLimit(t *testing.T) {
	monitors := make([]UptimeKumaMonitor, uptimeKumaMaxMonitors)
	for index := range monitors {
		monitors[index] = UptimeKumaMonitor{
			Name: strings.Repeat("<", uptimeKumaMaxNameBytes), Group: strings.Repeat(">", uptimeKumaMaxNameBytes),
			Uptime: 1, Status: 1,
		}
	}
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	writeUptimeKumaResponse(context, []UptimeKumaGroupResult{{CategoryName: "Core", Monitors: monitors}})

	assert.LessOrEqual(t, recorder.Body.Len(), uptimeKumaMaxPublicJSONBytes)
	assert.JSONEq(t, `{"success":true,"message":"","data":[]}`, recorder.Body.String())
}

func TestUptimeKumaProductionClientCannotProxyOrFollowRedirects(t *testing.T) {
	transport, ok := uptimeKumaHTTPClient.Transport.(*http.Transport)
	require.True(t, ok)
	assert.Nil(t, transport.Proxy)
	assert.NotNil(t, transport.DialContext)
	assert.Equal(t, reflect.ValueOf(httpx.SafeDialContext).Pointer(), reflect.ValueOf(transport.DialContext).Pointer())
	require.NotNil(t, uptimeKumaHTTPClient.CheckRedirect)
	request := httptest.NewRequest(http.MethodGet, "https://status.example.test/next", nil)
	assert.ErrorIs(t, uptimeKumaHTTPClient.CheckRedirect(request, nil), http.ErrUseLastResponse)
}
