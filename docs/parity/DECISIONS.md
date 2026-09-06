# Decisions Log

| # | Decision | Rationale |
|---|---|---|
| 1 | Module path `github.com/tokenrouter/tokenrouter` | Independent identity; local `replace` for the protocol module |
| 2 | Protocol module named `protocolkit/` (separate go.mod) | Must build standalone (GOWORK=off) with no root-module imports |
| 3 | `relay/common` package for shared adapter types | Breaks the engine↔adapter import cycle |
| 4 | Quota unit `QuotaPerUnit = 500000` | Preserves the reference-compatible quota scale; exact pricing behavior is graded separately in the billing matrix |
| 5 | Model prices stored as USD/1M tokens | Single unambiguous billing unit; avoids ratio-table ambiguity |
| 6 | `jsoniter` as the JSON wrapper backend | Centralizes target JSON handling; known wrapper-surface and type-label differences remain explicitly audited |
| 7 | Setup wizard instead of default root password | Security: no shipped credentials |
| 8 | Priority-first then weighted-random channel selection | Matches reference routing semantics; seedable for tests |
| 9 | `clause.Locking{Strength:"UPDATE"}` via `lockForUpdate` | GORM v2 correct locking; SQLite skipped |
| 10 | log/slog for structured logging | stdlib, JSON output, no extra dependency |
| 11 | Checked secure entropy for every security-sensitive issuance | Failure occurs before returning or persisting a credential; fallback is restricted to explicitly non-secret correlation values |
| 12 | Durable operation journals with database-time leases and fencing | Restarts and ambiguous commits are reconciled without duplicate provider dispatch or guessed settlement |
| 13 | Provider registry owns native/compatible dispatch semantics | A native request reaches only a profile that explicitly supports its wire contract, authentication, URL, and conversion |
| 14 | Strict domain matrices govern completeness | The CSV ledger records implementation milestones; it cannot override a missing route, provider contract, rendered workflow, or executable boundary, while consciously retained and tested schema differences remain explicit `INTENTIONAL_DEVIATION` rows rather than false PASSes or unfinished work |
