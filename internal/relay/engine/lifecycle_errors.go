package engine

import (
	"errors"
	"fmt"
	"github.com/gin-gonic/gin"
	channelssvc "github.com/tokenrouter/tokenrouter/internal/channels"
	channelcatalog "github.com/tokenrouter/tokenrouter/internal/channels/catalog"
	relaycommon "github.com/tokenrouter/tokenrouter/internal/relay/contract"
	"github.com/tokenrouter/tokenrouter/protocolkit"
)

func relayChannelFailure(err error) channelssvc.ChannelFailure {
	failure := channelssvc.ChannelFailure{ErrorPresent: err != nil}
	if err == nil {
		return failure
	}
	var upstream *relaycommon.UpstreamError
	if errors.As(err, &upstream) && upstream != nil {
		failure.StatusCode = upstream.StatusCode
		failure.SkipRetry = upstream.SkipRetry
		failure.BadResponseBody = errors.Is(upstream.Cause, relaycommon.ErrUpstreamResponseTooLarge)
		failure.Message = upstream.Body
		return failure
	}
	failure.ChannelError = errors.Is(err, relaycommon.ErrUpstreamTransportFailed)
	return failure
}

func abortRelay(c *gin.Context, status int, message, code string) {
	c.JSON(status, gin.H{"error": protocolkit.OpenAIError{Message: message, Type: "invalid_request_error", Code: code}})
}

func writeRelayAccountingFailure(c *gin.Context, info *RelayInfo, status int, message, code string) {
	if c.Writer.Written() {
		return
	}
	if info != nil && info.Format == channelcatalog.RelayFormatClaude {
		abortClaude(c, status, "api_error", message)
		return
	}
	abortRelay(c, status, message, code)
}

func wrapRelayError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", operation, err)
}
