package controller

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/relay/channel/ollama"
	"github.com/tokenrouter/tokenrouter/service"
)

type ollamaModelOperationRequest struct {
	ChannelID int    `json:"channel_id"`
	ModelName string `json:"model_name"`
}

// OllamaPullModel pulls one model through the selected Ollama channel.
func OllamaPullModel(c *gin.Context) {
	request, channel, ok := bindOllamaModelOperation(c)
	if !ok {
		return
	}
	if err := ollama.PullModel(c.Request.Context(), ollamaBaseURL(channel), service.GetChannelKey(channel), request.ModelName); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"success": false, "message": "Failed to pull model: " + safeOllamaOperationError(err)})
		return
	}
	channelAudit(c, "channel.ollama_pull", map[string]any{"id": channel.Id, "model": strings.TrimSpace(request.ModelName)})
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": fmt.Sprintf("Model %s pulled successfully", strings.TrimSpace(request.ModelName)),
	})
}

// OllamaPullModelStream streams bounded native pull progress as SSE.
func OllamaPullModelStream(c *gin.Context) {
	request, channel, ok := bindOllamaModelOperation(c)
	if !ok {
		return
	}
	started := false
	writeEvent := func(payload any) error {
		body, err := common.Marshal(payload)
		if err != nil {
			return err
		}
		if !started {
			c.Header("Content-Type", "text/event-stream")
			c.Header("Cache-Control", "no-cache")
			c.Header("Connection", "keep-alive")
			c.Status(http.StatusOK)
			started = true
		}
		if _, err := c.Writer.WriteString("data: " + string(body) + "\n\n"); err != nil {
			return err
		}
		c.Writer.Flush()
		return nil
	}

	err := ollama.PullModelStream(
		c.Request.Context(), ollamaBaseURL(channel), service.GetChannelKey(channel), request.ModelName,
		func(progress ollama.PullProgress) error { return writeEvent(progress) },
	)
	if err != nil {
		if !started {
			c.JSON(http.StatusBadGateway, gin.H{"success": false, "message": "Failed to pull model: " + safeOllamaOperationError(err)})
			return
		}
		if c.Request.Context().Err() == nil {
			_ = writeEvent(gin.H{"error": safeOllamaOperationError(err)})
			_, _ = c.Writer.WriteString("data: [DONE]\n\n")
			c.Writer.Flush()
		}
		return
	}
	channelAudit(c, "channel.ollama_pull_stream", map[string]any{"id": channel.Id, "model": strings.TrimSpace(request.ModelName)})
	_ = writeEvent(gin.H{"message": fmt.Sprintf("Model %s pulled successfully", strings.TrimSpace(request.ModelName))})
	_, _ = c.Writer.WriteString("data: [DONE]\n\n")
	c.Writer.Flush()
}

// OllamaDeleteModel deletes one model through the selected Ollama channel.
func OllamaDeleteModel(c *gin.Context) {
	request, channel, ok := bindOllamaModelOperation(c)
	if !ok {
		return
	}
	if err := ollama.DeleteModel(c.Request.Context(), ollamaBaseURL(channel), service.GetChannelKey(channel), request.ModelName); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"success": false, "message": "Failed to delete model: " + safeOllamaOperationError(err)})
		return
	}
	channelAudit(c, "channel.ollama_delete", map[string]any{"id": channel.Id, "model": strings.TrimSpace(request.ModelName)})
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": fmt.Sprintf("Model %s deleted successfully", strings.TrimSpace(request.ModelName)),
	})
}

// OllamaVersion returns the selected channel's native server version.
func OllamaVersion(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Invalid channel id"})
		return
	}
	channel, err := service.GetChannelByID(id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "Channel not found"})
		return
	}
	if channel.Type != int(constant.ChannelTypeOllama) {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "This operation is only supported for Ollama channels"})
		return
	}
	version, err := ollama.FetchVersion(c.Request.Context(), ollamaBaseURL(channel), service.GetChannelKey(channel))
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "获取Ollama版本失败: " + safeOllamaOperationError(err)})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"version": version}})
}

func bindOllamaModelOperation(c *gin.Context) (ollamaModelOperationRequest, *model.Channel, bool) {
	var request ollamaModelOperationRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Invalid request parameters"})
		return request, nil, false
	}
	if request.ChannelID <= 0 || strings.TrimSpace(request.ModelName) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Channel ID and model name are required"})
		return request, nil, false
	}
	channel, err := service.GetChannelByID(request.ChannelID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "Channel not found"})
		return request, nil, false
	}
	if channel.Type != int(constant.ChannelTypeOllama) {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "This operation is only supported for Ollama channels"})
		return request, nil, false
	}
	return request, channel, true
}

func ollamaBaseURL(channel *model.Channel) string {
	if channel != nil && strings.TrimSpace(channel.BaseURL) != "" {
		return strings.TrimSpace(channel.BaseURL)
	}
	return constant.ChannelBaseURLs[int(constant.ChannelTypeOllama)]
}

func safeOllamaOperationError(err error) string {
	if err == nil {
		return "unknown error"
	}
	if errors.Is(err, context.Canceled) {
		return "request canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "upstream request timed out"
	}
	return common.RedactSensitiveText(err.Error())
}
