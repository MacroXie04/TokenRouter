package quota

const (
	maxSafeDecimalLiteralBytes      = 96
	maxSafeDecimalCoefficientDigits = 64
	maxSafeDecimalExponentMagnitude = 64
)

// IsSafeDecimalLiteral reports whether raw is a bounded ASCII decimal literal
// that can be handed to arbitrary-precision decimal code without allowing an
// attacker-controlled coefficient or exponent to trigger an enormous rescale.
// Scientific notation remains available within the deliberately small bound.
func IsSafeDecimalLiteral(raw string) bool {
	if raw == "" || len(raw) > maxSafeDecimalLiteralBytes {
		return false
	}

	index := 0
	if raw[index] == '+' || raw[index] == '-' {
		index++
		if index == len(raw) {
			return false
		}
	}

	coefficientDigits := 0
	for index < len(raw) && raw[index] >= '0' && raw[index] <= '9' {
		coefficientDigits++
		index++
	}
	if index < len(raw) && raw[index] == '.' {
		index++
		for index < len(raw) && raw[index] >= '0' && raw[index] <= '9' {
			coefficientDigits++
			index++
		}
	}
	if coefficientDigits == 0 || coefficientDigits > maxSafeDecimalCoefficientDigits {
		return false
	}

	if index < len(raw) && (raw[index] == 'e' || raw[index] == 'E') {
		index++
		if index < len(raw) && (raw[index] == '+' || raw[index] == '-') {
			index++
		}
		exponentDigits := 0
		exponentMagnitude := 0
		for index < len(raw) && raw[index] >= '0' && raw[index] <= '9' {
			exponentDigits++
			if exponentDigits > 3 {
				return false
			}
			exponentMagnitude = exponentMagnitude*10 + int(raw[index]-'0')
			if exponentMagnitude > maxSafeDecimalExponentMagnitude {
				return false
			}
			index++
		}
		if exponentDigits == 0 {
			return false
		}
	}

	return index == len(raw)
}
