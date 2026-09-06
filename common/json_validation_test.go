package common

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidateJSONNoDuplicateKeys(t *testing.T) {
	for _, valid := range []string{
		`null`, `42`, `[]`, `{}`, `{"outer":{"first":1,"second":[{"third":2}]}}`,
	} {
		assert.NoError(t, ValidateJSONNoDuplicateKeys([]byte(valid)), valid)
	}
	for _, invalid := range []string{
		``, `{"ratio":1,"ratio":2}`, `{"outer":{"ratio":1,"ratio":2}}`,
		`{"first":1} {"second":2}`, `{"unterminated":`,
		strings.Repeat("[", maxValidatedJSONDepth+2) + "0" + strings.Repeat("]", maxValidatedJSONDepth+2),
	} {
		assert.Error(t, ValidateJSONNoDuplicateKeys([]byte(invalid)), invalid)
	}
}
