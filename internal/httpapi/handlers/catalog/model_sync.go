package catalog

import (
	"errors"
	"github.com/gin-gonic/gin"
	catalogsvc "github.com/tokenrouter/tokenrouter/internal/catalog"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"
)

func SyncUpstreamPreview(c *gin.Context) {
	locale := c.Query("locale")
	if utf8.RuneCountInString(locale) > 16 {
		modelMetadataErrorMessage(c, "locale 参数过长")
		return
	}
	preview, err := catalogsvc.PreviewModelMetadataSync(c.Request.Context(), locale)
	if err != nil {
		writeModelSyncError(c, locale, err)
		return
	}
	modelMetadataSuccess(c, preview)
}

func SyncUpstreamModels(c *gin.Context) {
	var request catalogsvc.ModelSyncRequest
	if err := c.ShouldBindJSON(&request); err != nil && !errors.Is(err, io.EOF) {
		modelMetadataError(c, err)
		return
	}
	if utf8.RuneCountInString(request.Locale) > 16 {
		modelMetadataErrorMessage(c, "locale 参数过长")
		return
	}
	if len(request.Overwrite) > 1000 {
		modelMetadataErrorMessage(c, "overwrite 项不能超过 1000 个")
		return
	}
	result, err := catalogsvc.ApplyModelMetadataSync(c.Request.Context(), request)
	if err != nil {
		writeModelSyncError(c, request.Locale, err)
		return
	}
	modelMetadataSuccess(c, result)
}

func writeModelSyncError(c *gin.Context, locale string, err error) {
	var upstreamErr *catalogsvc.ModelSyncUpstreamError
	if errors.As(err, &upstreamErr) {
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": "获取上游模型失败: " + upstreamErr.Err.Error(),
			"locale":  locale,
			"source_urls": gin.H{
				"models_url":  upstreamErr.Source.ModelsURL,
				"vendors_url": upstreamErr.Source.VendorsURL,
			},
		})
		return
	}
	if errors.Is(err, io.EOF) {
		modelMetadataError(c, err)
		return
	}
	if strings.HasPrefix(err.Error(), "获取模型列表失败，请稍后重试") {
		modelMetadataErrorMessage(c, "获取模型列表失败，请稍后重试")
		return
	}
	modelMetadataError(c, err)
}
