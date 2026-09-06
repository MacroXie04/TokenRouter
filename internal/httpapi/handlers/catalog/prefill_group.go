package catalog

import (
	"github.com/gin-gonic/gin"
	catalogsvc "github.com/tokenrouter/tokenrouter/internal/catalog"
	model "github.com/tokenrouter/tokenrouter/internal/store"
	"net/http"
	"strconv"
)

func GetGroups(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data":    catalogsvc.GetConfiguredGroups(),
	})
}

func GetPrefillGroups(c *gin.Context) {
	groups, err := catalogsvc.ListPrefillGroups(c.Query("type"))
	if err != nil {
		prefillGroupError(c, err)
		return
	}
	prefillGroupSuccess(c, groups)
}

func CreatePrefillGroup(c *gin.Context) {
	var group model.PrefillGroup
	if err := c.ShouldBindJSON(&group); err != nil {
		prefillGroupError(c, err)
		return
	}
	if err := catalogsvc.CreatePrefillGroup(&group); err != nil {
		prefillGroupError(c, err)
		return
	}
	prefillGroupSuccess(c, &group)
}

func UpdatePrefillGroup(c *gin.Context) {
	var group model.PrefillGroup
	if err := c.ShouldBindJSON(&group); err != nil {
		prefillGroupError(c, err)
		return
	}
	if err := catalogsvc.UpdatePrefillGroup(&group); err != nil {
		prefillGroupError(c, err)
		return
	}
	prefillGroupSuccess(c, &group)
}

func DeletePrefillGroup(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		prefillGroupError(c, err)
		return
	}
	if err := catalogsvc.DeletePrefillGroup(id); err != nil {
		prefillGroupError(c, err)
		return
	}
	prefillGroupSuccess(c, nil)
}

func prefillGroupSuccess(c *gin.Context, data any) {
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "", "data": data})
}

func prefillGroupError(c *gin.Context, err error) {
	c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
}
