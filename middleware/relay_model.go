package middleware

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/relay/channel/jimeng"
	"github.com/tokenrouter/tokenrouter/service"
)

const relayModelPolicyContext = "relay_model_policy"

// GetRelayModelPolicy returns the immutable policy installed during relay
// authentication. The fallback derives it from the token and therefore still
// fails closed if a caller omitted policy setup.
func GetRelayModelPolicy(c *gin.Context) service.TokenModelPolicy {
	if value, ok := c.Get(relayModelPolicyContext); ok {
		if policy, ok := value.(service.TokenModelPolicy); ok {
			return policy
		}
	}
	return service.NewTokenModelPolicy(GetRelayToken(c))
}

// RelayModelAllowed applies the authenticated token's model policy to an
// exact client-facing model identifier.
func RelayModelAllowed(c *gin.Context, modelName string) bool {
	return GetRelayModelPolicy(c).Allows(modelName)
}

// FilterRelayModels returns the token-visible subset of a group catalog.
func FilterRelayModels(c *gin.Context, models map[string]bool) map[string]bool {
	return GetRelayModelPolicy(c).Filter(models)
}

// RequireRelayQueryModel enforces a model carried in a query parameter before
// a handler can select or contact an upstream (notably Realtime WebSocket).
func RequireRelayQueryModel(parameter string) gin.HandlerFunc {
	return func(c *gin.Context) {
		modelName := c.Query(parameter)
		if modelName != "" && !RelayModelAllowed(c, modelName) {
			abortRelayModelAccess(c, modelName)
			return
		}
		c.Next()
	}
}

// RequireJimengModel enforces the req_key on submits and the persisted origin
// model on fetches. It runs after JimengRequestConvert and before the relay
// handler, leaving the Jimeng implementation itself independent of auth state.
func RequireJimengModel() gin.HandlerFunc {
	return func(c *gin.Context) {
		policy := GetRelayModelPolicy(c)
		if !policy.Limited() {
			c.Next()
			return
		}

		requestContext, ok := GetJimengRequest(c)
		if !ok {
			abortJimengMiddleware(c, http.StatusInternalServerError, "Jimeng request context is missing")
			return
		}

		modelName := strings.TrimSpace(requestContext.Request.ReqKey)
		if requestContext.Action == jimeng.FetchAction {
			persistedModel, found, err := jimengTaskOriginModel(common.GetUserId(c), requestContext.Request.TaskID)
			if err != nil {
				abortJimengMiddleware(c, http.StatusInternalServerError, "Unable to verify task model access")
				return
			}
			if !found {
				// The handler owns the protocol-specific task-not-found response;
				// no upstream work can occur when the task does not exist.
				c.Next()
				return
			}
			modelName = persistedModel
		}

		if modelName == "" {
			// Submit validation owns the missing-req_key response. Fetches with
			// an existing task never reach this branch because persisted model
			// metadata is required above.
			c.Next()
			return
		}
		if policy.Allows(modelName) {
			c.Next()
			return
		}
		abortJimengMiddleware(c, http.StatusForbidden, "Token is not allowed to access model "+modelName)
	}
}

func jimengTaskOriginModel(userID int, taskID string) (string, bool, error) {
	if model.DB == nil {
		return "", false, errors.New("database is not initialized")
	}
	var task model.Task
	err := model.DB.Select("properties").Where("task_id = ? AND user_id = ?", taskID, userID).First(&task).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	var properties struct {
		OriginModelName string `json:"origin_model_name"`
	}
	if err := common.UnmarshalJsonStr(task.Properties, &properties); err != nil {
		return "", true, err
	}
	modelName := strings.TrimSpace(properties.OriginModelName)
	if modelName == "" {
		return "", true, errors.New("task origin model is missing")
	}
	return modelName, true, nil
}

func abortRelayModelAccess(c *gin.Context, modelName string) {
	c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": &protocolError{
		Message: "令牌无权访问模型 " + modelName,
		Type:    "invalid_request_error",
		Code:    "model_not_allowed",
	}})
}
