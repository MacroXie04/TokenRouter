# Billing Matrix

| # | Capability | Status | Evidence / Test |
|---|---|---|---|
| 1 | Prompt token billing | PASS | `service.ComputeQuota` — `go test ./service/...` |
| 2 | Completion token billing | PASS | `service.ComputeQuota` — `go test ./service/...` |
| 3 | Model price registry (USD/1M) | PASS | `service.GetModelPrices` + default fallback |
| 4 | Group ratio multiplier | PASS | `TestGroupRatioAffectsQuota` |
| 5 | Integer saturation (int32 clamp) | PASS | `TestQuotaFromFloatSaturation` |
| 6 | NaN/Inf protection | PASS | `TestQuotaFromFloatNaNInf` |
| 7 | Overflow → no negative charge | PASS | `TestQuotaMathNeverNegativeOnOverflow` |
| 8 | Pre-consume reservation | PASS | `service.PreConsumeUserQuota` (atomic conditional update) |
| 9 | Settlement (post-consume) | PASS | `service.SettleUserQuota` — reserve→settle, no double charge |
| 10 | Failure refund | PASS | `service.RefundUserQuota` on upstream failure |
| 11 | Cache-read/create billing | PASS | `service.ComputeBillingQuota` wires `billingexpr` cache normalization; `go test ./service/...` |
| 12 | Image/audio token billing | PASS | usage fields + `billingexpr` normalization wired; `go test ./service/...` |
| 13 | Tiered expression billing | PASS | `pkg/billingexpr` (expr-lang engine); `go test ./pkg/billingexpr/...` |
| 14 | Wallet top-up / redemption / check-in | PASS | `service/topup.go` + `service/redemption.go` + `service/checkin.go`; `go test ./service/...` |
| 15 | Subscriptions (quota reset) | PASS | `service/subscription.go` (plan/purchase/consume/reset); `go test ./service/...` |
| 16 | Concurrent overspend prevention | PASS | `TestConcurrentPreConsumeDoesNotOverspend` (10 workers, no negative quota) |
| 17 | Saturation audit markers | PASS | `QuotaClamp` threaded into log `other` |
