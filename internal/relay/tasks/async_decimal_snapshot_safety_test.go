package tasks

import (
	"fmt"
	"github.com/stretchr/testify/require"
	billingsvc "github.com/tokenrouter/tokenrouter/internal/billing"
	"github.com/tokenrouter/tokenrouter/internal/relay/providers/task/doubao"
	"testing"
)

func TestAsyncTaskSnapshotValidationRejectsHostileDecimalExponents(t *testing.T) {
	t.Parallel()

	plan := billingsvc.ReferenceAsyncTaskBillingPlan{
		Version:       1,
		ModelName:     "safe-model",
		GroupRatio:    "1",
		UseFixedPrice: true,
		FixedPrice:    "0.004",
	}
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

			require.Error(t, (aliWanPricingSnapshot{
				Plan: plan, Duration: 5, ResolutionMultiplier: literal,
			}).validate("wan2.2-i2v-flash", "720P"))
			require.Error(t, (geminiVeoPricingSnapshot{
				Plan: plan, Duration: 8, ResolutionMultiplier: literal,
			}).validate("veo-3.1-fast-generate-preview", "4k"))
			_, err := marshalDoubaoTaskProperties(doubaoTaskProperties{
				Version:           doubaoTaskMetadataVersion,
				Family:            "doubao",
				Prompt:            "safe prompt",
				OriginModelName:   "doubao-seedance-1-0-pro-250528",
				UpstreamModelName: "doubao-seedance-1-0-pro-250528",
				Action:            doubao.ActionGenerate,
				Duration:          5,
				Resolution:        "720p",
				VideoInputRatio:   literal,
			})
			require.Error(t, err)

			raw := fmt.Sprintf(
				`{"version":1,"family":"doubao","prompt":"safe prompt","origin_model_name":"doubao-seedance-1-0-pro-250528","upstream_model_name":"doubao-seedance-1-0-pro-250528","action":"generate","duration":5,"resolution":"720p","video_input_ratio":%q}`,
				literal,
			)
			_, err = decodeDoubaoTaskProperties(raw)
			require.Error(t, err)
		})
	}
}

func TestAsyncTaskSnapshotValidationAcceptsSafeDecimalLiterals(t *testing.T) {
	t.Parallel()

	plan := billingsvc.ReferenceAsyncTaskBillingPlan{
		Version:       1,
		ModelName:     "safe-model",
		GroupRatio:    "1",
		UseFixedPrice: true,
		FixedPrice:    "0.004",
	}
	require.NoError(t, (aliWanPricingSnapshot{
		Plan: plan, Duration: 5, ResolutionMultiplier: "2",
	}).validate("wan2.2-i2v-flash", "720P"))
	require.NoError(t, (geminiVeoPricingSnapshot{
		Plan: plan, Duration: 8, ResolutionMultiplier: "1.5",
	}).validate("veo-3.1-generate-preview", "4k"))
	_, err := marshalDoubaoTaskProperties(doubaoTaskProperties{
		Version:           doubaoTaskMetadataVersion,
		Family:            "doubao",
		Prompt:            "safe prompt",
		OriginModelName:   "doubao-seedance-1-0-pro-250528",
		UpstreamModelName: "doubao-seedance-1-0-pro-250528",
		Action:            doubao.ActionGenerate,
		Duration:          5,
		Resolution:        "720p",
		VideoInputRatio:   "1e-2",
	})
	require.NoError(t, err)
}
