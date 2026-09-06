package controller_test

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	epay "github.com/Calcium-Ion/go-epay/epay"
	"github.com/glebarez/sqlite"
	"github.com/pquerna/otp/totp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/tokenrouter/tokenrouter/common"
	"github.com/tokenrouter/tokenrouter/constant"
	"github.com/tokenrouter/tokenrouter/model"
	"github.com/tokenrouter/tokenrouter/service"
	"github.com/tokenrouter/tokenrouter/setting"
)

const backupCodeCountForRouteTest = 8

// setupAuthMiscRoutes resets the process-wide live settings after installing
// the isolated database used by setupChannelRead. None of these tests run in
// parallel because the application intentionally publishes settings and
// pricing through process-wide immutable snapshots.
func setupAuthMiscRoutes(t *testing.T, role int) (http.Handler, func(method, path, body string) *httptest.ResponseRecorder, int) {
	t.Helper()
	handler, do, userID := setupChannelRead(t, role)
	require.NoError(t, setting.Init())
	service.SetGroupRatios(map[string]float64{service.GroupDefault: 1})
	service.SetGroupGroupRatios(map[string]map[string]float64{})
	service.SetModelPriceRegistry(map[string]service.ModelPrice{})
	require.NoError(t, service.InitAbilityCache())
	return handler, do, userID
}

func authMiscRequest(handler http.Handler, method, path, body string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.RemoteAddr = "198.51.100.119:4321"
	for _, cookie := range cookies {
		request.AddCookie(cookie)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func authMiscStringList(t *testing.T, value any) []string {
	t.Helper()
	raw, ok := value.([]any)
	require.True(t, ok, "expected JSON array, got %#v", value)
	values := make([]string, 0, len(raw))
	for _, item := range raw {
		text, ok := item.(string)
		require.True(t, ok, "expected string item, got %#v", item)
		values = append(values, text)
	}
	return values
}

// closeAfterArmedCommitPool simulates the primary database disappearing at
// the exact commit boundary. Work inside the armed transaction succeeds, but
// every post-commit read through the original pool fails because it is closed.
type closeAfterArmedCommitPool struct {
	db     *sql.DB
	armed  atomic.Bool
	closed atomic.Bool
}

func (p *closeAfterArmedCommitPool) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	return p.db.PrepareContext(ctx, query)
}

func (p *closeAfterArmedCommitPool) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return p.db.ExecContext(ctx, query, args...)
}

func (p *closeAfterArmedCommitPool) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return p.db.QueryContext(ctx, query, args...)
}

func (p *closeAfterArmedCommitPool) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return p.db.QueryRowContext(ctx, query, args...)
}

func (p *closeAfterArmedCommitPool) BeginTx(ctx context.Context, opts *sql.TxOptions) (gorm.ConnPool, error) {
	tx, err := p.db.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &closeAfterArmedCommitTx{Tx: tx, pool: p}, nil
}

type closeAfterArmedCommitTx struct {
	*sql.Tx
	pool *closeAfterArmedCommitPool
}

func (tx *closeAfterArmedCommitTx) Commit() error {
	if err := tx.Tx.Commit(); err != nil {
		return err
	}
	if tx.pool.armed.CompareAndSwap(true, false) {
		tx.pool.closed.Store(true)
		_ = tx.pool.db.Close()
	}
	return nil
}

func installCloseAfterCommitPool(t *testing.T) (*closeAfterArmedCommitPool, string) {
	t.Helper()
	db := model.DB
	raw, err := db.DB()
	require.NoError(t, err)
	var (
		sequence int
		name     string
		path     string
	)
	require.NoError(t, raw.QueryRow("PRAGMA database_list").Scan(&sequence, &name, &path))
	require.NotEmpty(t, path)
	pool := &closeAfterArmedCommitPool{db: raw}
	db.ConnPool = pool
	db.Statement.ConnPool = pool
	return pool, path
}

func TestRatioConfigRouteContract(t *testing.T) {
	_, do, _ := setupAuthMiscRoutes(t, constant.RoleRootUser)

	// The reference endpoint is public but disabled unless the operator opts
	// in. Its disabled response is a deliberate 403, not an auth failure.
	disabled := do(http.MethodGet, "/api/ratio_config", "")
	require.Equal(t, http.StatusForbidden, disabled.Code, disabled.Body.String())
	disabledBody := decodeBody(t, disabled)
	assert.Equal(t, false, disabledBody["success"])
	assert.Equal(t, "倍率配置接口未启用", disabledBody["message"])

	service.SetModelPriceRegistry(map[string]service.ModelPrice{
		"route-ratio-model": {Prompt: 2, Completion: 6},
	})
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.ExposeRatioEnabledOption: "true",
		"CacheRatio":                     `{"route-ratio-model":0.5}`,
		"CreateCacheRatio":               `{"route-ratio-model":1.25}`,
		"PerCallModelPrice":              `{"route-ratio-model":4}`,
	}))

	enabled := do(http.MethodGet, "/api/ratio_config", "")
	require.Equal(t, http.StatusOK, enabled.Code, enabled.Body.String())
	body := decodeBody(t, enabled)
	assert.Equal(t, true, body["success"])
	assert.Equal(t, "", body["message"])
	data, ok := body["data"].(map[string]any)
	require.True(t, ok, enabled.Body.String())
	for _, field := range []string{"model_ratio", "completion_ratio", "cache_ratio", "create_cache_ratio", "model_price"} {
		_, present := data[field]
		assert.True(t, present, "missing reference ratio field %q", field)
	}
	assert.Equal(t, float64(1), data["model_ratio"].(map[string]any)["route-ratio-model"])
	assert.Equal(t, float64(3), data["completion_ratio"].(map[string]any)["route-ratio-model"])
	assert.Equal(t, float64(0.5), data["cache_ratio"].(map[string]any)["route-ratio-model"])
	assert.Equal(t, float64(1.25), data["create_cache_ratio"].(map[string]any)["route-ratio-model"])
	assert.Equal(t, float64(4), data["model_price"].(map[string]any)["route-ratio-model"])

	// Root settings expose stable defaults for all newly supported feature
	// gates even when no row has been persisted yet.
	options := do(http.MethodGet, "/api/option/", "")
	require.Equal(t, http.StatusOK, options.Code, options.Body.String())
	optionData, ok := decodeBody(t, options)["data"].([]any)
	require.True(t, ok, options.Body.String())
	wantDefaults := map[string]string{
		setting.PasskeyEnabledOption:  "false",
		setting.CheckinEnabledOption:  "false",
		setting.CheckinMinQuotaOption: fmt.Sprint(setting.DefaultCheckinMinQuota),
		setting.CheckinMaxQuotaOption: fmt.Sprint(setting.DefaultCheckinMaxQuota),
	}
	for _, raw := range optionData {
		option := raw.(map[string]any)
		key, _ := option["key"].(string)
		if want, wanted := wantDefaults[key]; wanted {
			assert.Equal(t, want, option["value"], key)
			delete(wantDefaults, key)
		}
	}
	assert.Empty(t, wantDefaults, "missing option defaults")
}

func TestTwoFALoginRouteContract(t *testing.T) {
	handler, _, _ := setupAuthMiscRoutes(t, constant.RoleRootUser)
	t.Setenv("ANONYMOUS_REQUEST_BODY_LIMIT_KB", "1")
	t.Setenv("TURNSTILE_SECRET_KEY", "")
	require.NoError(t, model.DB.AutoMigrate(&model.TwoFABackupCode{}))
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.TurnstileCheckEnabledOption: "false",
		setting.TurnstileEnabledOption:      "false",
		setting.TurnstileSecretKeyOption:    "",
	}))

	password := "correct-password"
	hash, err := common.PasswordHash(password)
	require.NoError(t, err)
	user := model.User{
		Username: "route-twofa-user", Password: hash, Role: constant.RoleCommonUser,
		Status: model.UserStatusEnabled, Group: service.GroupDefault, Quota: 1000, AuthVersion: 1,
	}
	require.NoError(t, model.DB.Create(&user).Error)
	secret, _, err := service.GenerateTwoFASecret(user.Id, user.Username)
	require.NoError(t, err)
	code, err := totp.GenerateCode(secret, time.Now())
	require.NoError(t, err)
	_, err = service.EnableTwoFA(user.Id, code)
	require.NoError(t, err)

	login := authMiscRequest(handler, http.MethodPost, "/api/user/login",
		fmt.Sprintf(`{"username":%q,"password":%q}`, user.Username, password))
	require.Equal(t, http.StatusOK, login.Code, login.Body.String())
	assert.Contains(t, login.Header().Get("Cache-Control"), "no-store")
	assert.Empty(t, login.Result().Cookies(), "the password step must not create a session")
	loginData := decodeBody(t, login)["data"].(map[string]any)
	assert.Equal(t, true, loginData["twofa_required"])
	flowToken, ok := loginData["flow_token"].(string)
	require.True(t, ok)
	require.NotEmpty(t, flowToken)

	oversized := authMiscRequest(handler, http.MethodPost, "/api/user/login/2fa", strings.Repeat("x", 1025))
	assert.Equal(t, http.StatusRequestEntityTooLarge, oversized.Code)
	assert.Contains(t, oversized.Header().Get("Cache-Control"), "no-store")

	wrong := authMiscRequest(handler, http.MethodPost, "/api/user/login/2fa",
		fmt.Sprintf(`{"flow_token":%q,"code":"not-a-code"}`, flowToken))
	require.Equal(t, http.StatusUnauthorized, wrong.Code, wrong.Body.String())
	assert.Contains(t, wrong.Header().Get("Cache-Control"), "no-store")
	assert.Empty(t, wrong.Result().Cookies())
	var flow model.AuthFlow
	require.NoError(t, model.DB.Where("token_hash = ?", common.SHA256Hex(flowToken)).First(&flow).Error)
	assert.Nil(t, flow.ConsumedAt, "a bad second factor must not burn a valid password flow")

	code, err = totp.GenerateCode(secret, time.Now())
	require.NoError(t, err)
	complete := authMiscRequest(handler, http.MethodPost, "/api/user/login/2fa",
		fmt.Sprintf(`{"flow_token":%q,"code":%q}`, flowToken, code))
	require.Equal(t, http.StatusOK, complete.Code, complete.Body.String())
	assert.Contains(t, complete.Header().Get("Cache-Control"), "no-store")
	assert.Equal(t, true, decodeBody(t, complete)["success"])
	assert.Len(t, complete.Result().Cookies(), 2)
	require.NoError(t, model.DB.First(&flow, flow.Id).Error)
	assert.NotNil(t, flow.ConsumedAt)

	replay := authMiscRequest(handler, http.MethodPost, "/api/user/login/2fa",
		fmt.Sprintf(`{"flow_token":%q,"code":%q}`, flowToken, code))
	assert.Equal(t, http.StatusUnauthorized, replay.Code)
	assert.Empty(t, replay.Result().Cookies())
}

func TestPasskeyLoginBeginRouteContract(t *testing.T) {
	handler, _, _ := setupAuthMiscRoutes(t, constant.RoleRootUser)
	t.Setenv("ANONYMOUS_REQUEST_BODY_LIMIT_KB", "1")
	require.NoError(t, service.InitWebAuthn())

	disabled := authMiscRequest(handler, http.MethodPost, "/api/user/passkey/login/begin", "")
	require.Equal(t, http.StatusOK, disabled.Code, disabled.Body.String())
	assert.Contains(t, disabled.Header().Get("Cache-Control"), "no-store")
	disabledBody := decodeBody(t, disabled)
	assert.Equal(t, false, disabledBody["success"])
	assert.Equal(t, "管理员未启用 Passkey 登录", disabledBody["message"])

	require.NoError(t, setting.UpdateOption(setting.PasskeyEnabledOption, "true"))
	begin := authMiscRequest(handler, http.MethodPost, "/api/user/passkey/login/begin", "")
	require.Equal(t, http.StatusOK, begin.Code, begin.Body.String())
	assert.Contains(t, begin.Header().Get("Cache-Control"), "no-store")
	data := decodeBody(t, begin)["data"].(map[string]any)
	assert.NotEmpty(t, data["options"])
	flowToken, ok := data["flow_token"].(string)
	require.True(t, ok)
	require.NotEmpty(t, flowToken)
	expiresAt, ok := data["expires_at"].(float64)
	require.True(t, ok)
	assert.Greater(t, int64(expiresAt), time.Now().Unix())
	var flow model.AuthFlow
	require.NoError(t, model.DB.Where("token_hash = ?", common.SHA256Hex(flowToken)).First(&flow).Error)
	assert.Equal(t, service.PasskeyPurposeLogin, flow.Purpose)
	assert.Zero(t, flow.UserId)
	assert.Empty(t, flow.SessionId)

	oversized := authMiscRequest(handler, http.MethodPost, "/api/user/passkey/login/begin", strings.Repeat("x", 1025))
	assert.Equal(t, http.StatusRequestEntityTooLarge, oversized.Code)
	assert.Contains(t, oversized.Header().Get("Cache-Control"), "no-store")
}

func TestPasskeyLoginFinishRouteContract(t *testing.T) {
	handler, _, _ := setupAuthMiscRoutes(t, constant.RoleRootUser)
	t.Setenv("ANONYMOUS_REQUEST_BODY_LIMIT_KB", "1")

	disabled := authMiscRequest(handler, http.MethodPost, "/api/user/passkey/login/finish", `{}`)
	require.Equal(t, http.StatusOK, disabled.Code, disabled.Body.String())
	assert.Contains(t, disabled.Header().Get("Cache-Control"), "no-store")
	assert.Equal(t, "管理员未启用 Passkey 登录", decodeBody(t, disabled)["message"])

	require.NoError(t, setting.UpdateOption(setting.PasskeyEnabledOption, "true"))
	var sessionsBefore int64
	require.NoError(t, model.DB.Model(&model.UserSession{}).Count(&sessionsBefore).Error)
	invalid := authMiscRequest(handler, http.MethodPost, "/api/user/passkey/login/finish",
		`{"flow_token":"present-but-invalid"}`)
	require.Equal(t, http.StatusBadRequest, invalid.Code, invalid.Body.String())
	assert.Contains(t, invalid.Header().Get("Cache-Control"), "no-store")
	assert.Equal(t, false, decodeBody(t, invalid)["success"])
	assert.Empty(t, invalid.Result().Cookies())
	var sessionsAfter int64
	require.NoError(t, model.DB.Model(&model.UserSession{}).Count(&sessionsAfter).Error)
	assert.Equal(t, sessionsBefore, sessionsAfter, "invalid WebAuthn input must not create a login session")

	oversized := authMiscRequest(handler, http.MethodPost, "/api/user/passkey/login/finish", strings.Repeat("x", 1025))
	assert.Equal(t, http.StatusRequestEntityTooLarge, oversized.Code)
	assert.Contains(t, oversized.Header().Get("Cache-Control"), "no-store")
}

func TestEpayNotifyGETRouteContract(t *testing.T) {
	handler, _, userID := setupTopupTest(t)
	require.NoError(t, setting.UpdateOption(setting.PayAddressOption, "https://pay.example.com"))
	require.NoError(t, setting.UpdateOption(setting.EpayIdOption, "get-route-pid"))
	require.NoError(t, setting.UpdateOption(setting.EpayKeyOption, "get-route-secret"))
	callbackPath := func(tradeNo, gatewayTradeNo string) string {
		t.Helper()
		_, err := service.CreateTopUpWithTradeNo(
			userID, 2, 2, "alipay", service.PaymentProviderEpay, tradeNo,
		)
		require.NoError(t, err)

		params := epay.GenerateParams(map[string]string{
			"pid":          "get-route-pid",
			"trade_no":     gatewayTradeNo,
			"out_trade_no": tradeNo,
			"type":         "alipay",
			"name":         "TUC2",
			"money":        "2.00",
			"trade_status": epay.StatusTradeSuccess,
		}, "get-route-secret")
		query := url.Values{}
		for key, value := range params {
			query.Set(key, value)
		}
		return "/api/user/epay/notify?" + query.Encode()
	}

	var before model.User
	require.NoError(t, model.DB.First(&before, userID).Error)
	compliancePath := callbackPath("get-notify-compliance-order", "get-route-compliance-gateway-order")
	setPaymentCompliance(t, false)
	settledWithoutCompliance := authMiscRequest(handler, http.MethodGet, compliancePath, "")
	assert.Equal(t, "success", settledWithoutCompliance.Body.String())
	var afterCompliance model.User
	require.NoError(t, model.DB.First(&afterCompliance, userID).Error)
	assert.Equal(t, before.Quota+2*common.QuotaPerUnit, afterCompliance.Quota,
		"disabling new checkout creation must not strand an already-issued order")

	setPaymentCompliance(t, true)
	catalogPath := callbackPath("get-notify-catalog-order", "get-route-catalog-gateway-order")
	require.NoError(t, setting.UpdateOption(setting.PayMethodsOption, "[]"))
	settledWithoutCatalog := authMiscRequest(handler, http.MethodGet, catalogPath, "")
	assert.Equal(t, "success", settledWithoutCatalog.Body.String())
	var afterCatalog model.User
	require.NoError(t, model.DB.First(&afterCatalog, userID).Error)
	assert.Equal(t, afterCompliance.Quota+2*common.QuotaPerUnit, afterCatalog.Quota,
		"removing a checkout method must not strand an already-issued order")

	require.NoError(t, setting.UpdateOption(setting.PayMethodsOption, ""))
	duplicate := authMiscRequest(handler, http.MethodGet, catalogPath, "")
	assert.Equal(t, "success", duplicate.Body.String())
	var afterDuplicate model.User
	require.NoError(t, model.DB.First(&afterDuplicate, userID).Error)
	assert.Equal(t, afterCatalog.Quota, afterDuplicate.Quota, "duplicate GET callbacks must be idempotent")

	empty := authMiscRequest(handler, http.MethodGet, "/api/user/epay/notify", "")
	assert.Equal(t, "fail", empty.Body.String())
}

func configureAuthMiscGroups(t *testing.T, userID int) {
	t.Helper()
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.UserUsableGroupsOption:   `{"default":"Default","vip":"VIP","auto":"Automatic"}`,
		setting.AutoGroupsOption:         `["vip","default"]`,
		setting.MaxTokenAutoGroupsOption: "5",
	}))
	service.SetGroupRatios(map[string]float64{"default": 1, "vip": 2, "staff": 3, "hidden": 4})
	service.SetGroupGroupRatios(map[string]map[string]float64{
		"staff": {"staff": 0.75, "vip": 0.5},
	})
	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", userID).Update("group", "staff").Error)
}

func TestPublicUserGroupsRouteContract(t *testing.T) {
	handler, _, userID := setupAuthMiscRoutes(t, constant.RoleRootUser)
	configureAuthMiscGroups(t, userID)

	response := authMiscRequest(handler, http.MethodGet, "/api/user/groups", "")
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	body := decodeBody(t, response)
	assert.Equal(t, true, body["success"])
	data := body["data"].(map[string]any)
	require.Len(t, data, 3)
	assert.Equal(t, float64(1), data["default"].(map[string]any)["ratio"])
	assert.Equal(t, "Default", data["default"].(map[string]any)["desc"])
	assert.Equal(t, float64(2), data["vip"].(map[string]any)["ratio"])
	assert.Equal(t, "VIP", data["vip"].(map[string]any)["desc"])
	assert.Equal(t, "自动", data["auto"].(map[string]any)["ratio"])
	assert.Equal(t, "Automatic", data["auto"].(map[string]any)["desc"])
	assert.NotContains(t, data, "staff", "the public route must not infer another user's private group")
	assert.NotContains(t, data, "hidden", "configured ratios alone do not make a group user-selectable")
}

func TestSelfUserGroupsRouteContract(t *testing.T) {
	handler, do, userID := setupAuthMiscRoutes(t, constant.RoleRootUser)
	configureAuthMiscGroups(t, userID)

	response := do(http.MethodGet, "/api/user/self/groups", "")
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	data := decodeBody(t, response)["data"].(map[string]any)
	require.Contains(t, data, "staff")
	assert.Equal(t, float64(0.75), data["staff"].(map[string]any)["ratio"])
	assert.Equal(t, "用户分组", data["staff"].(map[string]any)["desc"])
	assert.Equal(t, float64(0.5), data["vip"].(map[string]any)["ratio"])
	assert.Equal(t, "自动", data["auto"].(map[string]any)["ratio"])
	assert.NotContains(t, data, "hidden")

	unauthenticated := authMiscRequest(handler, http.MethodGet, "/api/user/self/groups", "")
	assert.Equal(t, http.StatusUnauthorized, unauthenticated.Code)
}

func TestUserModelsRouteContract(t *testing.T) {
	handler, do, userID := setupAuthMiscRoutes(t, constant.RoleRootUser)
	configureAuthMiscGroups(t, userID)
	for _, ability := range []model.Ability{
		{Group: "default", Model: "model-default", ChannelId: 1, Enabled: true},
		{Group: "vip", Model: "model-vip", ChannelId: 2, Enabled: true},
		{Group: "staff", Model: "model-staff", ChannelId: 3, Enabled: true},
		{Group: "hidden", Model: "model-hidden", ChannelId: 4, Enabled: true},
		{Group: "vip", Model: "model-disabled", ChannelId: 5, Enabled: false},
	} {
		entry := ability
		require.NoError(t, model.DB.Create(&entry).Error)
	}
	require.NoError(t, service.InitAbilityCache())

	all := do(http.MethodGet, "/api/user/models", "")
	require.Equal(t, http.StatusOK, all.Code, all.Body.String())
	assert.Equal(t, []string{"model-default", "model-staff", "model-vip"},
		authMiscStringList(t, decodeBody(t, all)["data"]))

	vip := do(http.MethodGet, "/api/user/models?group=vip", "")
	assert.Equal(t, []string{"model-vip"}, authMiscStringList(t, decodeBody(t, vip)["data"]))
	own := do(http.MethodGet, "/api/user/models?group=staff", "")
	assert.Equal(t, []string{"model-staff"}, authMiscStringList(t, decodeBody(t, own)["data"]))
	auto := do(http.MethodGet, "/api/user/models?group=auto", "")
	assert.Equal(t, []string{"model-default", "model-vip"}, authMiscStringList(t, decodeBody(t, auto)["data"]))
	hidden := do(http.MethodGet, "/api/user/models?group=hidden", "")
	assert.Empty(t, authMiscStringList(t, decodeBody(t, hidden)["data"]))

	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.ModelBillingModeOption:  `{"model-vip":"reference"}`,
		setting.PerCallModelPriceOption: `{}`,
		setting.ModelRatioOption:        `{}`,
		setting.CompletionRatioOption:   `{}`,
	}))
	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", userID).Update("setting", `{}`).Error)
	priced := do(http.MethodGet, "/api/user/models", "")
	require.Equal(t, http.StatusOK, priced.Code, priced.Body.String())
	assert.Equal(t, []string{"model-default", "model-staff"},
		authMiscStringList(t, decodeBody(t, priced)["data"]))
	require.NoError(t, model.DB.Model(&model.User{}).Where("id = ?", userID).Update(
		"setting", `{"accept_unset_model_ratio_model":true}`,
	).Error)
	accepted := do(http.MethodGet, "/api/user/models", "")
	require.Equal(t, http.StatusOK, accepted.Code, accepted.Body.String())
	assert.Equal(t, []string{"model-default", "model-staff", "model-vip"},
		authMiscStringList(t, decodeBody(t, accepted)["data"]))

	unauthenticated := authMiscRequest(handler, http.MethodGet, "/api/user/models", "")
	assert.Equal(t, http.StatusUnauthorized, unauthenticated.Code)
}

func TestEnableTwoFARouteContract(t *testing.T) {
	handler, do, userID := setupAuthMiscRoutes(t, constant.RoleRootUser)
	require.NoError(t, model.DB.AutoMigrate(&model.TwoFABackupCode{}))
	secret, _, err := service.GenerateTwoFASecret(userID, "route-enable-user")
	require.NoError(t, err)
	code, err := totp.GenerateCode(secret, time.Now())
	require.NoError(t, err)

	pat, err := service.GenerateUserAccessToken(userID)
	require.NoError(t, err)
	patRequest := httptest.NewRequest(http.MethodPost, "/api/user/2fa/enable",
		strings.NewReader(fmt.Sprintf(`{"code":%q}`, code)))
	patRequest.Header.Set("Authorization", "Bearer "+pat)
	patRequest.Header.Set("Content-Type", "application/json")
	patResponse := httptest.NewRecorder()
	handler.ServeHTTP(patResponse, patRequest)
	assert.Equal(t, http.StatusUnauthorized, patResponse.Code)
	assert.Contains(t, patResponse.Header().Get("Cache-Control"), "no-store")
	assert.Equal(t, false, decodeBody(t, patResponse)["success"])
	enabled, err := service.TwoFAStatusChecked(userID)
	require.NoError(t, err)
	assert.False(t, enabled, "a management PAT cannot enroll a login factor")

	unauthenticated := authMiscRequest(handler, http.MethodPost, "/api/user/2fa/enable",
		fmt.Sprintf(`{"code":%q}`, code))
	assert.Equal(t, http.StatusUnauthorized, unauthenticated.Code)

	code, err = totp.GenerateCode(secret, time.Now())
	require.NoError(t, err)
	enable := do(http.MethodPost, "/api/user/2fa/enable", fmt.Sprintf(`{"code":%q}`, code))
	require.Equal(t, http.StatusOK, enable.Code, enable.Body.String())
	assert.Contains(t, enable.Header().Get("Cache-Control"), "no-store")
	body := decodeBody(t, enable)
	assert.Equal(t, true, body["success"])
	assert.Equal(t, "两步验证启用成功", body["message"])
	data := body["data"].(map[string]any)
	assert.Len(t, data["backup_codes"], 8)
	assert.NotEmpty(t, data["access_token"])
	assert.Equal(t, "Bearer", data["token_type"])

	var freshAccess *http.Cookie
	for _, cookie := range enable.Result().Cookies() {
		if cookie.Name == "access_token" {
			freshAccess = cookie
		}
	}
	require.NotNil(t, freshAccess, "auth-version rotation requires a replacement access cookie")
	status := authMiscRequest(handler, http.MethodGet, "/api/user/2fa/status", "", freshAccess)
	require.Equal(t, http.StatusOK, status.Code, status.Body.String())
	assert.Equal(t, true, decodeBody(t, status)["data"].(map[string]any)["enabled"])
	assert.Equal(t, http.StatusUnauthorized, do(http.MethodGet, "/api/user/2fa/status", "").Code,
		"the pre-rotation access token must stop authorizing requests")

	backupCode := data["backup_codes"].([]any)[0].(string)
	disable := authMiscRequest(handler, http.MethodPost, "/api/user/2fa/disable",
		fmt.Sprintf(`{"code":%q}`, backupCode), freshAccess)
	require.Equal(t, http.StatusOK, disable.Code, disable.Body.String())
	disableData := decodeBody(t, disable)["data"].(map[string]any)
	assert.NotEmpty(t, disableData["access_token"])
	assert.Equal(t, "Bearer", disableData["token_type"])
	var disableAccess *http.Cookie
	for _, cookie := range disable.Result().Cookies() {
		if cookie.Name == "access_token" {
			disableAccess = cookie
		}
	}
	require.NotNil(t, disableAccess, "disabling 2FA also rotates auth state")
	status = authMiscRequest(handler, http.MethodGet, "/api/user/2fa/status", "", disableAccess)
	require.Equal(t, http.StatusOK, status.Code, status.Body.String())
	assert.Equal(t, false, decodeBody(t, status)["data"].(map[string]any)["enabled"])
	assert.Equal(t, http.StatusUnauthorized,
		authMiscRequest(handler, http.MethodGet, "/api/user/2fa/status", "", freshAccess).Code,
		"the access token from before factor removal must be revoked")
}

func TestEnableTwoFARouteReturnsRecoveryMaterialWhenDatabaseDropsAfterCommit(t *testing.T) {
	_, do, userID := setupAuthMiscRoutes(t, constant.RoleRootUser)
	require.NoError(t, model.DB.AutoMigrate(&model.TwoFABackupCode{}))
	secret, _, err := service.GenerateTwoFASecret(userID, "post-commit-outage-user")
	require.NoError(t, err)
	code, err := totp.GenerateCode(secret, time.Now())
	require.NoError(t, err)

	pool, databasePath := installCloseAfterCommitPool(t)
	pool.armed.Store(true)
	response := do(http.MethodPost, "/api/user/2fa/enable", fmt.Sprintf(`{"code":%q}`, code))
	require.True(t, pool.closed.Load(), "the simulated outage must start immediately after commit")
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	data := decodeBody(t, response)["data"].(map[string]any)
	require.Len(t, data["backup_codes"], backupCodeCountForRouteTest)
	access, ok := data["access_token"].(string)
	require.True(t, ok)
	require.NotEmpty(t, access)
	claims, err := common.ParseJWTSigned(access, common.SessionSecret())
	require.NoError(t, err)
	assert.Equal(t, userID, claims.UserID)
	assert.Equal(t, int64(2), claims.UserAuthVersion)

	// Reopen the file independently: the response was complete even though the
	// original connection is down, and the factor/codes did commit durably.
	verificationDB, err := gorm.Open(sqlite.Open(databasePath), &gorm.Config{})
	require.NoError(t, err)
	var factor model.TwoFA
	require.NoError(t, verificationDB.Where("user_id = ?", userID).First(&factor).Error)
	assert.True(t, factor.IsEnabled)
	var backupCount int64
	require.NoError(t, verificationDB.Model(&model.TwoFABackupCode{}).
		Where("user_id = ?", userID).Count(&backupCount).Error)
	assert.EqualValues(t, backupCodeCountForRouteTest, backupCount)
}

func TestCheckInPOSTRouteContract(t *testing.T) {
	handler, do, userID := setupAuthMiscRoutes(t, constant.RoleRootUser)
	t.Setenv("TURNSTILE_SECRET_KEY", "")
	require.NoError(t, model.DB.AutoMigrate(&model.Checkin{}))
	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.CheckinEnabledOption:        "false",
		setting.CheckinMinQuotaOption:       "321",
		setting.CheckinMaxQuotaOption:       "321",
		setting.TurnstileCheckEnabledOption: "false",
		setting.TurnstileEnabledOption:      "false",
		setting.TurnstileSecretKeyOption:    "",
	}))

	unauthenticated := authMiscRequest(handler, http.MethodPost, "/api/user/checkin", "")
	assert.Equal(t, http.StatusUnauthorized, unauthenticated.Code)
	disabled := do(http.MethodPost, "/api/user/checkin", "")
	require.Equal(t, http.StatusOK, disabled.Code, disabled.Body.String())
	assert.Equal(t, false, decodeBody(t, disabled)["success"])
	assert.Equal(t, "签到功能未启用", decodeBody(t, disabled)["message"])

	require.NoError(t, setting.UpdateOptions(map[string]string{
		setting.CheckinEnabledOption:        "true",
		setting.TurnstileCheckEnabledOption: "true",
	}))
	turnstileBlocked := do(http.MethodPost, "/api/user/checkin", "")
	require.Equal(t, http.StatusOK, turnstileBlocked.Code, turnstileBlocked.Body.String())
	assert.Equal(t, false, decodeBody(t, turnstileBlocked)["success"])
	assert.Equal(t, "Turnstile token 为空", decodeBody(t, turnstileBlocked)["message"])
	var checkinCount int64
	require.NoError(t, model.DB.Model(&model.Checkin{}).Count(&checkinCount).Error)
	assert.Zero(t, checkinCount, "Turnstile must run before the check-in mutation")

	require.NoError(t, setting.UpdateOption(setting.TurnstileCheckEnabledOption, "false"))
	var before model.User
	require.NoError(t, model.DB.First(&before, userID).Error)
	checkedIn := do(http.MethodPost, "/api/user/checkin", "")
	require.Equal(t, http.StatusOK, checkedIn.Code, checkedIn.Body.String())
	body := decodeBody(t, checkedIn)
	assert.Equal(t, true, body["success"])
	assert.Equal(t, "签到成功", body["message"])
	data := body["data"].(map[string]any)
	assert.Len(t, data, 2, "the success payload matches the reference check-in contract")
	assert.Equal(t, float64(321), data["quota_awarded"])
	assert.Equal(t, time.Now().Format("2006-01-02"), data["checkin_date"])
	var after model.User
	require.NoError(t, model.DB.First(&after, userID).Error)
	assert.Equal(t, before.Quota+321, after.Quota)
	require.NoError(t, model.DB.Model(&model.Checkin{}).Count(&checkinCount).Error)
	assert.EqualValues(t, 1, checkinCount)

	duplicate := do(http.MethodPost, "/api/user/checkin", "")
	require.Equal(t, http.StatusOK, duplicate.Code, duplicate.Body.String())
	assert.Equal(t, false, decodeBody(t, duplicate)["success"])
	assert.Equal(t, "今日已签到", decodeBody(t, duplicate)["message"])
	require.NoError(t, model.DB.First(&after, userID).Error)
	assert.Equal(t, before.Quota+321, after.Quota)
}

func TestGetAllModelsMetaRouteContract(t *testing.T) {
	handler, do, _ := setupAuthMiscRoutes(t, constant.RoleRootUser)
	metadata := model.Model{
		ModelName: "route-model-metadata", Description: "route evidence",
		Status: 1, SyncOfficial: 1, NameRule: model.ModelNameRuleExact,
	}
	require.NoError(t, model.CreateModelMetadata(&metadata))

	root := do(http.MethodGet, "/api/models/?p=1&page_size=5", "")
	require.Equal(t, http.StatusOK, root.Code, root.Body.String())
	body := decodeBody(t, root)
	assert.Equal(t, true, body["success"])
	assert.Equal(t, "", body["message"])
	data := body["data"].(map[string]any)
	assert.Equal(t, float64(1), data["total"])
	assert.Equal(t, float64(1), data["page"])
	assert.Equal(t, float64(5), data["page_size"])
	items := data["items"].([]any)
	require.Len(t, items, 1)
	assert.Equal(t, "route-model-metadata", items[0].(map[string]any)["model_name"])
	vendorCounts, ok := data["vendor_counts"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, float64(1), vendorCounts["0"])

	unauthenticated := authMiscRequest(handler, http.MethodGet, "/api/models/", "")
	assert.Equal(t, http.StatusUnauthorized, unauthenticated.Code)

	commonUser := model.User{
		Username: "models-common-user", Password: "unused", Role: constant.RoleCommonUser,
		Status: model.UserStatusEnabled, Group: service.GroupDefault, AuthVersion: 1,
	}
	require.NoError(t, model.DB.Create(&commonUser).Error)
	_, commonAccess, _, err := service.CompleteLogin(&commonUser, "127.0.0.1", "ua", "test")
	require.NoError(t, err)
	forbidden := authMiscRequest(handler, http.MethodGet, "/api/models/", "",
		&http.Cookie{Name: "access_token", Value: commonAccess})
	assert.Equal(t, http.StatusForbidden, forbidden.Code)
}
