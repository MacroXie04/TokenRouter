package settings

import (
	"bytes"
	"encoding/json"
	"errors"
	"github.com/gin-gonic/gin"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	"github.com/tokenrouter/tokenrouter/internal/httpapi/requestctx"
	"github.com/tokenrouter/tokenrouter/internal/platform/httpx"
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	"io"
	"net/http"
	"strconv"
)

const maxSMTPSettingsRequestBytes = 16 << 10

type smtpSettingsUpdateRequest struct {
	SMTPServer             *string `json:"SMTPServer"`
	SMTPPort               *string `json:"SMTPPort"`
	SMTPAccount            *string `json:"SMTPAccount"`
	SMTPFrom               *string `json:"SMTPFrom"`
	SMTPToken              *string `json:"SMTPToken"`
	SMTPSSLEnabled         *bool   `json:"SMTPSSLEnabled"`
	SMTPStartTLSEnabled    *bool   `json:"SMTPStartTLSEnabled"`
	SMTPInsecureSkipVerify *bool   `json:"SMTPInsecureSkipVerify"`
	SMTPForceAuthLogin     *bool   `json:"SMTPForceAuthLogin"`
	ClearSMTPToken         *bool   `json:"clear_smtp_token"`
}

var smtpSettingsRequestFields = map[string]struct{}{
	"SMTPServer": {}, "SMTPPort": {}, "SMTPAccount": {}, "SMTPFrom": {}, "SMTPToken": {},
	"SMTPSSLEnabled": {}, "SMTPStartTLSEnabled": {}, "SMTPInsecureSkipVerify": {},
	"SMTPForceAuthLogin": {}, "clear_smtp_token": {},
}

func validateSMTPSettingsJSONKeys(body []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	first, err := decoder.Token()
	if err != nil || first != json.Delim('{') {
		return errors.New("request must be one JSON object")
	}
	seen := make(map[string]struct{}, len(smtpSettingsRequestFields))
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		key, ok := token.(string)
		if !ok {
			return errors.New("SMTP setting name must be a string")
		}
		if _, allowed := smtpSettingsRequestFields[key]; !allowed {
			return errors.New("unknown or incorrectly cased SMTP setting")
		}
		if _, duplicate := seen[key]; duplicate {
			return errors.New("duplicate SMTP setting")
		}
		seen[key] = struct{}{}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return err
		}
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return errors.New("request must be one JSON object")
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("request must contain one JSON object")
	}
	return nil
}

func decodeSMTPSettingsUpdate(c *gin.Context) (smtpSettingsUpdateRequest, error) {
	body, err := httpx.ReadAllLimited(c.Request.Body, maxSMTPSettingsRequestBytes)
	if err != nil {
		return smtpSettingsUpdateRequest{}, err
	}
	if err := validateSMTPSettingsJSONKeys(body); err != nil {
		return smtpSettingsUpdateRequest{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var request smtpSettingsUpdateRequest
	if err := decoder.Decode(&request); err != nil {
		return smtpSettingsUpdateRequest{}, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return smtpSettingsUpdateRequest{}, errors.New("request must contain one JSON object")
	}
	if request.SMTPServer == nil || request.SMTPPort == nil || request.SMTPAccount == nil ||
		request.SMTPFrom == nil || request.SMTPSSLEnabled == nil || request.SMTPStartTLSEnabled == nil ||
		request.SMTPInsecureSkipVerify == nil || request.SMTPForceAuthLogin == nil {
		return smtpSettingsUpdateRequest{}, errors.New("all non-secret SMTP settings are required")
	}
	clearToken := request.ClearSMTPToken != nil && *request.ClearSMTPToken
	if clearToken && request.SMTPToken != nil && *request.SMTPToken != "" {
		return smtpSettingsUpdateRequest{}, errors.New("SMTPToken and clear_smtp_token cannot both be supplied")
	}
	return request, nil
}

// UpdateSMTPSettings atomically changes the complete SMTP value domain. The
// token is write-only: omitting it (or submitting an empty placeholder) keeps
// the stored token, while clear_smtp_token explicitly removes it. The route is
// mounted beneath the root-only option group.
func UpdateSMTPSettings(c *gin.Context) {
	request, err := decodeSMTPSettingsUpdate(c)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "无效的 SMTP 设置"})
		return
	}
	updates := map[string]string{
		setting.SMTPServerOption:             *request.SMTPServer,
		setting.SMTPPortOption:               *request.SMTPPort,
		setting.SMTPAccountOption:            *request.SMTPAccount,
		setting.SMTPFromOption:               *request.SMTPFrom,
		setting.SMTPSSLEnabledOption:         strconv.FormatBool(*request.SMTPSSLEnabled),
		setting.SMTPStartTLSEnabledOption:    strconv.FormatBool(*request.SMTPStartTLSEnabled),
		setting.SMTPInsecureSkipVerifyOption: strconv.FormatBool(*request.SMTPInsecureSkipVerify),
		setting.SMTPForceAuthLoginOption:     strconv.FormatBool(*request.SMTPForceAuthLogin),
	}
	clearToken := request.ClearSMTPToken != nil && *request.ClearSMTPToken
	if clearToken {
		updates[setting.SMTPTokenOption] = ""
	} else if request.SMTPToken != nil && *request.SMTPToken != "" {
		updates[setting.SMTPTokenOption] = *request.SMTPToken
	}
	if err := setting.UpdateOptions(updates); err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": err.Error()})
		return
	}
	current := setting.GetOperationsSetting().SMTP
	billingsvc.RecordSystemLog(requestctx.GetUserId(c), billingsvc.LogTypeManage, "smtp.atomic_update")
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data": gin.H{
			"SMTPServer":             current.Server,
			"SMTPPort":               strconv.Itoa(current.Port),
			"SMTPAccount":            current.Account,
			"SMTPFrom":               current.From,
			"SMTPToken":              "",
			"SMTPTokenRedacted":      current.Token != "",
			"SMTPSSLEnabled":         current.SSLEnabled,
			"SMTPStartTLSEnabled":    current.StartTLSEnabled,
			"SMTPInsecureSkipVerify": current.InsecureSkipVerify,
			"SMTPForceAuthLogin":     current.ForceAuthLogin,
		},
	})
}
