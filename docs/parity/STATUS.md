# TokenRouter Status

Last updated: 2026-08-15 (automated build)

## Overall

- **SOFTWARE_PARITY**: NOT COMPLETE — iteration 94 closes the final
  `API_MATRIX` implementation gap with the official Jimeng asynchronous task
  relay. The route matrix is now 228 PASS, 113 REFERENCE_PLACEHOLDER, and 0
  NOT_STARTED, verified by exact route-table matching. `PARITY_MATRIX` is 141
  PASS, 1 IN_PROGRESS (Jimeng full-gate closure), 0 NOT_STARTED, 2
  BLOCKED_EXTERNAL, and 1 REFERENCE_PLACEHOLDER (maximum ID 146). Broad provider, database-schema, and
  frontend inventories remain unreconciled: `PROVIDER_MATRIX` currently has 3
  Jimeng/task-format PASS rows with its independent image/API-type rows and
  other providers still NOT_STARTED; `DATABASE_MATRIX` remains 4 PASS / 31
  NOT_STARTED; `FRONTEND_MATRIX` remains mostly NOT_STARTED. These inventories
  prevent `SOFTWARE_PARITY=PASS` despite the route surface now having no
  offline implementation gap.
  Iteration 66 added the OAuth
  state ceremony, code-verified email bind, and the admin passkey/2FA
  management endpoints with contract tests. Iteration 67 added the anonymous
  request-body limit middleware (512 KB default, env-tunable) on all
  anonymous POST endpoints, matching the reference middleware chains.
  Iteration 68 added the TurnstileCheck middleware (option-store config via
  the reference keys, query-param contract, wired on verification/reset-
  password/register/login/checkin) and the per-IP email verification rate
  limit (2/30s), completing the anonymous-endpoint middleware chains.
  Iteration 69 reworked token management onto the reference /api/token group
  (user-scoped CRUD, masked keys, search, auto-groups, batch key disclosure,
  plus GET /api/usage/token/) — the previous admin-scoped list and self-
  service /api/user/token handlers were replaced and the frontend repointed.
  Iteration 70 added the subscription admin surface (11 AdminAuth routes under
  /api/subscription/admin: plan list/create/update/status-patch, user bind and
  per-user create, subscription listing, per-user and plan-wide quota resets
  with the SubscriptionResetResult contract and per-user manage logs,
  invalidate/hard-delete with group upgrade/downgrade snapshot semantics,
  payment-compliance gating, reference Chinese validation messages) with
  contract tests (`go test ./controller/ -run TestAdminSubscription`).
  Iteration 71 closed the access-token feature: GET /api/user/token issues a
  reference-format dashboard PAT (28-32 char base64 in users.access_token,
  previous token invalidated on regeneration) behind CriticalRateLimit +
  UserCriticalRateLimit('access-token') + DisableCache, and the auth
  middleware accepts PATs as a bearer fallback across UserAuth/AdminAuth/
  RootAuth (disabled users rejected).
  Iteration 72 added admin user search (keyword/id LIKE search with
  group/role/status filters incl. soft-deleted selection, sort whitelist,
  credential fields omitted) and admin user manage (disable/enable/delete/
  promote/demote/quota add|subtract|override with the reference role guards,
  Root protections, session revocation on demote, and audit logs) with
  contract tests (`go test ./controller/ -run TestAdminSearchUsers\|TestAdminManageUser`).
  Iteration 73 unified the user purchase path onto the reference balance-pay
  contract (row 116): POST /api/subscription/balance/pay inside the
  /api/subscription UserAuth group (plans/self moved there too; the
  TokenRouter-only /api/user/subscription[/purchase] aliases were removed and
  the SPA repointed), a single-transaction purchase (strict non-clamping
  quota conversion via common.QuotaFromDecimalStrict, row-locked wallet
  deduction, stacking/cap/snapshot via the shared create-from-plan
  transaction, completed order row with the SUBBALUSR trade-no format,
  post-commit top-up log), compliance-gated {plan:...} DTO plan listing, and
  the reference catch-up calendar walk in ResetSubscriptionQuota
  (`go test ./service/ -run 'TestPurchase|TestResetSubscriptionQuota'`,
  `go test ./controller/ -run TestSubscriptionBalancePay`). Two gaps were
  discovered and recorded: relay settlement never consumes subscription quota
  (row 124) and the subscription self view/billing-preference contract is
  missing (row 125).
  Iteration 74 added the channel read surface — GET /api/channel/search with
  the full reference contract (keyword id/name/exact-key/base-url + model
  substring + comma-list group filter + status enabled|1/disabled|0 + type
  filters, tag_mode via distinct-tag subquery with per-tag fetch, sort
  whitelist id/name/priority/balance/response_time/test_time with id_sort and
  priority-desc default, in-memory status filter → type_counts → type filter
  ordering, p/page_size paging, keys omitted, multikey internals stripped,
  {success,message,data:{items,total,type_counts}} shape), /channel/models,
  /channel/models_enabled, /channel/ops, and reworked the test endpoints:
  GET /channel/test/:id returns the reference {success,message,time} shape
  (updates response_time; optional ?model override), and GET /channel/test
  now enqueues a durable channel_test system task (SystemTask rows with
  active_key dedupe, the reference 409 "已有通道测试任务正在运行或等待中…"
  contract, and a lease-guarded background runner that sweeps testable
  channels recording response_time/test_time/auto-ban and persists a
  {tested,succeeded,failed,disabled,enabled} result) — contract tests in
  `go test ./controller/ -run 'TestChannelSearch\|TestChannelListModels\|TestChannelOps\|TestChannelTest'`.
  Iteration 75 completed the channel write-side batch (16 routes): status
  update + batch (manageable-status validation, changed flag, other_info
  status_reason/status_time, ability-enabled follows), disabled-channel
  purge, tag disable/enable/edit (rename, priority/weight/model mapping/
  models/groups/param+header overrides with the reference JSON-validation
  messages and ability rebuild on model/group changes), batch delete
  (transactional with abilities), ability fix (truncate + models×groups
  rebuild with status/priority/weight/tag, try-lock guard), batch tag with
  ability mirroring, tag/models (longest list), channel copy (key copied,
  _复制 suffix, reset_balance), multi-key management (status listing with
  pagination/counts/previews/filter, per-key and all-key enable/disable,
  key deletion with reindexing, auto-disabled sweep, the reference Chinese
  messages and guards), upstream model fetching (stored and preview
  channels, /v1/models with provider headers, Gemini /v1beta/models), and
  balance refresh (OpenAI dashboard subscription+usage two-call, persisted
  balance, zero-balance auto-disable "余额不足", multikey refusal,
  "尚未实现" for other provider types = the reference default) — contract
  tests in `go test ./controller/ -run 'TestChannelStatus\|TestChannelDeleteDisabled\|TestChannelTag\|TestChannelDeleteBatch\|TestChannelFix\|TestChannelFetch\|TestChannelBatchTag\|TestChannelCopy\|TestChannelMultiKey\|TestChannelBalance'`.
  Iteration 76 completed the redemption admin slice (rows 128-129): the
  full seven-endpoint contract under AdminAuth — compliance-gated batch
  create (1-20 rune name, count 1-100, future-expiry-only, one row per
  32-hex key, keys returned in data), pageInfo list, keyword search
  (numeric keywords also match the id, name prefix), the reference status
  filters (expired/1/2/3), get/update with "id 为空！" and past-expiry
  rejection plus status_only mode, delete by id, and the invalid sweep
  (used/disabled/expired) — with the reference Chinese messages. This also
  fixed a data-parity bug: TokenRouter's status numbering was swapped
  (used=2/disabled=3) versus the reference (enabled=1, disabled=2, used=3);
  redeem, filters, update and sweep all now use the reference numbering
  with a guard test. Contract tests in
  `go test ./controller/ -run 'TestRedemption'`. Also repaired
  PARITY_MATRIX.csv RFC-4180 quoting on 22 rows (a concurrent rewrite had
  stripped all field quoting, splitting fields at internal commas) and
  added a scripted verify (every row now parses to exactly 8 fields).
  Iteration 77 implemented the fine-grained channel permission layer
  (row 130), closing the last security-priority gap: GET /api/authz/catalog
  returns the reference permission schema (channel resource with
  read/operate/write/sensitive_write/secret_view actions, root/admin role
  descriptors with baseline grant matrices, internal DefaultRoles
  redacted); a dedicated sub/obj/act permission enforcer over the shared
  casbin_rule table backs it (root is a superuser, admin baselines are
  read/operate/write, role baselines reseed idempotently at boot and
  re-sync every SYNC_FREQUENCY for multi-node); every channel route is now
  wired with the reference per-route RequirePermission (sensitive routes:
  add/delete/delete-disabled/batch/copy/fetch_models), plus the inline
  sensitive-write guards in tag edit (param/header overrides) and
  multi-key manage (delete_key/delete_disabled_keys); the channel update
  handler gained the reference fail-closed classifier (sensitive fields
  compared old-vs-new, unknown fields treated as sensitive, guard test
  enforces classification of every request field) and now takes id from
  the body (the route has no :id param — it previously updated WHERE
  id=0); PUT /api/user accepts root-only admin_permissions (baseline-
  matching entries omitted, explicit denies for revoked baselines, policy
  reload after commit) and GetUser/GetSelf expose admin_permissions
  (nested under permissions with the reference sidebar map). Contract
  tests in `go test ./controller/ -run 'TestPermission\|TestChannelFieldsAreClassified'`.
  PARITY_MATRIX: 124 PASS, 0 IN_PROGRESS, 2 NOT_STARTED (rows 124-125:
  subscription consumption in relay settlement + self-view/preference
  contract, owned by the concurrent session), 2 BLOCKED_EXTERNAL (live
  credentials), 1 REFERENCE_PLACEHOLDER.
  Iteration 78 closed both remaining NOT_STARTED rows (124-125, the
  billing-correctness gap discovered in iteration 73): relay settlement
  now funds requests from active subscriptions. New
  service/subscription_funding.go ports the reference funding contracts —
  PreConsumeUserSubscription with an idempotent request-id ledger
  (SubscriptionPreConsumeRecord: replay returns the original reservation,
  refunded ids are permanently rejected, unique-index dup race re-reads),
  candidate walk over locked active subscriptions in end_time-asc order
  with insufficient-skip and "subscription quota insufficient, need=N",
  the lazy due-reset calendar walk before the remain check
  (maybeResetUserSubscriptionWithPlanTx; never-period schedule semantics
  per deviation #17), RefundSubscriptionPreConsume (idempotent, delta
  inlined in one tx — SQLite-safe vs the reference's nested independent
  tx), PostConsumeUserSubscriptionDelta (clamp <0→0, exceeds-total
  rejection), and the 7-day ledger cleanup wired into the subscription
  reset job. New service/billing_session.go dispatches by the user's
  billing preference (subscription_first default with the
  allow_wallet_overflow fallback gate, wallet_first, subscription_only,
  wallet_only — preference stored in users.setting JSON via
  read-modify-write UserSettings so quota-warning fields survive);
  the ledger request id is generated server-side (client X-Request-Id is
  never a ledger key — replay hardening); Settle adjusts the reservation
  to actual usage and records used_quota/request_count exactly once
  (RecordUserUsage), subscription refunds retry ×3 while the
  non-idempotent wallet refund stays single-shot; the consume log now
  carries the reference billing fields (billing_source, subscription_id/
  pre_consumed/post_delta/total/used/remain/consumed/plan_id/plan_title,
  wallet_quota_deducted=0; funding-insufficiency relay status is 400 vs
  the reference 403 — deviation #18). Row 125: GET /api/subscription/self
  returns the reference {billing_preference, subscriptions,
  all_subscriptions} shape ({subscription:...} rows, errors degrade to
  empty arrays) and PUT /api/subscription/self/preference normalizes
  unknown values to subscription_first silently (400 参数错误 only for
  malformed JSON); the SPA console renders active subscriptions and the
  preference selector. Tests: `go test ./service/ -run
  'TestPreConsumeSubscription|TestRefundSubscriptionPreConsume|TestPostConsumeUserSubscriptionDelta|TestFundingSession|TestConcurrentSubscriptionPreConsume|TestCleanupSubscriptionPreConsumeRecords|TestUserSettingsMergePreservesFields|TestNormalizeBillingPreference'`,
  `go test ./controller/ -run 'TestSubscriptionSelfView|TestSubscriptionPreferenceUpdate'`,
  `go test ./relay/ -run TestRelaySubscription` (end-to-end
  subscription-funded settle + refund-on-upstream-failure against a mock
  upstream), and `go test -race ./common/... ./service/...`.
  PARITY_MATRIX: 126 PASS, 0 IN_PROGRESS, 0 NOT_STARTED,
  2 BLOCKED_EXTERNAL (live credentials), 1 REFERENCE_PLACEHOLDER.
  Iteration 79 completed the user top-up/payment batch (row 131): GET
  /api/user/topup/info returns the reference configuration shape
  (compliance-gated pay-method catalog with the reference defaults plus the
  appended Stripe method, enable flags, minimums, amount presets and
  discounts, topup_link); POST /api/user/amount and /stripe/amount convert
  amounts into payable money (display-type token scaling, unified
  GroupRatio, Price/StripeUnitPrice options, preset discounts) with the
  reference {message,data} shapes; POST /api/user/pay builds the signed
  Epay purchase URL through the go-epay SDK (the same public SDK the
  reference uses) with the USR<id>NO<rand><ts> trade-no format and records
  the pending order; POST+GET /api/user/epay/notify verifies the callback
  signature and settles TRADE_SUCCESS idempotently (tampering and
  payment-method mismatches rejected; bare success/fail bodies); POST
  /api/user/stripe/pay creates a real checkout session via stripe-go
  (trusted-redirect-domain validation, metadata carrying user_id/trade_no/
  quota for the existing webhook) and records the pending order.
  STRIPE_SECRET_KEY stays an env credential; live money movement remains
  BLOCKED_EXTERNAL. Payment options (PayMethods/Price/MinTopUp/PayAddress/
  EpayId/EpayKey/CustomCallbackAddress/StripeMinTopUp/StripePriceId/
  StripeUnitPrice/StripePromotionCodes/QuotaDisplayType/PaymentSetting)
  are DB-backed; the API-matrix classifier's blanket /epay placeholder
  rule was narrowed to the subscription-side endpoints. Contract tests in
  `go test ./controller/ -run 'TestTopUp'`.
  Iteration 80 completed the log analytics batch (rows 205-211 except the
  then-deferred affinity stats): admin GET /api/log with the full reference
  filter contract (type, timestamps, username, token/model names, channel,
  quoted group column, request_id/upstream_request_id, pageInfo paging),
  admin GET /api/log/stat and user GET /api/log/self/stat with the
  trailing-60s rpm/tpm window and unconditional type=consume aggregation
  (the reference accepts but ignores the type param), user GET
  /api/log/self with redaction (channel_name cleared, admin_info/
  audit_info stripped from Other, display-id renumbering) plus the
  reference LIKE/escape sanitizer (no %%, <=2 %, >=2 literal chars when
  fuzzy; exact match otherwise), relay GET /api/log/token via
  TokenAuthReadOnly, and both deprecated search endpoints returning the
  reference {success:false, message:"该接口已废弃"} shape. Rows 205, 206,
  208, 209, 210, 211 flipped to PASS in that iteration; row 207 was completed
  by the real rule/fingerprint routing cache in iteration 89. Contract tests in
  `go test ./controller/ -run 'TestLogs|TestLogByKey'`.
  Iteration 81 completed the data analytics slice (rows 219-223): the
  quota_data hour-bucketed histogram is now recorded at consume time
  (in-memory aggregation, DataExportEnabled/DataExportInterval options
  with the reference defaults true/5min, flushed by a background loop
  started from StartBackgroundJobs with atomic counter increments for
  multi-node safety), and the /api/data group serves the reference
  contracts — admin per-(model,hour) and per-(username,hour) histograms
  with username filtering, self per-(model,hour) with the 30-day span
  limit ("时间跨度不能超过 1 个月"), and role-scoped flow analytics
  (root sees node/token dimensions, admin sees user/group/model/channel,
  self sees token/group/model; use_group <> '' only; token names
  resolved with deleted tokens left empty; missing channels fall back to
  channel-<id>; invalid time ranges rejected with the reference
  messages). Rows 219-223 are all PASS now — row 219 had been a false
  PASS: the old house dashboard-stats endpoint at /api/data collided
  with the reference quota-histogram route via trailing-slash
  normalization; the stats moved to the extension path
  /api/dashboard/stats (deviation #20) and the frontend repointed.
  Contract tests in `go test ./controller/ -run 'TestData'` plus the
  write-path/flush test in `go test ./service/ -run 'TestQuotaData|TestDataExport'`.
  Iteration 82 completed the subscription Stripe pay slice (row 102):
  POST /api/subscription/stripe/pay with the reference validation chain
  (payment-compliance gate, plan enabled + StripePriceId checks, sk_/rk_
  secret and webhook-secret gates, per-user purchase cap via
  CountUserSubscriptionsByPlan) opens a subscription-mode checkout
  session via stripe-go (ClientReferenceID as the sub_ref_+sha1 trade
  no, price with quantity 1, customer email/creation or existing Stripe
  customer, return to /wallet) and records the pending SubscriptionOrder
  with the reference {message,data} shapes. The webhook now fulfills
  orders: checkout.session.completed completes the order transactionally
  (user-row locked, subscription created with the purchase-cap check,
  top-up row upserted, order marked success, 订阅购买成功 system log,
  idempotent on duplicate delivery, unknown references fall back to the
  legacy top-up metadata path) and checkout.session.expired marks
  pending orders expired with the top-up fallback. Contract tests in
  `go test ./controller/ -run 'TestSubscriptionStripe'` (pay validation
  chain + checkout params + webhook E2E completion/expiry/idempotency).
  Iteration 83 completed custom OAuth provider administration and admin user-
  binding inspection (row 135; API rows 86-87 and 131-136): RootAuth CRUD
  with slug/built-in-conflict validation, secret-redacted responses, deletion
  guards while bindings exist, and SSRF-guarded OIDC discovery; AdminAuth can
  list or unbind a lower-role user's provider bindings. Contract tests in
  `go test ./controller/ -run 'TestCustomOAuth|TestAdminUserOAuth' -count=1`.
  Iteration 84 completed the system-task and system-instance administration
  surface (row 136; API rows 212-218): RootAuth log-cleanup enqueue with
  active-task deduplication, a durable 100-row batch runner with persisted
  progress and decoded payload/state/result responses, current/list/get task
  APIs, and online/stale instance listing plus single/bulk stale deletion at
  the reference 90-second threshold. The cleanup contract test executes the
  runner against 3,000 synthetic old log rows and proves fresh rows survive;
  the instance contract proves JSON info decoding and stale-only deletion.
  `go vet ./...`, `go test ./...`, standalone protocolkit vet/build/test, API
  matrix verification, and `scripts/tokenrouter-acceptance.sh` all passed;
  the full acceptance gate passed twice consecutively without intervening code
  changes. Contract tests: `go test ./controller/ -run
  'TestSystemTaskLogCleanupContract|TestSystemInfoInstancesContract' -count=1`.
  Iteration 85 completed admin user creation (row 137; API row 90): AdminAuth
  POST /api/user/ now trims and validates the reference model fields, rejects
  equal/higher roles, and copies only username/password/display_name/role into
  the persisted account so submitted quota, group, status, email, affiliate,
  credential and remark fields cannot be injected. User creation and explicit
  permission overrides commit in one transaction; non-root permission attempts
  roll the user row back, successful changes reload policy. Bcrypt password,
  InitialQuota, enabled/default-group/auth-version, four-character affiliate
  code, role-derived sidebar settings, initial-quota log and actor-owned create
  audit are covered. Every business error retains the reference HTTP-200
  {success:false,message} envelope. Targeted and complete controller suites,
  `go vet ./...`, `go test ./...`, API matrix exact verification, and the full
  acceptance gate passed; acceptance passed twice consecutively without
  intervening code changes. Contract tests: `go test ./controller/ -run
  'TestAdminCreateUser' -count=1`.
  Iteration 86 closed the versioned payment-compliance and option-security gap
  (row 138; API row 122). POST /api/option/payment_compliance now requires a
  RootAuth dashboard session and explicitly rejects dashboard PATs, requires an
  affirmative acknowledgement, atomically persists confirmed/current-v1/at/by/IP
  metadata, updates cache only after commit, writes a secret-free actor audit,
  and returns the reference status payload. Every payment/redemption/
  subscription/affiliate gate now requires both confirmed=true and the current
  terms version. The existing GET/PUT /api/option/ routes were corrected from
  AdminAuth to RootAuth; sensitive Token/Secret/Key values are omitted, generic
  writes cannot mutate compliance fields, and positive affiliate bonuses cannot
  be enabled before confirmation. The root-only frontend settings workflow now
  uses key/value option updates and an explicit compliance acknowledgement panel;
  ordinary admins neither fetch nor render options. Focused security/payment
  tests, frontend typecheck, all controller/backend tests, exact API verification,
  and the full acceptance gate passed; acceptance passed twice consecutively
  without intervening code changes. Contract tests: `go test ./controller/ -run
  'TestPaymentCompliance|TestRootOption|TestOptionAndCompliance' -count=1`.
  Iteration 87 completed the root model-pricing reset (row 139; API row 125).
  The reference resets its ratio registry, while TokenRouter bills from a unified
  USD-per-million `ModelPrice` registry, so the handler atomically persists an
  explicit built-in price baseline and a prompt-price-derived compatibility
  `ModelRatio`, then replaces live billing state after commit. The generic option
  path now validates and immediately publishes `ModelPrice` and `GroupRatio`
  changes; malformed, negative, non-finite and non-positive values leave both the
  database and runtime caches unchanged. Pricing maps use defensive copies behind
  an RWMutex and pass a concurrent reader/writer race test. Verification also
  found that the periodic option refresh was cluster-lease-gated and refreshed
  only the settings map, leaving pricing stale on non-winning nodes; every node
  now runs `SyncRuntimeOptions` and publishes both registries only after validating
  a coherent snapshot. The root-only frontend action explains the live billing
  effect, requires explicit confirmation and refreshes options after success.
  Focused controller/service tests, full affected package suites, frontend
  typecheck/build, exact API verification, and the full acceptance gate passed;
  acceptance passed twice consecutively without intervening code changes.
  Contract tests: `go test ./controller/ -run
  'TestResetModelRatio|TestPricingOptionsWriteThrough' -count=1` and `go test
  -race ./service/ -run TestPricingRegistriesConcurrentAccess`.
  Iteration 88 completed detailed performance metrics (row 140; API row 14) and
  corrected the previously non-parity summary payload (row 73; API row 13). Relay
  success and terminal failure paths now record atomic per-model/group samples;
  streamed responses measure first-token latency at the first body write. Hot
  buckets expose current data immediately, completed buckets flush through an
  additive `(model_name, group, bucket_ts)` conflict upsert, and configurable
  retention cleans durable history. Queries merge durable and hot counters without
  double counting and return the reference stable series schema with chronological
  TTFT, latency, success-rate and token-throughput points; model summaries include
  rounded aggregates and the last three success rates while omitting internal
  request counts. The reference `perf_metrics_setting.*` enabled/flush/bucket/
  retention controls are DB-backed and every node starts its local flusher. The
  public home view renders 24-hour model performance and the root grouped editor
  exposes all four controls. Focused model/service/controller/relay contracts,
  stream and failure E2E tests, full affected suites, service race tests, frontend
  typecheck/build, exact API verification, and the full acceptance gate passed;
  acceptance passed twice consecutively without intervening code changes.
  Contract tests: `go test ./service/ -run TestPerfMetrics -count=1`, `go test
  ./controller/ -run TestPerfMetricsContract -count=1`, and `go test ./relay/ -run
  'TestRelayChatCompletionsEndToEnd|TestClaudeMessagesViaOpenAIChannelStream|TestRelaySubscriptionRefundOnUpstreamFailure' -count=1`.
  Iteration 89 replaced the simple per-user/model 30-minute sticky map with the
  reference settings-driven channel-affinity engine (rows 68/141; API rows 123,
  124, and 207). Named rules match model/path/user-agent/value regexes and extract
  keys from request headers, reusable JSON bodies, or Gin context values. Cache
  keys are full SHA-256 fingerprints, observability exposes only 8-hex key
  fingerprints, and rule/model/group dimensions, per-rule/default TTL, bounded
  LRU capacity, disabled-channel cleanup, switch-on-success and skip-retry behavior
  are honored. Affinity is accepted only inside the enabled ability candidate set;
  ordinary fallback remains priority-first/weighted, and retry exclusions now
  accumulate across all attempts. Redis deployments share indexed cache entries
  and atomically aggregate prompt-cache usage with Lua; single-node deployments use
  the same bounded TTL semantics in memory. The default Codex/Claude rules also
  apply explicit request-header pass-through allow-lists. Root cache stats and
  all/rule clear controls, AdminAuth per-fingerprint usage counters, log metadata,
  validated atomic settings snapshots, remote-node refresh, and a root frontend
  rules/cache workflow are implemented. Service and endpoint contracts, concurrent
  setting/counter race tests, a lower-priority affinity/skip-retry relay E2E, the
  existing ordinary retry E2E, frontend typecheck/build, exact route verification,
  and the full acceptance gate passed; acceptance passed twice consecutively
  without intervening code changes. Contract tests: `go test ./service/ -run
  'TestRuleBasedChannelAffinity|TestChannelAffinity|TestPreferredChannel' -count=1`,
  `go test ./controller/ -run TestChannelAffinity -count=1`, `go test ./relay/ -run
  'TestRelayAffinityOverridesPriorityAndSkipsRetry|TestRelayRetriesToSecondChannel'
  -count=1`, and `go test -race ./setting/ ./service/`.
  Iteration 90 completed configured-group listing and reusable prefill-group
  management (row 142; API rows 224-228). GET `/api/group/` returns a stable
  sorted snapshot of live GroupRatio keys. The AdminAuth prefill surface provides
  type-filtered updated-time-desc listing plus create, update, and soft delete with
  the reference HTTP-200 envelopes and Chinese validation messages. The existing
  placeholder schema was corrected: `items` is now a custom JSON value that emits
  arrays/strings as actual JSON rather than quoted storage text; model/tag lists and
  endpoint JSON strings retain their reference wire forms. Names are unique among
  active rows, duplicate creation is race-tested, soft-deleted names are reusable,
  and missing-ID updates cannot silently upsert. Startup repairs the legacy
  non-partial SQLite/PostgreSQL name index before use, while legacy blank/plain-text
  item values scan safely. The admin frontend adds type filtering and complete
  create/edit/cancel/delete workflows with endpoint JSON validation, confirmation,
  busy, success, error, and empty states. Model/service/controller contracts, focused
  race tests, the complete Go suite, frontend typecheck/build, translations, exact
  route verification, and the full acceptance gate passed; acceptance passed twice
  consecutively without intervening code changes. Contract tests: `go test
  ./controller/ -run 'TestConfiguredGroups|TestPrefillGroup' -count=1`, `go test
  -race ./model/ ./service/ -run
  'TestJSONValue|TestEnsurePrefillGroup|TestConcurrentPrefillGroup|TestUpdatePrefillGroup'
  -count=1`.
  Iteration 91 completed the AdminAuth model-metadata registry and upstream
  synchronization surface (row 143; API rows 239-247). Paged list/search return
  global vendor counts plus exact/rule enrichment for bound channels, groups,
  quota type, matched models, and stored/default endpoint JSON. CRUD validates
  persisted fields, rereads authoritative responses, rejects missing-ID upserts,
  soft-deletes safely, and uses portable case-insensitive search. Missing models
  are the stable difference between enabled abilities and active metadata.
  Preview/apply fetch model and vendor catalogs concurrently through the SSRF
  guard with context cancellation, bounded reads, retry and ETag caching; failure
  envelopes and either catalog's transport errors fail closed. Local mock tests
  cover conflict previews, explicit disabled status, vendor creation, selective
  overwrite, no-op behavior, malformed bodies, authorization, and upstream
  failures. Nullable active-name keys enforce concurrent model/vendor uniqueness
  on SQLite/MySQL/PostgreSQL while preserving name reuse; staged startup migration
  keeps the newest legacy duplicate active, soft-deletes older rows, repoints
  duplicate-vendor references, backfills keys, and then creates unique indexes.
  A full-suite race exposed by this work was also fixed: controller tests now use
  a controlled one-shot system-task claim pass instead of leaving a global runner
  attached to subsequently replaced test databases. Focused controller/model/
  service tests, five repeated system-task contracts, vet, the complete Go suite,
  targeted races, exact route verification, frontend build/typecheck/translations,
  SQLite migration, and secret scan passed. The full acceptance gate passed twice
  consecutively without intervening file changes. Contract tests: `go test
  ./controller/ -run TestModelMetadata -count=1`, `go test -race ./model/ -run
  'TestEnsureRegistryActiveNames|TestDeleteModelMetadata|TestConcurrentModelName'
  -count=1`, and `go test ./service/ -run TestModelSync -count=1`.
  API_MATRIX: 221 PASS, 114 REFERENCE_PLACEHOLDER, 6 NOT_STARTED.
  PARITY_MATRIX: 139 PASS, 0 IN_PROGRESS, 0 NOT_STARTED,
  2 BLOCKED_EXTERNAL (live credentials), 1 REFERENCE_PLACEHOLDER.
  Iteration 92 completed the role-scoped Midjourney and generic asynchronous task
  history slice (row 144; API rows 229-232) and the public Midjourney image proxy
  (API row 306). UserAuth self routes derive ownership only from the session and
  suppress the generic task channel ID; AdminAuth routes span users, expose the
  reference channel field, and enrich generic tasks with active usernames. Both
  collections implement stable descending-ID pages, `p`/`page_size` plus `ps` and
  `size` aliases, exact platform/task/status/action/channel/time filters, and
  explicit database failures instead of silent empty success. The generic DTO
  projects only the reference public fields, emits typed `properties` and
  structural `data`, redacts `private_data`, and exposes only its public
  `result_url`. Midjourney history honors `MjForwardUrlEnabled` and rewrites to
  `ServerAddress/mj/image/:id`; that unauthenticated proxy performs context-aware
  SSRF-safe fetches, caps responses at 32 MiB, preserves upstream errors, and
  forces non-raster MIME types to download. Synthetic role/ownership/filter/
  paging/legacy-JSON/missing-table tests and a local image server cover success,
  upstream errors, missing tasks, MIME hardening, size limits, and SSRF blocking.
  An independent focused review found two medium issues (typed properties and
  image forwarding); both were fixed and the verification review found no
  remaining high/medium findings. Focused tests, vet, the complete Go suite,
  targeted races, exact route verification, and the full acceptance gate passed;
  acceptance passed twice consecutively without intervening file changes.
  Contract tests: `go test ./controller/ -run
  'TestTaskHistory|TestMidjourneyHistory|TestMidjourneyImage' -count=1` and
  `go test -race ./controller/ ./service/ -run
  'TestTaskHistory|TestMidjourneyHistory|TestMidjourneyImage' -count=1`.
  API_MATRIX: 226 PASS, 113 REFERENCE_PLACEHOLDER, 2 NOT_STARTED.
  PARITY_MATRIX: 140 PASS, 0 IN_PROGRESS, 0 NOT_STARTED,
  2 BLOCKED_EXTERNAL (live credentials), 1 REFERENCE_PLACEHOLDER.
  DATABASE_MATRIX: 4 PASS, 31 NOT_STARTED.
  Iteration 93 completed the dashboard playground chat-completions route (row
  145; API row 275). Session-authenticated requests now create a request-local
  `playground-<group>` token and enter the same OpenAI relay lifecycle as
  `/v1/chat/completions`: sensitive-word checks, prompt estimation, wallet or
  subscription reservation, priority/weight/affinity routing, retry, non-stream
  or SSE response forwarding, usage-based settlement, performance metrics, and
  consumption logging. The temporary token is never persisted and is marked
  unlimited only to reproduce the reference's playground token-quota bypass;
  real user funding and usage counters remain authoritative. Dashboard PATs are
  rejected with the reference 500 `new_api_error/access_denied` payload. The
  optional request `group` is parsed from a reusable bounded body, authorized
  against TokenRouter's documented flat configured-group model, removed from the
  upstream payload, and used consistently for channel selection, pricing, token
  naming, and logs; malformed or unauthorized overrides fail before selection.
  A local OpenAI server exercises non-stream and SSE responses, upstream path and
  key setup, allowed/denied groups, malformed requests, session/PAT/anonymous
  authorization, quota/request-count effects, log fields, and non-persistence.
  Focused review found the missing group override; after the fix, verification
  reported no remaining high/medium findings. Focused tests, vet, the complete Go
  suite, targeted races, exact route verification, and the full acceptance gate
  passed; acceptance passed twice consecutively without intervening file changes.
  Contract tests: `go test ./controller/ -run TestPlayground -count=1` and
  `go test -race ./controller/ ./middleware/ -run
  'TestPlayground|TestRoleGuards' -count=1`.
  API_MATRIX: 227 PASS, 113 REFERENCE_PLACEHOLDER, 1 NOT_STARTED.
  PARITY_MATRIX: 141 PASS, 0 IN_PROGRESS, 0 NOT_STARTED,
  2 BLOCKED_EXTERNAL (live credentials), 1 REFERENCE_PLACEHOLDER.
  Iteration 94 implemented the official Jimeng asynchronous task route (row 146;
  API row 339). Direct `access_key|secret_key` channels use deterministic
  HMAC-SHA256 signing and gateway `sk-` channels use bearer auth; both send the
  official Action/Version query contract through an SSRF-safe, timeout-bound,
  1-MiB-capped client. Submit validation enforces prompt/model, image one-of and
  ~4.7-MiB limits, safe image URLs, 121/241 frames, and v3 model normalization.
  Public task IDs are owner-scoped while upstream IDs remain private; polling
  persists mapped status/result data and serves terminal records without rebilling.
  Per-call decimal pricing bills 121 frames as one unit and 241 as two, rejects
  arbitrary-precision overflow before integer conversion, and atomically reserves
  limited tokens plus wallet/subscription funds before dispatch. Independent
  review found unsafe post-dispatch retries, accepted-task refund/persistence,
  ignored settlement errors, fetch status regression, and pre-auth body allocation;
  all were fixed and regression-tested. Once a submit is dispatched it is never
  retried without an idempotency key; accepted metadata plus user/token settlement
  commit atomically and idempotently, ambiguous outcomes become billed `UNKNOWN`
  tasks, terminal fetch writes cannot regress, and rate limit/auth precede body
  decoding. Focused vet/package/race suites and exact API verification pass; the
  unchanged double full-acceptance gate is pending before row 146 becomes PASS.
- **LIVE_EXTERNAL_PARITY**: BLOCKED_MISSING_CREDENTIALS — no live provider,
  OAuth, WeChat server, or payment sandbox credentials were used
  (ALLOW_LIVE_EXTERNAL_TESTS=false).

## Verified working (with evidence)

- Go module builds: `go build ./...`
- Independent protocol module: `GOWORK=off go build ./...`
- Quota-math saturation invariants: `go test ./common/...`
- Protocol conversion (Claude/Gemini/OpenAI): `go test ./protocolkit/...`
- Routing (priority + weighted): `go test ./service/...`
- Billing quota computation: `go test ./service/...`
- Affiliate invite flow: `go test ./controller/ -run TestRegister`
- WeChat login/bind: `go test ./controller/ -run TestWeChat`
- Role guards: `go test ./middleware/ -run TestRoleGuards`
- SQLite migration + `/api/status` + setup + login + channel/ability/token CRUD
  + relay (non-stream and SSE stream) against a mock upstream (manual smoke test).

## Phase summary

| Phase | Status |
|---|---|
| 0 Forensic inventory | DONE (341 routes, 35 entities, 57 channel types, 38 relay modes, 13 relay formats) |
| 1 Engineering foundation | DONE (backend + protocol module + frontend; all 3 DBs migrate live) |
| 2 Relay & protocol engine | DONE (OpenAI/Claude/Gemini + SSE + WebSocket + per-mode passthrough; task platforms = reference placeholders) |
| 3 Intelligent routing | DONE (priority/weight/retry/model-mapping/ability-cache, rule/fingerprint affinity, multi-key rotation, and health behavior tested) |
| 4 Auth & security | DONE (password/sessions/2FA/OAuth+Telegram+WeChat/passkeys/RBAC/rate-limit/SSRF/origin-guard/email/password-reset; role-guard bypass fixed) |
| 5 Billing & subscriptions | DONE (flat+tiered billing, saturation, wallet, redemption, check-in, subscriptions) |
| 6 Web application | CORE DONE (setup/login/register/OAuth + user console + admin console + 7-language i18n; full ~59-route page inventory not reproduced) |
| 7 Integrations & jobs | DONE (background jobs + leases + Stripe webhook + SMTP; EPay/Creem/Waffo = BLOCKED_EXTERNAL) |
| 8 Deployment & ops | DONE (Docker build + compose + secret scan + amd64/arm64; CI authored) |
