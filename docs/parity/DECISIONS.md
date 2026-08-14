# Decisions Log

| # | Decision | Rationale |
|---|---|---|
| 1 | Module path `github.com/tokenrouter/tokenrouter` | Independent identity; local `replace` for the protocol module |
| 2 | Protocol module named `protocolkit/` (separate go.mod) | Must build standalone (GOWORK=off) with no root-module imports |
| 3 | `relay/common` package for shared adapter types | Breaks the engine↔adapter import cycle |
| 4 | Quota unit `QuotaPerUnit = 500000` | Matches reference accounting so numeric parity holds |
| 5 | Model prices stored as USD/1M tokens | Single unambiguous billing unit; avoids ratio-table ambiguity |
| 6 | `jsoniter` as the JSON wrapper backend | Single codec for performance; wrapper API kept identical |
| 7 | Setup wizard instead of default root password | Security: no shipped credentials |
| 8 | Priority-first then weighted-random channel selection | Matches reference routing semantics; seedable for tests |
| 9 | `clause.Locking{Strength:"UPDATE"}` via `lockForUpdate` | GORM v2 correct locking; SQLite skipped |
| 10 | log/slog for structured logging | stdlib, JSON output, no extra dependency |
