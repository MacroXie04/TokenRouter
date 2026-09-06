package billing

import (
	setting "github.com/tokenrouter/tokenrouter/internal/settings"
	"strconv"
)

const CurrentPaymentComplianceTermsVersion = "v1"

// PaymentComplianceStatus is returned after the root confirms the current
// payment terms.
type PaymentComplianceStatus struct {
	Confirmed    bool   `json:"confirmed"`
	TermsVersion string `json:"terms_version"`
	ConfirmedAt  int64  `json:"confirmed_at"`
	ConfirmedBy  int    `json:"confirmed_by"`
}

// ConfirmPaymentCompliance persists the complete versioned confirmation in one
// transaction and publishes it to the option cache only after commit.
func ConfirmPaymentCompliance(userID int, clientIP string, confirmedAt int64) (PaymentComplianceStatus, error) {
	status := PaymentComplianceStatus{
		Confirmed:    true,
		TermsVersion: CurrentPaymentComplianceTermsVersion,
		ConfirmedAt:  confirmedAt,
		ConfirmedBy:  userID,
	}
	err := setting.UpdateOptions(map[string]string{
		setting.PaymentComplianceConfirmedOption:    "true",
		setting.PaymentComplianceTermsVersionOption: CurrentPaymentComplianceTermsVersion,
		setting.PaymentComplianceConfirmedAtOption:  strconv.FormatInt(confirmedAt, 10),
		setting.PaymentComplianceConfirmedByOption:  strconv.Itoa(userID),
		setting.PaymentComplianceConfirmedIPOption:  clientIP,
	})
	return status, err
}
