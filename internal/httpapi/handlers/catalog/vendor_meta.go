package catalog

import (
	"github.com/gin-gonic/gin"
	catalogsvc "github.com/tokenrouter/tokenrouter/internal/catalog"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"strconv"
)

func GetAllVendors(c *gin.Context) {
	listVendorMetadata(c, "")
}

func SearchVendors(c *gin.Context) {
	listVendorMetadata(c, c.Query("keyword"))
}

func listVendorMetadata(c *gin.Context, keyword string) {
	page := parseModelPage(c)
	items, total, err := model.SearchVendorMetadata(keyword, (page.Page-1)*page.PageSize, page.PageSize)
	if err != nil {
		modelMetadataError(c, err)
		return
	}
	modelMetadataSuccess(c, gin.H{
		"items": items, "total": total, "page": page.Page, "page_size": page.PageSize,
	})
}

func GetVendorMeta(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		modelMetadataErrorMessage(c, "供应商 ID 无效")
		return
	}
	vendor, err := model.GetVendorMetadataByID(id)
	if err != nil {
		modelMetadataError(c, err)
		return
	}
	modelMetadataSuccess(c, vendor)
}

func CreateVendorMeta(c *gin.Context) {
	var vendor model.Vendor
	if err := c.ShouldBindJSON(&vendor); err != nil {
		modelMetadataError(c, err)
		return
	}
	if err := catalogsvc.ValidateVendorMetadata(&vendor); err != nil {
		modelMetadataError(c, err)
		return
	}
	if err := model.CreateVendorMetadata(&vendor); err != nil {
		modelMetadataError(c, err)
		return
	}
	modelMetadataSuccess(c, &vendor)
}

func UpdateVendorMeta(c *gin.Context) {
	var vendor model.Vendor
	if err := c.ShouldBindJSON(&vendor); err != nil {
		modelMetadataError(c, err)
		return
	}
	if vendor.Id <= 0 {
		modelMetadataErrorMessage(c, "缺少供应商 ID")
		return
	}
	if err := catalogsvc.ValidateVendorMetadata(&vendor); err != nil {
		modelMetadataError(c, err)
		return
	}
	if err := model.UpdateVendorMetadata(&vendor); err != nil {
		modelMetadataError(c, err)
		return
	}
	updated, err := model.GetVendorMetadataByID(vendor.Id)
	if err != nil {
		modelMetadataError(c, err)
		return
	}
	modelMetadataSuccess(c, updated)
}

func DeleteVendorMeta(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		modelMetadataErrorMessage(c, "供应商 ID 无效")
		return
	}
	if err := model.DeleteVendorMetadata(id); err != nil {
		modelMetadataError(c, err)
		return
	}
	modelMetadataSuccess(c, nil)
}
