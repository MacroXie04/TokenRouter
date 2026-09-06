# Known Deviations

Documented differences between TokenRouter and the reference system. Some are
intentional hardening or product choices; others are deliberately retained
exact-schema differences. Inclusion here does not silently turn a difference
into PASS: the governing strict matrix records either tested behavior or an
explicit `INTENTIONAL_DEVIATION` rationale.

1. **No default root password.** The reference seeds a `root/123456` account.
   TokenRouter does not ship any default credential; the initial root account is
   created through a setup wizard (`POST /api/setup`) with an operator-chosen
   password. This is a security improvement.

2. **Module path and identity.** TokenRouter uses `github.com/tokenrouter/tokenrouter`
   and its own product name/identity; it does not reuse the reference module
   path or branding.

3. **Relay-mode task-platform gap resolved with stricter durability.** All
   reference task families—Ali, Doubao, Gemini Veo, Hailuo, Jimeng, Kling,
   Midjourney, OpenAI/Sora, Suno, Vertex Veo, and Vidu—now have bounded wire
   codecs, model/channel dispatch, durable accounting, polling/content paths,
   restart recovery, and deterministic provider fixtures. Gemini API and
   Vertex deliberately share one descriptor-driven Veo state machine rather
   than duplicating ledgers. Across the task providers, TokenRouter retains
   stricter encrypted provider identity, reservation-before-I/O, ambiguity
   fencing, no-replay recovery, and root-audited manual-review behavior. The
   provider and billing matrices are complete locally; no credentialed live
   provider exchange is implied.

4. **JSON codec.** TokenRouter uses `jsoniter` for its JSON wrapper. Core
   `Marshal`/`Unmarshal` paths are centralized, but the wrapper surface and some
   type-label/null semantics are not identical to the reference and remain an
   audited compatibility difference.

5. **Billing price namespaces are explicit.** TokenRouter's default model-price
   registry stores USD per 1M prompt and completion tokens. The immutable
   `QuotaPerUnit = 500000` accounting conversion is preserved. A model may
   instead select the separately validated reference-compatibility namespace,
   which uses `PerCallModelPrice`, `ModelRatio`, `CompletionRatio`, and the
   effective user-group-to-routing-group ratio with request-time snapshots.
   Independently priced built-in and custom tool executions use the bounded
   `tool_price_setting.prices` registry and are added to the model charge. The
   reference-shaped reset endpoint still persists a prompt-price-derived
   `ModelRatio` compatibility map alongside `ModelPrice`; it does not make that
   compatibility map the default USD billing source.

6. **ClickHouse log storage is optional.** ClickHouse logging is implemented
   (`model/clickhouse.go`: MergeTree DDL + raw INSERT path) and activates only
   when LOG_SQL_DSN points at a ClickHouse DSN; the default log path is the
   primary SQL database. The recorded 2026-09-05 assembled-tree run of
   `TestClickHouseExternalLogLifecycle` against ClickHouse 24.8 covered
   configurable TTL creation/removal, DDL/migration, raw insertion, stable
   audit-event retry deduplication, and content readback;
   `.github/workflows/ci.yml` now provisions the same ClickHouse version and
   runs that gated test. `LOG_SQL_CLICKHOUSE_TTL_DAYS` now matches the
   reference's optional retention control while safely parsing an integer
   before constructing DDL. A missing, invalid, negative, or out-of-range value
   disables the independent native TTL. Native ClickHouse TTL evaluation uses
   the ClickHouse server clock, while the primary database supplies the
   `created_at` emitted for checked audit logs and every audit-outbox lifecycle
   timestamp, as well as the durable `LOG_RETENTION_DAYS` cleanup cutoff. The
   native TTL therefore defaults to `0`; deployments that require one strict
   retention authority should leave it disabled and use configured log-sink
   cleanup. `LOG_RETENTION_DAYS` accepts 0 through 36500, and `0` disables only
   scheduled cleanup, not root-triggered manual cleanup. The reference also
   treats ClickHouse as optional.

7. **Frontend i18n follows the independently designed target UI.** TokenRouter's
   seven locale files currently have identical validated sets of 2,644 keys. The
   source-aware checker covers all 2,536 runtime keys found across 110 source
   files: 2,019 direct literals and 517 keys resolved through catalogs, imports,
   iteration helpers, and static branches. It rejects duplicates,
   missing/extra/invalid values, copied-English translations, and interpolation
   mismatches. A mechanical key-name comparison with the reference's 5,266-key
   English catalog finds 435 shared keys, 4,831 reference-only keys, and 2,209
   target-only keys. Reference-only strings are not treated as missing behavior
   when their source UI is dormant, unsafe, or intentionally absent; all seven
   locale rows instead require and now have complete coverage of reachable target
   behavior.

8. **Settings implementation is independently consolidated.** The reference
   exposes 40 settings sections; TokenRouter validates the same category/section
   paths and renders them through one feature-scoped, route-aware editor rather
   than duplicating a component per section. Thirty-eight sections expose typed,
   bounded options or purpose-built workflows, including update checks and log
   maintenance. The remaining two are explicit non-editable trust boundaries:
   SSRF protection is enforced by the immutable safe transport, and TokenRouter
   has no delegated worker URL/shared-key trust plane. Generic `PUT /api/option`
   accepts only bounded scalar keys/values, blocks compliance and the immutable
   quota-unit invariants, and routes pricing domains through complete validation
   and atomic publication. Payment, pricing, Waffo Pancake, and channel-affinity
   operations remain mounted only at their canonical paths. The route inventory,
   root guard, keyboard navigation, validation, secret non-redisplay, and
   partial-resource-failure isolation are rendered and tested. Related
   configuration-surface differences:
   - **Option-backed settings with whole-domain environment overrides.** SMTP and
     the GitHub, Discord, LinuxDO, and OIDC clients have coherent database-backed
     option domains; when a deployment selects the corresponding environment
     configuration, the complete domain overrides those options. Telegram uses
     its environment token when present and otherwise falls back to the option.
     Trust/infrastructure secrets such as `STRIPE_WEBHOOK_SECRET`,
     `SESSION_SECRET`, and `REDIS_CONN_STRING` remain deployment-only.
   - **Reference-only knobs without a reachable target workflow.** TokenRouter
     does not expose mutable SSRF or worker-trust controls. Individual dormant
     knobs such as remaining Midjourney submission controls, file/image
     upload/download permissions, and `StreamCacheQueueLength` likewise do not
     create an unimplemented target settings workflow. Implemented sections use
     typed controls; immutable boundaries explain why no mutation is offered.
   - **Compatibility and usage-price tables have explicit consumers.** The
     editor exposes `ModelRatio`, `CompletionRatio`, and `GroupGroupRatio` for
     reference-compatible model billing, plus cache-read, cache-creation,
     image/audio input and output, top-up-group, and tool prices where the target
     runtime consumes them. Default USD model billing remains sourced from
     `ModelPrice`, `GroupRatio`, `ModelBillingMode`, and `ModelBillingExpr`; see
     deviation 5 for the explicit compatibility namespace.
   - **Channel reliability is both global and per-channel.** Validated
     `AutomaticDisableStatusCodes`, `AutomaticDisableKeywords`, and
     `AutomaticEnableChannelEnabled` snapshots govern global transition policy,
     while each channel's auto-ban flag remains the local opt-in.
   - **Small reference-default-inactive controls not ported.** File/image
     upload/download permission controls and `StreamCacheQueueLength` remain
     absent. `ModelRequestRateLimit*`, email domain/alias restrictions, and
     email/webhook/Bark/Gotify notifications are implemented with bounded,
     fail-closed or SSRF-safe behavior as appropriate.

9. **Quota reminder anti-spam cooldown.** The reference sends a low-quota
   notification on every request below the threshold. TokenRouter reserves one
   delivery atomically and sends at most once per hour per user through the
   configured email, webhook, Bark, or Gotify channel; failed delivery releases
   the reservation for retry. Email defaults require a verified account address
   unless a separate valid notification address is configured. This avoids
   per-request spam while keeping the same default threshold
   (`QuotaRemindThreshold = 1000`).

10. **External-platform route gap resolved; live exchanges remain external.**
    The GPU deployment marketplace, Codex-credit operations, Ollama channel
    operations, channel upstream-update synchronization, `ratio_sync`, and the
    reference dashboard-billing routes now have independently implemented,
    bounded contracts and deterministic tests. The EPay, Creem, classic Waffo,
    Waffo Pancake, and Stripe wallet/subscription surfaces are likewise ported;
    the pinned reference exposes no classic-Waffo subscription-initiation
    route. The exact API inventory therefore has no missing implemented
    reference route: all 342 operative routes are `PASS`, and the other 12 are
    genuine reference `RelayNotImplemented` placeholders. Credentialed live
    provider/payment validation remains a separate external boundary and is
    not implied by those route-level results.

11. **Alpha-search tool billing is implemented.** A successful
    `POST /v1/alpha/search` marks one completed `web_search_preview` execution.
    Settlement uses the request-start immutable tool-price snapshot, applies the
    longest matching model prefix (including an explicit zero), multiplies by
    the exact effective group ratio and immutable quota unit, and adds the tool
    surcharge to the ordinary model charge. Invalid or overflowing prices fail
    closed, retry state cannot double-count a prior attempt, and the consume
    audit records a bounded reference-compatible surcharge summary. The built-in
    defaults and operator overrides cover the reference's separately configured
    search-tool price without changing the passthrough response contract.

12. **Flat group model with explicit cross-group usability.** The reference
    filters candidates through recursive group chains. TokenRouter deliberately
    keeps one primary group per user and no recursive hierarchy, but implements
    the observable add/remove usability rules through the validated
    `group_special_usable_group` map. Playground overrides and token groups are
    limited to the user's primary group plus those explicit grants, and every
    selected billing group must have a valid effective ratio. Auto tokens use
    their stored ordered `auto_groups` list or inherit the ordered global
    `AutoGroups` setting. The remaining difference is recursive hierarchy, not
    absence of cross-group authorization.

13. **Relay key storage includes the `sk-` prefix.** The reference stores the
    raw 48-character key and prepends `sk-` only at display time; TokenRouter
    stores keys as `sk-` + 48 characters. Visible difference: full-key
    disclosure (`POST /api/token/:id/key`, `POST /api/token/batch/keys`,
    `GET /api/usage/token/`) returns the prefixed form, and exact key search
    matches the prefixed form. The masked forms are identical.

14. **Channel-test notification gap resolved through account delivery.** A
    `channel_test` task with `notify:true` now sends the reference completion
    notice to an enabled root account through that account's configured email,
    webhook, Bark, or Gotify channel. Delivery failure is isolated from the
    already-persisted task result; `notify:false` performs no delivery. Focused
    and race tests cover both branches, so this is retained only as resolution
    history and is not an outstanding deviation.

15. **Provider-specific and scheduled balance refresh resolved.** The balance
    endpoints implement OpenAI/Custom, AIProxy, API2GPT, AIGC2D, SiliconFlow,
    DeepSeek, OpenRouter, and Moonshot wire contracts plus a validated
    per-channel `balance_url`. Responses and numeric values are bounded,
    transport failures are sanitized, redirects/SSRF are blocked, and balance
    persistence is conditional on an unchanged channel snapshot. The all-channel
    sweep is serialized, cancellable, paged, skips multi-key channels, honors
    per-channel auto-ban, paces requests with bounded `POLLING_INTERVAL`, and
    can run cluster-wide under the periodic-job lease when
    `CHANNEL_UPDATE_FREQUENCY` is configured in minutes. Startup validation and
    focused/race tests cover invalid schedules, overlap, cancellation, provider
    failures, and stale writes. This is no longer an outstanding deviation.

16. **Channel permission layer (resolved in iteration 77).** The reference
    models fine-grained channel permissions (read/operate/write/
    sensitive_write) granted per role with per-user overrides, checked per
    route plus inline in sensitive handlers. TokenRouter now implements the
    same layer: a dedicated sub/obj/act enforcer over the shared casbin_rule
    table, root/admin built-in roles with the admin read/operate/write
    baselines, per-route RequirePermission wiring per the reference table,
    inline sensitive-write guards, the fail-closed update-channel classifier,
    root-only admin_permissions updates via PUT /api/user, and
    admin_permissions payloads. Remaining nuance: TokenRouter reseeds the role
    baselines idempotently on every boot of every node (the reference seeds only
    on master nodes — the resulting policy set is identical). Both admin user
    update and creation now apply the transactional permission-touch semantics.

17. **Subscription quota resets run both lazily and in a periodic job.** The
    reference resets a due subscription only lazily, inside pre-consume
    (`maybeResetUserSubscriptionWithPlanTx`). TokenRouter implements the same
    lazy catch-up walk on its pre-consume path
    (`service/subscription_funding.go`, covered by
    `go test ./service/ -run TestPreConsumeSubscriptionLazyReset`) and
    additionally runs it from the background reset job
    (`ResetDueSubscriptionQuotas`), so a due reset becomes visible on the
    self view without waiting for the next consume. Effective remaining quota
    at consume time is identical. One scheduling-column adaptation: when a
    plan's reset period is `never` but a subscription carries a stale
    `next_reset_time`, the lazy path preserves it exactly like the reference,
    and the job clears it to 0 without zeroing usage so its due-rows query
    terminates. TokenRouter also snapshots the purchased cadence and group
    entitlement, uses database time and a monotonically increasing
    `usage_epoch`, and terminalizes delayed old-window settlements/refunds
    without changing the current window. This is additional safety for
    multi-node reset/payment races. Covered by the reset, entitlement, expiry,
    and usage-epoch suites in `service/subscription_epoch_test.go`.

18. **Insufficient-funding relay errors return 400, not 403.** The reference
    maps both wallet insufficiency (`用户额度不足, 剩余额度: ...`) and
    subscription insufficiency (`订阅额度不足或未配置订阅: ...`) to HTTP 403
    (`service/billing_session.go`, `types.ErrorCodeInsufficientUserQuota`).
    TokenRouter's relay has always used 400 for the wallet case
    (`用户额度不足`), and the subscription funding path follows that house
    convention (`订阅额度不足或未配置订阅: <cause>` with code
    `insufficient_quota`). Message text and error codes match the reference;
    only the status differs, consistently across both funding sources.

19. **Classic Waffo nonterminal notifications remain pending.** The pinned
    reference comment says terminal failures should close an order, but its
    implementation marks every signed status other than `PAY_SUCCESS` failed.
    TokenRouter acknowledges the documented authorization/in-progress states
    without changing the durable order and closes only an exact signed
    `ORDER_CLOSE`; an unknown status receives a signed failed response so Waffo
    can retry rather than silently losing a still-payable order.
    `TestWaffoWebhookSignatureBoundsAndTerminalStatuses` covers both branches.

20. **GET /api/dashboard/stats is a TokenRouter extension (no reference
    counterpart).** The reference's GET /api/data/ serves the quota
    histogram (GetAllQuotaDates); its frontend has no dashboard-counters
    endpoint. TokenRouter's admin console shows user/token/channel/request
    counters, so the former /api/data handler was moved to
    /api/dashboard/stats (AdminAuth) and the frontend repointed — this keeps
    the reference /api/data contract intact (iteration 81 fixed a false
    matrix PASS caused by trailing-slash normalization matching the old
    route).

21. **SubscriptionPlan.price_amount is serialized as a string.** The
    persistent column now matches the reference `decimal(10,6)` contract on
    SQLite, MySQL, and PostgreSQL, including a lossless legacy text migration.
    The reference Go model exposes a `float64` JSON number; TokenRouter keeps a
    six-place decimal string (`"40.000000"` vs `40`) so payment and wallet
    calculations never depend on binary floating-point. Only the plan-payload
    wire type differs; the database type, limits, defaults, and billing value
    are aligned.

22. **Model-metadata writes fail closed on invalid or stale requests.** The
    reference silently succeeds when model update/delete IDs do not exist and
    ignores malformed upstream-sync JSON bodies. TokenRouter preserves the
    reference HTTP-200 business-error envelope but returns `success:false` for
    these cases. Active model/vendor names use a nullable `active_name` unique
    key, backfilled at startup, rather than relying on a nullable
    `(name, deleted_at)` composite; this enforces concurrent uniqueness on
    SQLite, MySQL, and PostgreSQL while retaining soft-deleted name reuse.
    Upstream synchronization also rejects vendor/failure-envelope errors and
    preserves an explicit disabled `status:0`; the reference ignores those
    catalog failures and conflates zero with an omitted status. The locale
    normalizer accepts the intended lowercase `zh-cn` and `zh-tw` forms that
    the reference's case mismatch accidentally skips.

23. **Task-history reads fail closed and normalize legacy JSON.** The reference
    task query helpers discard database errors and may return a successful empty
    page, while its Midjourney filters pass raw timestamp strings to integer
    columns. TokenRouter returns HTTP 500 with `success:false` on query/count or
    username-enrichment failures and parses timestamps before building portable
    queries; malformed timestamps are ignored. Generic task JSON remains stored
    in legacy text columns until a versioned cross-database migration is
    available, but the response DTO emits `properties` and `data` structurally,
    restricts properties to the reference's three public fields, normalizes
    malformed properties to the typed zero-value object, preserves malformed
    legacy `data` as a JSON string, and never serializes `private_data` (only its
    public `result_url` projection). The public image proxy matches the reference
    forwarding option and URL shape but adds a 32 MiB response cap and serves
    non-raster MIME types as attachments; the reference streams unbounded content
    with the upstream type. Valid records retain the reference response shape on
    SQLite, MySQL, and PostgreSQL.

24. **Jimeng task safety hardening.** Jimeng uses the reference channel type
    `51` exactly, including task platform `"51"`; there is no target-specific
    type-number adaptation. The official `POST /jimeng/` wire surface,
    action/version queries, HMAC signing, gateway bearer mode, provider payloads,
    public task IDs, and OpenAI video responses are preserved. TokenRouter
    intentionally hardens several edge cases: it rejects unsupported actions;
    accepts only 121- or 241-frame jobs; enforces one of base64/images plus
    per-image (~4.7 MiB), request (16 MiB), and provider-response (1 MiB) bounds;
    and bills 241 frames as two explicit per-call units. Only the cheap Action
    selector runs before bearer auth; rate limiting and token auth precede body
    allocation/JSON parsing. This differs from the reference's full pre-auth
    conversion and fixes its direct `CVSync2AsyncGetResult` middleware rewrite,
    which changes method/path after Gin has already selected the submit handler.

    Funding holds, token holds, the durable reservation, task row, and
    `jimeng_task_operations` recovery row are created in one transaction.
    Before dispatch, the database records a fenced at-risk marker; the provider
    submission is never retried after that point because Jimeng exposes no
    idempotency key. The primary database is the cluster-visible recovery queue:
    it retains encrypted channel credentials and an encrypted accepted provider
    task ID when needed, uses database-time leases and owner/state fencing, and
    autonomously settles and polls after restart. A bounded encrypted node-local
    journal is only a secondary emergency source when a just-observed outcome
    cannot be written to the primary database; every node promotes its own
    journal before generic recovery. Definitive rejections refund atomically;
    ambiguous dispatched outcomes are retained for recovery/manual review rather
    than redispatched or guessed. Accounting, task state, terminal secret
    scrubbing, and the immutable consume-audit outbox event share transactional
    boundaries. `TestJimengAtomicCreationRollsBackEveryPredispatchBoundary`,
    `TestJimengDispatchingRecoverySurvivesRestartWithoutRedispatchOrGuessingCharge`,
    `TestJimengSettlementAndAuditOutboxShareOneCommitAndReplayOnce`, and the
    MySQL/PostgreSQL `TestJimengExternalDatabaseAtomicLifecycle` gate cover these
    claims.

25. **Audit and Stripe recovery hardening.** Financial and consumption audit
    events have stable IDs and a primary-database outbox. Accounting paths can
    enqueue the immutable event in the same transaction as the charge; the
    pending payload is retained only until delivery, then atomically scrubbed to
    a SHA-256 receipt. Before serving, an indexed batched startup scrub converts
    all legacy delivered payloads while preserving pending delivery payloads;
    every leased periodic audit-delivery pass also scrubs at most 500 legacy
    delivered rows, independently of any delivery error. The receipt retains
    identical-replay idempotency while rejecting event-ID/content collisions.
    DB-time leases, stale-worker fencing, bounded retry state, and sink lookup
    reconcile ambiguous commits without silently losing or inventing an event.
    Receipt-aware and older binaries are not symmetric during a rolling upgrade:
    an old worker can still finalize a delivered row with its raw payload, and an
    old replay path may not interpret a receipt-only row as the original event.
    Operators should therefore coordinate the rollout or drain old workers before
    receipt-aware workers take ownership; periodic scrubbing limits residual raw
    payload lifetime but does not make mixed-version replay compatible.
    The recorded ClickHouse run additionally proves retry deduplication for the
    optional log sink. `TestCheckedAuditWriteFallsBackAndWorkerDeliversExactlyOnce`,
    `TestScrubDeliveredAuditLogPayloadsMigratesLegacyRowsAndPreservesPending`,
    `TestPeriodicAuditDeliveryScrubsLegacyPayloadEvenWhenDeliveryFails`,
    and the MySQL/PostgreSQL `TestAuditOutboxExternalDatabaseLeaseAndDelivery`
    gate cover the delivery boundary.

    Stripe wallet and subscription orders are persisted before the provider
    call with immutable request/economic/entitlement snapshots. A periodic
    reconciler uses DB-time leases and fencing to recover ambiguous checkout
    creation, enforce exact owner/customer/amount/currency binding, release
    expired capacity, and quarantine corrupt or reversal cases for manual review
    rather than guessing an entitlement reversal. This is additional durability
    beyond the reference flow. Local signature/provider fixtures and state-machine
    tests are deterministic; a real Stripe account and webhook exchange were not
    available in this audit, so the billing matrix keeps those live-gateway rows
    `BLOCKED_EXTERNAL`.

26. **Performance maintenance is local and explicitly filesystem-scoped.** The
    six root-only `/api/performance` routes reproduce the reference response
    shapes, retention modes, reset behavior, and synchronous GC action. Unlike
    the reference, TokenRouter does not yet spill relay request bodies into its
    disk cache and its structured logger writes to stderr by default. Disk-cache
    statistics and cleanup therefore observe only files already present in the
    managed `tokenrouter-body-cache` child directory, while log listing and
    cleanup are enabled only when `TOKENROUTER_LOG_DIR` or `LOG_DIR` explicitly
    selects a directory. Both maintenance paths use bounded, root-anchored,
    regular-file-only scans; reject traversal and directory symlinks; never
    recurse; and preflight entry limits before deletion. The route and failure
    boundaries are covered by the `TestPerformance*` controller suite.

27. **OpenAI/Sora video tasks use stricter durable recovery.** The six public
    OpenAI video routes and the reference duration/resolution price factors are
    preserved, including JSON and multipart creation, remix, polling, and
    content retrieval. TokenRouter additionally bounds bodies, multipart parts,
    provider responses, and content; rejects duplicate scalar fields and
    conflicting provider IDs; requires HTTPS provider bases; and serves only
    safe content types with private caching. OpenAI-compatible channel type `1`
    and Sora channel type `55` retain their exact persisted platform values,
    while the private `openai-video-v1:` envelope prevents unrelated tasks on a
    shared platform from entering this recovery path.

    Funding and limited-token holds, the task, and the provider-neutral
    `task_operations` row are created before network dispatch. Accepted provider
    IDs and channel credential snapshots use task/accounting-bound AES-GCM. The
    database is the cluster recovery queue; a bounded encrypted node-local
    journal closes the accepted-response/database-outage window without
    redispatching. A settled unknown row can be reopened only when its immutable
    reservation, task, operation, private accounting metadata, and consume audit
    all agree, and that transition never changes quota. Terminal provider
    failure reverses the settled charge exactly once. Exhausted polling moves to
    a root-only, secret-free manual-review workflow whose retry is atomically
    audited and leaves settled accounting unchanged. These hardenings are
    covered by the `TestVideoTask*`, `TestVideoRecoveryJournal*`,
    `TestRetryVideoTaskManualReview*`, Sora client, route, and TaskOperation
    accounting suites.
