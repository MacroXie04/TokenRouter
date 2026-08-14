package controller

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/service"
)

type modelPage struct {
	Page     int
	PageSize int
}

func GetAllModelsMeta(c *gin.Context) {
	listModelMetadata(c, "")
}

func SearchModelsMeta(c *gin.Context) {
	listModelMetadata(c, c.Query("keyword"))
}

func listModelMetadata(c *gin.Context, keyword string) {
	page := parseModelPage(c)
	items, total, err := model.SearchModelMetadata(model.ModelSearchFilter{
		Keyword:      keyword,
		Vendor:       c.Query("vendor"),
		Status:       service.ParseModelStatusFilter(c.Query("status")),
		SyncOfficial: service.ParseModelSyncFilter(c.Query("sync_official")),
		Offset:       (page.Page - 1) * page.PageSize,
		Limit:        page.PageSize,
	})
	if err != nil {
		modelMetadataError(c, err)
		return
	}
	if err := service.EnrichModelMetadata(items); err != nil {
		modelMetadataError(c, err)
		return
	}
	vendorCounts, err := model.GetVendorModelCounts()
	if err != nil {
		modelMetadataError(c, err)
		return
	}
	modelMetadataSuccess(c, gin.H{
		"items":         items,
		"total":         total,
		"page":          page.Page,
		"page_size":     page.PageSize,
		"vendor_counts": vendorCounts,
	})
}

func GetModelMeta(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		modelMetadataError(c, err)
		return
	}
	metadata, err := model.GetModelMetadataByID(id)
	if err != nil {
		modelMetadataError(c, err)
		return
	}
	if err := service.EnrichModelMetadata([]*model.Model{metadata}); err != nil {
		modelMetadataError(c, err)
		return
	}
	modelMetadataSuccess(c, metadata)
}

func CreateModelMeta(c *gin.Context) {
	var metadata model.Model
	if err := c.ShouldBindJSON(&metadata); err != nil {
		modelMetadataError(c, err)
		return
	}
	if err := service.ValidateModelMetadata(&metadata, false); err != nil {
		modelMetadataError(c, err)
		return
	}
	if err := model.CreateModelMetadata(&metadata); err != nil {
		modelMetadataError(c, err)
		return
	}
	respondWithModelMetadata(c, metadata.Id)
}

func UpdateModelMeta(c *gin.Context) {
	statusOnly := c.Query("status_only") == "true"
	var metadata model.Model
	if err := c.ShouldBindJSON(&metadata); err != nil {
		modelMetadataError(c, err)
		return
	}
	if metadata.Id == 0 {
		modelMetadataErrorMessage(c, "缺少模型 ID")
		return
	}
	if err := service.ValidateModelMetadata(&metadata, statusOnly); err != nil {
		modelMetadataError(c, err)
		return
	}
	if err := model.UpdateModelMetadata(&metadata, statusOnly); err != nil {
		modelMetadataError(c, err)
		return
	}
	respondWithModelMetadata(c, metadata.Id)
}

func respondWithModelMetadata(c *gin.Context, id int) {
	metadata, err := model.GetModelMetadataByID(id)
	if err != nil {
		modelMetadataError(c, err)
		return
	}
	if err := service.EnrichModelMetadata([]*model.Model{metadata}); err != nil {
		modelMetadataError(c, err)
		return
	}
	modelMetadataSuccess(c, metadata)
}

func DeleteModelMeta(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		modelMetadataError(c, err)
		return
	}
	if err := model.DeleteModelMetadata(id); err != nil {
		modelMetadataError(c, err)
		return
	}
	modelMetadataSuccess(c, nil)
}

func GetMissingModels(c *gin.Context) {
	missing, err := model.GetMissingModelNamesContext(c.Request.Context())
	if err != nil {
		modelMetadataError(c, err)
		return
	}
	modelMetadataSuccess(c, missing)
}

func parseModelPage(c *gin.Context) modelPage {
	page, _ := strconv.Atoi(c.DefaultQuery("p", "1"))
	if page < 1 {
		page = 1
	}
	rawSize := c.Query("page_size")
	if rawSize == "" {
		rawSize = c.Query("ps")
	}
	if rawSize == "" {
		rawSize = c.Query("size")
	}
	if rawSize == "" {
		rawSize = "10"
	}
	pageSize, _ := strconv.Atoi(rawSize)
	if pageSize < 1 {
		pageSize = 10
	}
	if pageSize > 100 {
		pageSize = 100
	}
	return modelPage{Page: page, PageSize: pageSize}
}

func modelMetadataSuccess(c *gin.Context, data any) {
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": data})
}

func modelMetadataError(c *gin.Context, err error) {
	modelMetadataErrorMessage(c, err.Error())
}

func modelMetadataErrorMessage(c *gin.Context, message string) {
	c.JSON(http.StatusOK, gin.H{"success": false, "message": message})
}
