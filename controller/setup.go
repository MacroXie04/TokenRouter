package controller

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/dto"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/service"
	"github.com/tokenrouter/tokenrouter/setting"
)

var errSetupAlreadyCompleted = errors.New("setup already completed")

var errSetupAdministratorRequired = errors.New("setup administrator required")

type setupState struct {
	Completed       bool
	RootInitialized bool
}

type setupRequest struct {
	Username           string `json:"username"`
	Password           string `json:"password"`
	ConfirmPassword    string `json:"confirmPassword"`
	SelfUseModeEnabled bool   `json:"SelfUseModeEnabled"`
	DemoSiteEnabled    bool   `json:"DemoSiteEnabled"`
}

func loadSetupState(db *gorm.DB) (setupState, error) {
	var setupCount int64
	if err := db.Model(&model.Setup{}).Count(&setupCount).Error; err != nil {
		return setupState{}, err
	}
	var rootCount int64
	if err := db.Model(&model.User{}).Where("role >= ?", constant.RoleRootUser).Count(&rootCount).Error; err != nil {
		return setupState{}, err
	}
	return setupState{Completed: setupCount > 0, RootInitialized: rootCount > 0}, nil
}

func setupRequired() (bool, error) {
	state, err := loadSetupState(model.DB)
	return !state.Completed, err
}

func setupDatabaseType() string {
	if model.DB == nil || model.DB.Dialector == nil {
		return "unknown"
	}
	databaseType := strings.ToLower(strings.TrimSpace(model.DB.Dialector.Name()))
	if databaseType == "postgresql" {
		return "postgres"
	}
	if databaseType == "" || len(databaseType) > 32 {
		return "unknown"
	}
	return databaseType
}

// GetSetup reports whether an initial root account must be created.
func GetSetup(c *gin.Context) {
	state, err := loadSetupState(model.DB)
	if err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail("读取初始化状态失败"))
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			// Keep TokenRouter's established setup_required field while exposing
			// the reference setup status contract used by the complete wizard.
			"setup_required":     !state.Completed,
			"status":             state.Completed,
			"root_init":          state.RootInitialized,
			"database_type":      setupDatabaseType(),
			"SelfUseModeEnabled": setting.GetOptionBool(setting.SelfUseModeEnabledOption, false),
			"DemoSiteEnabled":    setting.GetOptionBool(setting.DemoSiteEnabledOption, false),
			"site_name":          setting.GetSiteName(),
			"version":            common.Version,
		},
	})
}

// PostSetup creates the initial root account via the setup wizard. TokenRouter
// does not ship a default password; the operator chooses one here.
func PostSetup(c *gin.Context) {
	var req setupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, dto.Fail("请求参数有误"))
		return
	}
	if req.SelfUseModeEnabled && req.DemoSiteEnabled {
		c.JSON(http.StatusBadRequest, dto.Fail("使用模式无效"))
		return
	}
	state, err := loadSetupState(model.DB)
	if err != nil {
		c.JSON(http.StatusInternalServerError, dto.Fail("读取初始化状态失败"))
		return
	}
	if state.Completed {
		c.JSON(http.StatusForbidden, dto.Fail("系统已初始化，禁止重复设置"))
		return
	}

	var user *model.User
	var registrationPlan *service.RegistrationMutationPlan
	if !state.RootInitialized {
		username := strings.TrimSpace(req.Username)
		if len(username) < 3 || len(username) > 12 {
			c.JSON(http.StatusBadRequest, dto.Fail("管理员用户名长度必须为3到12个字符"))
			return
		}
		if len(req.Password) < 8 || len(req.Password) > 64 {
			c.JSON(http.StatusBadRequest, dto.Fail("密码长度必须为8到64个字符"))
			return
		}
		if req.Password != req.ConfirmPassword {
			c.JSON(http.StatusBadRequest, dto.Fail("两次输入的密码不一致"))
			return
		}
		hash, hashErr := common.PasswordHash(req.Password)
		if hashErr != nil {
			c.JSON(http.StatusInternalServerError, dto.Fail("密码加密失败"))
			return
		}
		plan, planErr := service.PlanRegistrationMutationFromEnvironment(username)
		if planErr != nil {
			c.JSON(http.StatusInternalServerError, dto.Fail("初始化注册配置无效"))
			return
		}
		registrationPlan = &plan
		user = &model.User{
			Username:    username,
			Password:    hash,
			DisplayName: username,
			Role:        constant.RoleRootUser,
			Status:      model.UserStatusEnabled,
			CreatedAt:   common.NowTimestamp(),
			AuthVersion: 1,
		}
	}
	if err := createInitialSetup(user, registrationPlan, req.SelfUseModeEnabled, req.DemoSiteEnabled); err != nil {
		if errors.Is(err, errSetupAlreadyCompleted) {
			c.JSON(http.StatusForbidden, dto.Fail("系统已初始化，禁止重复设置"))
			return
		}
		if errors.Is(err, errSetupAdministratorRequired) {
			c.JSON(http.StatusConflict, dto.Fail("管理员账号尚未初始化"))
			return
		}
		c.JSON(http.StatusInternalServerError, dto.Fail("系统初始化失败"))
		return
	}
	c.JSON(http.StatusOK, dto.OkMessage("初始化成功"))
}

// createInitialSetup serializes setup through a singleton row. The claim,
// optional root insert, and operation-mode options share one transaction, so
// no supported database can expose a half-initialized installation.
func createInitialSetup(user *model.User, registrationPlan *service.RegistrationMutationPlan, selfUseMode, demoSite bool) error {
	err := model.DB.Transaction(func(tx *gorm.DB) error {
		claim := model.Setup{ID: 1, Version: common.Version, InitializedAt: common.NowTimestamp()}
		result := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "id"}},
			DoNothing: true,
		}).Create(&claim)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errSetupAlreadyCompleted
		}

		var rootCount int64
		if err := tx.Model(&model.User{}).Where("role >= ?", constant.RoleRootUser).Count(&rootCount).Error; err != nil {
			return err
		}
		if rootCount == 0 {
			if user == nil || registrationPlan == nil {
				return errSetupAdministratorRequired
			}
			if err := service.InsertPlannedRegistrationUserWithTx(tx, user, *registrationPlan); err != nil {
				return err
			}
		}

		options := []model.Option{
			{Key: setting.SelfUseModeEnabledOption, Value: strconv.FormatBool(selfUseMode)},
			{Key: setting.DemoSiteEnabledOption, Value: strconv.FormatBool(demoSite)},
		}
		for index := range options {
			if err := tx.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "key"}},
				DoUpdates: clause.AssignmentColumns([]string{"value"}),
			}).Create(&options[index]).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	// The transaction above is authoritative. A cache refresh can fail because
	// of an unrelated option that was corrupted out of band; reporting setup as
	// failed after the root and singleton marker committed would make a retry
	// impossible and mislead the operator. Keep the durable success result and
	// surface the refresh failure through the operator log for repair/retry.
	if err := setting.Sync(); err != nil {
		common.SysError("setup committed but settings cache refresh failed: " + err.Error())
	}
	return nil
}
