package engine

import (
	"context"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/middleware"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type relayDeadlineRoundTripFunc func(*http.Request) (*http.Response, error)

func (roundTrip relayDeadlineRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

type relayDeadlineObservation struct {
	elapsed time.Duration
	err     error
}

func TestRelayRequestTimeoutUsesBoundedSafeConfiguration(t *testing.T) {
	t.Setenv("RELAY_TIMEOUT", "0")
	assert.Equal(t, defaultRelayRequestTimeout, relayRequestTimeout())
	t.Setenv("RELAY_TIMEOUT", "-1")
	assert.Equal(t, defaultRelayRequestTimeout, relayRequestTimeout())
	t.Setenv("RELAY_TIMEOUT", "17")
	assert.Equal(t, 17*time.Second, relayRequestTimeout())
	t.Setenv("RELAY_TIMEOUT", "999999999")
	assert.Equal(t, maxRelayRequestTimeout, relayRequestTimeout())
}

func TestApplyRelayRequestDeadlineBoundsRequestContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("RELAY_TIMEOUT", "60")
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	started := time.Now()
	cancel := applyRelayRequestDeadline(c)
	defer cancel()
	deadline, ok := c.Request.Context().Deadline()
	require.True(t, ok)
	assert.WithinDuration(t, started.Add(time.Minute), deadline, time.Second)

	cancel()
	assert.ErrorIs(t, c.Request.Context().Err(), context.Canceled)
}

func TestRelayDeadlineCancelsUpstreamAndStillRefundsDurably(t *testing.T) {
	t.Setenv("RELAY_TIMEOUT", "1")
	t.Setenv("RETRY_TIMES", "0")
	fixture := newRelayAccountingFixture(t, 500_000, 500_000)
	billingsvc.SetModelPriceRegistry(map[string]billingsvc.ModelPrice{
		"accounting-model": {Prompt: 1, Completion: 1},
	})

	observed := make(chan relayDeadlineObservation, 1)
	previousClient := relayHTTPClient
	relayHTTPClient = &http.Client{Transport: relayDeadlineRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		started := time.Now()
		select {
		case <-request.Context().Done():
			observed <- relayDeadlineObservation{elapsed: time.Since(started), err: request.Context().Err()}
			return nil, request.Context().Err()
		case <-time.After(5 * time.Second):
			observed <- relayDeadlineObservation{elapsed: time.Since(started)}
			return nil, context.DeadlineExceeded
		}
	})}
	t.Cleanup(func() { relayHTTPClient = previousClient })

	c, recorder, _ := newRelayAccountingContext(t, &fixture.token, 64)
	middleware.SetRelayGroupPolicy(c, billingsvc.RelayGroupPolicy{Groups: []string{"default"}})
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(
		`{"model":"accounting-model","messages":[{"role":"user","content":"hello"}],"max_tokens":64}`,
	))
	c.Request.Header.Set("Content-Type", "application/json")
	Relay(c)

	var observation relayDeadlineObservation
	select {
	case observation = <-observed:
	default:
		require.FailNow(t, "relay did not dispatch upstream", recorder.Body.String())
	}
	require.ErrorIs(t, observation.err, context.DeadlineExceeded)
	assert.Less(t, observation.elapsed, 3*time.Second, "provider I/O must honor the relay deadline")
	assert.GreaterOrEqual(t, recorder.Code, http.StatusInternalServerError, recorder.Body.String())

	// Refund reconciliation deliberately uses its own database context after the
	// provider deadline, so returning the error cannot strand reserved quota.
	var user model.User
	var token model.Token
	var reservation model.RelayQuotaReservationRecord
	require.NoError(t, model.DB.First(&user, fixture.user.Id).Error)
	require.NoError(t, model.DB.First(&token, fixture.token.Id).Error)
	require.NoError(t, model.DB.Order("id DESC").First(&reservation).Error)
	assert.Equal(t, 500_000, user.Quota)
	assert.Zero(t, user.UsedQuota)
	assert.Equal(t, 500_000, token.RemainQuota)
	assert.Zero(t, token.UsedQuota)
	assert.Equal(t, model.RelayQuotaReservationStatusRefunded, reservation.Status)
}

func TestRelayTransportHasConnectionPhaseDeadlines(t *testing.T) {
	transport, ok := relayHTTPClient.Transport.(*http.Transport)
	require.True(t, ok)
	assert.Positive(t, transport.TLSHandshakeTimeout)
	assert.Positive(t, transport.ResponseHeaderTimeout)
	assert.Positive(t, transport.IdleConnTimeout)
	assert.Positive(t, transport.ExpectContinueTimeout)
	assert.Positive(t, transport.MaxIdleConnsPerHost)
}
