package channels

import (
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestChannelFieldsAreClassified guards the fail-closed classifier: every
// JSON field of the channel update request must be classified in exactly one
// of the sensitive/non-sensitive/operational/read-only sets, otherwise it
// silently falls into the fail-closed branch and becomes editable only with
// ChannelSensitiveWrite.
func TestChannelFieldsAreClassified(t *testing.T) {
	typ := reflect.TypeOf(dtoChannelUpdate{})
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		tag := strings.Split(field.Tag.Get("json"), ",")[0]
		if tag == "" || tag == "-" {
			continue
		}
		count := 0
		for _, set := range []map[string]struct{}{channelSensitiveFields, channelNonSensitiveFields, channelOperationalFields, channelReadOnlyFields} {
			if _, ok := set[tag]; ok {
				count++
			}
		}
		assert.Equal(t, 1, count, "field %q must be classified in exactly one channel-field set", tag)
	}
}
