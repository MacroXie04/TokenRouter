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
| 18 | Admin subscription management (plan CRUD, bind, resets, invalidate/delete, group up/downgrade, compliance gate) | PASS | `service/subscription_admin.go` + `controller/subscription_admin.go` — `go test ./controller/ -run TestAdminSubscription` |
| 19 | Balance subscription purchase (strict quota conversion, wallet deduction, stacking/cap/snapshot, order + top-up log, calendar reset walk) | PASS | `service.PurchaseSubscriptionWithBalance` + `common.QuotaFromDecimalStrict` + POST `/api/subscription/balance/pay` — `go test ./service/ -run 'TestPurchase|TestResetSubscriptionQuota'` + `go test ./controller/ -run TestSubscriptionBalancePay` |
| 20 | Subscription consumption in relay settlement (pre-consume ledger, billing preference order) | PASS | PARITY rows 124/125 — `service/subscription_funding.go` (idempotent request-id ledger, lazy due-reset, delta clamp) + `service/billing_session.go` (FundingSession preference dispatch: subscription_first/wallet_first/subscription_only/wallet_only + allow_wallet_overflow gate) wired into `relay/controller.go` pre-consume/settle/refund with billing log fields — `go test ./service/ -run 'TestPreConsumeSubscription|TestFundingSession'` + `go test ./relay/ -run TestRelaySubscription` + `go test -race ./service/` |
| 21 | Versioned payment-compliance confirmation and gate | PASS | `service/payment_compliance.go` + `setting.UpdateOptions`: atomic current-terms metadata; all payment/redemption/subscription/affiliate gates require confirmed=true and terms v1; root-session-only POST `/api/option/payment_compliance`; `go test ./controller/ -run 'TestPaymentCompliance|TestRootOption'` |
| 22 | Root model-pricing reset, validated write-through, and multi-node cache refresh | PASS | `service.ResetModelPricingDefaults` atomically stores built-in USD/1M `ModelPrice` plus derived compatibility `ModelRatio` and immediately changes `ComputeQuota`; generic `ModelPrice`/`GroupRatio` writes validate before persistence/publication; every node runs `SyncRuntimeOptions`; `go test ./controller/ -run 'TestResetModelRatio|TestPricingOptionsWriteThrough'` + `go test -race ./service/ -run TestPricingRegistriesConcurrentAccess` |
| 23 | Jimeng per-call task billing and accepted-task settlement | PASS | `service.ComputePerCallQuota` uses decimal `price × QuotaPerUnit × group ratio × duration units` and strict overflow rejection; 121 frames bill 1 unit and 241 frames bill 2. Limited tokens and wallet/subscription funds reserve before dispatch. `FundingSession.CommitAcceptedPerCall` atomically and idempotently persists provider acceptance, adjusts exact funding (including zero-price subscription compensation), and commits user/token counters; definitive failures refund, while ambiguous/accepted outcomes cannot be retried or refunded. `go test ./service/ -run 'TestComputePerCallQuotaAndTokenReservation|TestCommitAcceptedPerCall' -count=1` + `go test ./controller/ -run TestJimeng -count=1` + affected race suite. |
