package service

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tokenrouter/tokenrouter/model"
)

func TestRelayQuotaAsyncReviewPricingRejectsHostileDecimalExponents(t *testing.T) {
	t.Parallel()

	plan := `{"version":1,"model_name":"safe-model","group_ratio":"1","use_fixed_price":true,"fixed_price":"0.004"}`
	hostile := []string{
		"1e-2147483648",
		"1e2147483647",
		"-1E-2147483648",
		"+1E2147483647",
	}
	for _, literal := range hostile {
		literal := literal
		t.Run(literal, func(t *testing.T) {
			t.Parallel()

			ali := json.RawMessage(fmt.Sprintf(
				`{"plan":%s,"duration":5,"resolution_multiplier":%q}`,
				plan, literal,
			))
			veo := json.RawMessage(fmt.Sprintf(
				`{"plan":%s,"duration":8,"resolution_multiplier":%q}`,
				plan, literal,
			))
			require.False(t, validRelayQuotaAsyncReviewPricing(model.TaskOperationPlatformAliWan, ali))
			require.False(t, validRelayQuotaAsyncReviewPricing(model.TaskOperationPlatformGeminiVeo, veo))
		})
	}
}

func TestRelayQuotaAsyncReviewPricingAcceptsSafeDecimalLiteral(t *testing.T) {
	t.Parallel()

	raw := json.RawMessage(`{
		"plan":{"version":1,"model_name":"safe-model","group_ratio":"1e-2","use_fixed_price":true,"fixed_price":"4e-3"},
		"duration":8,
		"resolution_multiplier":"1.5"
	}`)
	require.True(t, validRelayQuotaAsyncReviewPricing(model.TaskOperationPlatformGeminiVeo, raw))
}
