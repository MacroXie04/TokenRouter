package settings

import (
	"fmt"
	"os"
	"strings"
)

var smtpEnvironmentAliases = map[string][]string{
	SMTPServerOption:             {"SMTP_HOST", "SMTP_SERVER"},
	SMTPPortOption:               {"SMTP_PORT"},
	SMTPAccountOption:            {"SMTP_USER", "SMTP_ACCOUNT"},
	SMTPFromOption:               {"SMTP_FROM"},
	SMTPTokenOption:              {"SMTP_PASSWORD", "SMTP_TOKEN"},
	SMTPSSLEnabledOption:         {"SMTP_SSL_ENABLED", "SMTP_SSL_ENABLE"},
	SMTPStartTLSEnabledOption:    {"SMTP_STARTTLS_ENABLED", "SMTP_STARTTLS_ENABLE"},
	SMTPInsecureSkipVerifyOption: {"SMTP_INSECURE_SKIP_VERIFY", "SMTP_TLS_INSECURE_SKIP_VERIFY"},
	SMTPForceAuthLoginOption:     {"SMTP_FORCE_AUTH_LOGIN"},
}

func firstSMTPEnvironmentValue(names []string) (string, bool, error) {
	var selected string
	found := false
	for _, name := range names {
		if value, present := os.LookupEnv(name); present {
			if found && value != selected {
				return "", true, fmt.Errorf("conflicting SMTP environment aliases %s", strings.Join(names, " and "))
			}
			selected = value
			found = true
		}
	}
	return selected, found, nil
}

func smtpEnvironmentValues() (map[string]string, bool, error) {
	selected := false
	values := SMTPOptionDefaults()
	for option, aliases := range smtpEnvironmentAliases {
		value, present, err := firstSMTPEnvironmentValue(aliases)
		if err != nil {
			return nil, true, err
		}
		if present {
			values[option] = value
			selected = true
		}
	}
	return values, selected, nil
}

// EffectiveSMTPSetting resolves the entire deployment environment domain when
// any SMTP environment key is present. It never mixes an environment account
// with an option token (or vice versa). A partial or malformed override fails
// closed instead of falling back to persisted credentials.
func EffectiveSMTPSetting() (SMTPSetting, bool, error) {
	values, selected, err := smtpEnvironmentValues()
	if err != nil {
		return SMTPSetting{}, false, fmt.Errorf("invalid SMTP environment configuration: %w", err)
	}
	if selected {
		configured, err := ParseSMTPSetting(values)
		if err != nil {
			return SMTPSetting{}, false, fmt.Errorf("invalid SMTP environment configuration: %w", err)
		}
		if err := configured.ValidateRunnable(); err != nil {
			return SMTPSetting{}, false, fmt.Errorf("invalid SMTP environment configuration: %w", err)
		}
		return configured, true, nil
	}

	configured := GetOperationsSetting().SMTP
	if configured.Server == "" {
		return configured, false, nil
	}
	if err := configured.ValidateRunnable(); err != nil {
		return SMTPSetting{}, false, fmt.Errorf("invalid SMTP option configuration: %w", err)
	}
	return configured, true, nil
}
