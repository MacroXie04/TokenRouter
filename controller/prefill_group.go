package controller

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/service"
)

func GetGroups(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data":    service.GetConfiguredGroups(),
	})
}

func GetPrefillGroups(c *gin.Context) {
	groups, err := service.ListPrefillGroups(c.Query("type"))
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
	if err := service.CreatePrefillGroup(&group); err != nil {
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
	if err := service.UpdatePrefillGroup(&group); err != nil {
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
	if err := service.DeletePrefillGroup(id); err != nil {
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
