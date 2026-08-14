# Known Deviations

Documented, intentional differences between TokenRouter and the reference
system. These are legitimate design choices, not gaps.

1. **No default root password.** The reference seeds a `root/123456` account.
   TokenRouter does not ship any default credential; the initial root account is
   created through a setup wizard (`POST /api/setup`) with an operator-chosen
   password. This is a security improvement.

2. **Module path and identity.** TokenRouter uses `github.com/tokenrouter/tokenrouter`
   and its own product name/identity; it does not reuse the reference module
   path or branding.

3. **Relay-mode task platforms.** Midjourney/Suno/video submission and polling
   routes return a structured "not configured" result. The reference implements
   these against specific third-party APIs; TokenRouter's equivalents require the
   same external credentials and are pending (tracked as reference placeholders
   for the unimplemented subset). The credential-free Midjourney history and
   SSRF-safe public image proxy are implemented independently.

4. **JSON codec.** TokenRouter uses `jsoniter` for its JSON wrapper (the
   reference may use a different codec); the wrapper API surface is identical
   (`Marshal`/`Unmarshal`/etc.) and all business code routes through it.

5. **Billing price convention.** TokenRouter stores model prices as USD per 1M
   tokens (a single, unambiguous unit) rather than reproducing the reference's
   historical ratio-table conventions. `QuotaPerUnit = 500000` is preserved for
   quota-accounting compatibility. The reference-shaped reset endpoint persists
   a prompt-price-derived `ModelRatio` compatibility map alongside `ModelPrice`,
   but live billing continues to use the USD registry.

6. **ClickHouse log storage is optional.** ClickHouse logging is implemented
   (`model/clickhouse.go`: MergeTree DDL + raw INSERT path, verified against a
   live clickhouse:24.8 container) and activates only when LOG_SQL_DSN points
   at a ClickHouse DSN; the default log path is the primary SQL database. The
   reference behaves the same way.

7. **Frontend i18n key-set scope.** TokenRouter's 7-language i18n covers every
   string in the implemented localized surfaces (57 keys per language, validated
   for completeness). The reference's ~5,266 keys per language reflect its much
   larger UI surface. This is a scope difference, not a missing i18n mechanism.

8. **Settings pages are grouped, not page-for-page.** The reference exposes ~40
   dedicated settings pages; TokenRouter exposes the same option-editing
   behavior through one grouped settings editor (Site / Authentication /
   Moderation / Billing / Model & routing). Every key in the editor is read by
   backend code (machine-verified), and `PUT /api/option` persists any key.
   The 1:1 page inventory is intentionally not reproduced to avoid
   manufacturing near-identical cosmetic files. Related configuration-surface
   differences:
   - **Env-backed config.** Credentials and infrastructure settings the
     reference stores as DB options are environment variables in TokenRouter
     (12-factor choice): SMTP_HOST/PORT/USER/PASSWORD/FROM (↔ SMTPAccount,
     SMTPToken, SMTPServer, SMTPPort, SMTPFrom), STRIPE_WEBHOOK_SECRET (↔
     StripeWebhookSecret), TURNSTILE_SECRET_KEY (↔ TurnstileSecretKey),
     TELEGRAM_BOT_TOKEN (↔ TelegramBotToken), GITHUB/DISCORD/LINUXDO/OIDC
     client credentials, SESSION_SECRET, REDIS_CONN_STRING.
   - **Options tied to placeholder/blocked features not ported.** Remaining
     Midjourney provider-submission settings (the history proxy's
     `MjForwardUrlEnabled` is implemented), Waffo/Creem/EPay payment settings,
     data-export options,
     and site-cosmetics modes (DemoSiteEnabled, ChatLink, SelfUseModeEnabled)
     are not exposed because their features are REFERENCE_PLACEHOLDER /
     BLOCKED_EXTERNAL / intentionally not ported.
   - **Legacy ratio tables are not billing sources.** AudioRatio, ImageRatio,
     CompletionRatio, CacheRatio, CreateCacheRatio, GroupGroupRatio and
     TopupGroupRatio are replaced by the unified ModelPrice / GroupRatio /
     ModelBillingMode / ModelBillingExpr options. `ModelRatio` is retained only
     as the derived compatibility value written by the reference-shaped reset
     endpoint (see deviation 5).
   - **Channel health configuration.** The reference configures auto-disable
     via AutomaticDisableStatusCodes / AutomaticDisableKeywords /
     AutomaticEnableChannelEnabled options; TokenRouter's health-test job
     auto-disables/re-enables per the channel's own auto-ban flag, covering the
     same behavior with a per-channel opt-in instead of global status-code
     ranges.
   - **Not ported (small features, reference-default-inactive or cosmetic).**
     Non-email notify channels (Bark/Gotify/webhook) for the quota reminder
     (the per-user threshold override itself is implemented via
     `PUT /api/user/setting`); ModelRequestRateLimit*;
     EmailDomainRestriction/EmailAliasRestriction; file/image upload/download
     permission controls; StreamCacheQueueLength (stream cache). (The
     MinTopUp/PayAddress/PayMethods payment-page options were ported in
     iteration 78 with the user top-up flow.)

9. **Quota reminder anti-spam cooldown.** The reference sends a low-quota
   notification on every request below the threshold. TokenRouter sends the
   email at most once per hour per user (and only to verified addresses),
   avoiding per-request spam while keeping the same default threshold
   (QuotaRemindThreshold = 1000).

10. **External-platform features not ported.** The reference integrates
    specific third-party platforms that TokenRouter intentionally does not
    reproduce; their routes are marked REFERENCE_PLACEHOLDER in the API
    matrix: the subscription-side EPay endpoints, Creem/Waffo(-pancake)
    payment channels and webhooks, the GPU
    deployments marketplace, the vendors directory, Codex-credit and Ollama
    channel operations, channel upstream-updates sync, ratio_sync, performance
    GC/disk-cache operations, and the reference-specific dashboard billing
    routes. The user-side EPay top-up flow (pay/amount/notify) IS ported
    (iteration 78). TokenRouter covers the same core needs through its own
    surfaces
    (Stripe payments, channel health tests, billing options); porting these
    would mean binding to vendor-specific APIs without their credentials.

11. **Alpha-search billing.** The reference bills `POST /v1/alpha/search`
    through its Responses price-config machinery: one `web_search_preview`
    built-in-tool call recorded in `ResponsesUsageInfo` and settled by
    `PostTextConsumeQuota` (which can apply a configured tool price).
    TokenRouter has no built-in-tool pricing layer; the request settles with
    the standard flat/tiered model pricing on the prompt estimate (the same
    fallback used when an upstream reports no usage). With a plain
    price-per-token model this matches the reference's observable deduction;
    only deployments that configure a separate web_search_preview tool price
    in the reference would differ.

12. **Flat group model for token auto-groups and playground overrides.** The
    reference filters auto-group candidates and `POST /pg/chat/completions`
    group overrides through a group hierarchy (`GroupInUserUsableGroups` with
    per-group auto-group chains). TokenRouter has a flat model — one group per
    user, no group chains — so both surfaces accept any ratio-configured group;
    the user's own group remains usable even when absent from the ratio map.
    Routing consumes `AutoGroupConsume` from a token's stored auto-groups. No
    other endpoint depends on the hierarchy.

13. **Relay key storage includes the `sk-` prefix.** The reference stores the
    raw 48-character key and prepends `sk-` only at display time; TokenRouter
    stores keys as `sk-` + 48 characters. Visible difference: full-key
    disclosure (`POST /api/token/:id/key`, `POST /api/token/batch/keys`,
    `GET /api/usage/token/`) returns the prefixed form, and exact key search
    matches the prefixed form. The masked forms are identical.

14. **Channel-test task notifications are not delivered.** The reference
    `channel_test` system task with `notify: true` sends admin notification
    messages when the sweep finishes; TokenRouter has no message/notification
    delivery subsystem, so the notify flag is stored on the task payload but
    no message is produced. The task itself, its per-channel
    response_time/test_time/auto-ban side effects, and the persisted
    {tested,succeeded,failed,disabled,enabled} result are all real.

15. **Provider-specific balance fetchers are not ported.** The reference
    `update_balance` endpoints have dedicated upstream balance fetchers for
    AIProxy, API2GPT, AIGC2D, SiliconFlow, DeepSeek, OpenRouter and Moonshot;
    TokenRouter implements the OpenAI-shaped dashboard path (subscription +
    usage) and returns the reference's own default "尚未实现" for every other
    provider type, so the route contract matches while per-provider coverage
    is narrower.

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
    terminates. Covered by
    `go test ./service/ -run TestResetSubscriptionQuotaCalendarWalk`.

18. **Insufficient-funding relay errors return 400, not 403.** The reference
    maps both wallet insufficiency (`用户额度不足, 剩余额度: ...`) and
    subscription insufficiency (`订阅额度不足或未配置订阅: ...`) to HTTP 403
    (`service/billing_session.go`, `types.ErrorCodeInsufficientUserQuota`).
    TokenRouter's relay has always used 400 for the wallet case
    (`用户额度不足`), and the subscription funding path follows that house
    convention (`订阅额度不足或未配置订阅: <cause>` with code
    `insufficient_quota`). Message text and error codes match the reference;
    only the status differs, consistently across both funding sources.

20. **GET /api/dashboard/stats is a TokenRouter extension (no reference
    counterpart).** The reference's GET /api/data/ serves the quota
    histogram (GetAllQuotaDates); its frontend has no dashboard-counters
    endpoint. TokenRouter's admin console shows user/token/channel/request
    counters, so the former /api/data handler was moved to
    /api/dashboard/stats (AdminAuth) and the frontend repointed — this keeps
    the reference /api/data contract intact (iteration 81 fixed a false
    matrix PASS caused by trailing-slash normalization matching the old
    route).

21. **SubscriptionPlan.price_amount is stored and serialized as a string.**
    The reference model stores `PriceAmount float64` (decimal(10,6)) and
    serves it as a JSON number; TokenRouter stores `varchar(64)` and serves
    a JSON string ("40.00" vs 40.00). All internal money math parses the
    string through service.ParseSubscriptionPlanPrice, and the subscription
    Stripe order carries the parsed float, so no arithmetic deviates — only
    the wire shape of plan payloads differs. Tracked as a follow-up
    model-type alignment.

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

24. **Jimeng task safety and type-number adaptations.** The reference assigns
    Jimeng channel type 51; TokenRouter's stable independent channel catalog
    assigns it 47, so Jimeng task rows use platform `"47"`. The official
    `POST /jimeng/` wire surface, action/version queries, HMAC signing, gateway
    bearer mode, provider payloads, public task IDs, and OpenAI video responses
    are otherwise preserved. TokenRouter intentionally hardens several edge
    cases: it rejects unsupported actions; accepts only 121- or 241-frame jobs;
    enforces one of base64/images plus per-image (~4.7 MiB), request (16 MiB),
    and provider-response (1 MiB) bounds; and bills 241 frames as two explicit
    per-call units. Only the cheap Action selector runs before bearer auth;
    rate limiting and token auth precede body allocation/JSON parsing. This
    differs from the reference's full pre-auth conversion and fixes its direct
    `CVSync2AsyncGetResult` middleware rewrite, which changes method/path after
    Gin has already selected the submit handler. Finally, provider submission is
    never retried after dispatch because Jimeng exposes no idempotency key.
    Definitive rejections refund; transport-ambiguous outcomes are persisted and
    billed once as `UNKNOWN` with a public task ID, preventing duplicate,
    untracked provider jobs. Accepted metadata and wallet/subscription/user/token
    accounting commit in one idempotent database transaction; settlement failure
    is never reported as successful or silently refunded.
