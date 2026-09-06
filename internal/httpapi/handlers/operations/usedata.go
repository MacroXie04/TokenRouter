package operations

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/tokenrouter/tokenrouter/internal/auth/roles"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/requestctx"
	"github.com/tokenrouter/tokenrouter/internal/platform/logging"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	dashboardDataQueryTimeout     = 5 * time.Second
	dashboardDataMaxResponseBytes = 2 * 1024 * 1024
	dashboardDataMaxUsernameRunes = 64
	dashboardDataErrorMessage     = "unable to load dashboard data"
)

var (
	errDashboardDataEncoding         = errors.New("dashboard data response encoding failed")
	errDashboardDataResponseTooLarge = errors.New("dashboard data response exceeds safe limits")
)

// parseDashboardDataTimeRange uniformly validates every dashboard endpoint's
// required positive timestamps, ordering, and maximum 30-day span.
func parseDashboardDataTimeRange(c *gin.Context) (int64, int64, bool) {
	startTimestamp, err := strconv.ParseInt(c.Query("start_timestamp"), 10, 64)
	if err != nil || startTimestamp <= 0 {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "invalid start_timestamp"})
		return 0, 0, false
	}
	endTimestamp, err := strconv.ParseInt(c.Query("end_timestamp"), 10, 64)
	if err != nil || endTimestamp <= 0 {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "invalid end_timestamp"})
		return 0, 0, false
	}
	if endTimestamp < startTimestamp {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "invalid time range"})
		return 0, 0, false
	}
	if err := billingsvc.ValidateDashboardDataRange(startTimestamp, endTimestamp); err != nil {
		message := "invalid time range"
		if errors.Is(err, billingsvc.ErrDashboardDataRangeTooLarge) {
			message = "时间跨度不能超过 1 个月"
		}
		c.JSON(http.StatusOK, gin.H{"success": false, "message": message})
		return 0, 0, false
	}
	return startTimestamp, endTimestamp, true
}

// parseDashboardDataUsername validates the optional administrator filter at
// the same 64-code-point boundary as the persisted usage username field. A
// repeated parameter is ambiguous and therefore rejected rather than taking
// the first value silently.
func parseDashboardDataUsername(c *gin.Context) (string, bool) {
	values, present := c.GetQueryArray("username")
	if !present {
		return "", true
	}
	if len(values) != 1 {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "invalid username"})
		return "", false
	}
	username := values[0]
	if !utf8.ValidString(username) || utf8.RuneCountInString(username) > dashboardDataMaxUsernameRunes || strings.TrimSpace(username) != username {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "invalid username"})
		return "", false
	}
	for _, character := range username {
		if character <= 0x1f || (character >= 0x7f && character <= 0x9f) ||
			(character >= 0x202a && character <= 0x202e) || (character >= 0x2066 && character <= 0x2069) {
			c.JSON(http.StatusOK, gin.H{"success": false, "message": "invalid username"})
			return "", false
		}
	}
	return username, true
}

func dashboardDataContext(c *gin.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(c.Request.Context(), dashboardDataQueryTimeout)
}

func dashboardDataFailureKind(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, billingsvc.ErrDashboardDataTooLarge), errors.Is(err, errDashboardDataResponseTooLarge):
		return "result_too_large"
	case errors.Is(err, billingsvc.ErrInvalidDashboardDataRange), errors.Is(err, billingsvc.ErrDashboardDataRangeTooLarge):
		return "invalid_range"
	case errors.Is(err, errDashboardDataEncoding):
		return "encoding"
	default:
		return "query"
	}
}

func writeDashboardDataError(c *gin.Context, err error) {
	logging.LogError("dashboard data request failed",
		"path", c.FullPath(),
		"failure", dashboardDataFailureKind(err),
		"request_id", requestctx.GetRequestId(c),
	)
	c.JSON(http.StatusOK, gin.H{"success": false, "message": dashboardDataErrorMessage})
}

func writeDashboardDataSuccess(c *gin.Context, data any) {
	payload, err := json.Marshal(gin.H{"success": true, "message": "", "data": data})
	if err != nil {
		writeDashboardDataError(c, errDashboardDataEncoding)
		return
	}
	if len(payload) > dashboardDataMaxResponseBytes {
		writeDashboardDataError(c, errDashboardDataResponseTooLarge)
		return
	}
	c.Data(http.StatusOK, "application/json; charset=utf-8", payload)
}

// GetAllQuotaDates returns the admin quota histogram (reference /api/data/).
func GetAllQuotaDates(c *gin.Context) {
	startTimestamp, endTimestamp, ok := parseDashboardDataTimeRange(c)
	if !ok {
		return
	}
	ctx, cancel := dashboardDataContext(c)
	defer cancel()
	username, ok := parseDashboardDataUsername(c)
	if !ok {
		return
	}
	dates, err := billingsvc.GetAllQuotaDatesContext(ctx, startTimestamp, endTimestamp, username)
	if err != nil {
		writeDashboardDataError(c, err)
		return
	}
	writeDashboardDataSuccess(c, dates)
}

// GetQuotaDatesByUser returns the admin per-(username, hour) histogram
// (reference /api/data/users).
func GetQuotaDatesByUser(c *gin.Context) {
	startTimestamp, endTimestamp, ok := parseDashboardDataTimeRange(c)
	if !ok {
		return
	}
	ctx, cancel := dashboardDataContext(c)
	defer cancel()
	dates, err := billingsvc.GetQuotaDataGroupByUserContext(ctx, startTimestamp, endTimestamp)
	if err != nil {
		writeDashboardDataError(c, err)
		return
	}
	writeDashboardDataSuccess(c, dates)
}

// GetUserQuotaDates returns the authenticated user's per-(model, hour)
// histogram with the reference 1-month span limit (reference /api/data/self).
func GetUserQuotaDates(c *gin.Context) {
	userId := requestctx.GetUserId(c)
	startTimestamp, endTimestamp, ok := parseDashboardDataTimeRange(c)
	if !ok {
		return
	}
	ctx, cancel := dashboardDataContext(c)
	defer cancel()
	dates, err := billingsvc.GetQuotaDataByUserIDContext(ctx, userId, startTimestamp, endTimestamp)
	if err != nil {
		writeDashboardDataError(c, err)
		return
	}
	writeDashboardDataSuccess(c, dates)
}

// GetAllFlowQuotaDates returns the role-scoped flow histogram for the admin
// console (reference /api/data/flow).
func GetAllFlowQuotaDates(c *gin.Context) {
	startTimestamp, endTimestamp, ok := parseDashboardDataTimeRange(c)
	if !ok {
		return
	}
	ctx, cancel := dashboardDataContext(c)
	defer cancel()
	username, ok := parseDashboardDataUsername(c)
	if !ok {
		return
	}
	dates, err := billingsvc.GetFlowQuotaDataContext(ctx, startTimestamp, endTimestamp, username, 0, requestctx.GetRole(c))
	if err != nil {
		writeDashboardDataError(c, err)
		return
	}
	writeDashboardDataSuccess(c, dates)
}

// GetUserFlowQuotaDates returns the authenticated user's flow histogram with
// the reference validations (reference /api/data/flow/self).
func GetUserFlowQuotaDates(c *gin.Context) {
	userId := requestctx.GetUserId(c)
	startTimestamp, endTimestamp, ok := parseDashboardDataTimeRange(c)
	if !ok {
		return
	}
	ctx, cancel := dashboardDataContext(c)
	defer cancel()
	dates, err := billingsvc.GetFlowQuotaDataContext(ctx, startTimestamp, endTimestamp, "", userId, roles.RoleCommonUser)
	if err != nil {
		writeDashboardDataError(c, err)
		return
	}
	writeDashboardDataSuccess(c, dates)
}
