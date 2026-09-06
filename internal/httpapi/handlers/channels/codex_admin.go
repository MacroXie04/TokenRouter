package channels

import (
	"context"
	"fmt"
	"github.com/gin-gonic/gin"
	channelssvc "github.com/tokenrouter/tokenrouter/internal/channels"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/dto"
	"github.com/tokenrouter/tokenrouter/internal/platform/jsonutil"
	"github.com/tokenrouter/tokenrouter/internal/platform/logging"
	"net/http"
	"strconv"
)

type codexChannelOperation func(context.Context, int) (*channelssvc.CodexUpstreamResult, error)

func parseCodexChannelID(c *gin.Context) (int, bool) {
	raw := c.Param("id")
	// Channel ids are positive database integers. Bound the path before
	// parsing so malformed input cannot be reflected into a large response.
	if len(raw) == 0 || len(raw) > 10 {
		c.JSON(http.StatusOK, dto.Fail("invalid channel id"))
		return 0, false
	}
	id, err := strconv.Atoi(raw)
	if err != nil {
		c.JSON(http.StatusOK, dto.Fail(fmt.Sprintf("invalid channel id: %v", err)))
		return 0, false
	}
	return id, true
}

// RefreshCodexChannelCredential refreshes a Codex OAuth credential without
// ever returning the access, refresh, or id token to the dashboard client.
func RefreshCodexChannelCredential(c *gin.Context) {
	channelID, ok := parseCodexChannelID(c)
	if !ok {
		return
	}
	result, err := channelssvc.RefreshCodexChannelCredential(c.Request.Context(), channelID)
	if err != nil {
		logging.LogError("failed to refresh codex channel credential", "error", logging.RedactSensitiveText(err.Error()))
		c.JSON(http.StatusOK, dto.Fail("刷新凭证失败，请稍后重试"))
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "refreshed",
		"data":    result,
	})
}

// GetCodexChannelUsage returns the bounded upstream Wham usage document.
func GetCodexChannelUsage(c *gin.Context) {
	fetchCodexChannelWhamData(c, channelssvc.GetCodexChannelUsage,
		"failed to fetch codex usage", "获取用量信息失败，请稍后重试")
}

// GetCodexChannelRateLimitResetCredits returns the channel's reset-credit
// document from the Codex Wham API.
func GetCodexChannelRateLimitResetCredits(c *gin.Context) {
	fetchCodexChannelWhamData(c, channelssvc.GetCodexChannelRateLimitResetCredits,
		"failed to fetch codex reset credits", "获取重置次数详情失败，请稍后重试")
}

// ResetCodexChannelUsage consumes one rate-limit reset credit.
func ResetCodexChannelUsage(c *gin.Context) {
	fetchCodexChannelWhamData(c, channelssvc.ResetCodexChannelUsage,
		"failed to reset codex usage", "重置用量失败，请稍后重试")
}

func fetchCodexChannelWhamData(
	c *gin.Context,
	operation codexChannelOperation,
	logMessage string,
	userMessage string,
) {
	channelID, ok := parseCodexChannelID(c)
	if !ok {
		return
	}
	result, err := operation(c.Request.Context(), channelID)
	if err != nil {
		if message, exposed := channelssvc.CodexChannelContractMessage(err); exposed {
			c.JSON(http.StatusOK, dto.Fail(message))
			return
		}
		logging.LogError(logMessage, "error", logging.RedactSensitiveText(err.Error()))
		c.JSON(http.StatusOK, dto.Fail(userMessage))
		return
	}
	if result == nil {
		logging.LogError(logMessage, "error", "empty service result")
		c.JSON(http.StatusOK, dto.Fail(userMessage))
		return
	}

	var payload any
	if err := jsonutil.Unmarshal(result.Body, &payload); err != nil {
		payload = string(result.Body)
	}
	success := result.StatusCode >= http.StatusOK && result.StatusCode < http.StatusMultipleChoices
	message := ""
	if !success {
		message = fmt.Sprintf("upstream status: %d", result.StatusCode)
	}
	c.JSON(http.StatusOK, gin.H{
		"success":         success,
		"message":         message,
		"upstream_status": result.StatusCode,
		"data":            payload,
	})
}
