package commerce

import "unicode/utf8"

const maxPaymentServerURLBytes = 2048

// boundedPaymentPublicText applies the same public-text boundary to payment
// callback destinations and credentials without coupling commerce to public
// status handlers.
func boundedPaymentPublicText(value string, maximumBytes int) string {
	if len(value) > maximumBytes || !utf8.ValidString(value) {
		return ""
	}
	for _, character := range value {
		if character < 0x20 || (character >= 0x7f && character <= 0x9f) || character == 0x061c ||
			character == 0x200e || character == 0x200f ||
			(character >= 0x202a && character <= 0x202e) ||
			(character >= 0x2066 && character <= 0x2069) {
			return ""
		}
	}
	return value
}
