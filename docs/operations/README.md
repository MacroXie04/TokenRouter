# Runtime and operations

Build with `go build -o tokenrouter ./cmd/tokenrouter` after building `web/dist`.
The Dockerfile performs both builds and retains the same runtime entry point,
port and data directory.

Configuration remains environment-based with optional `.env` loading. See
[`../../.env.example`](../../.env.example). Use a strong `SESSION_SECRET`.
SQLite is the local default; `SQL_DSN` selects MySQL or PostgreSQL. `LOG_SQL_DSN`
may select a separate store including ClickHouse. Optional Redis retains the
existing in-memory fallback behavior.

Initialization ordering lives in `internal/app`: settings publish before
authorization, passkey, routing and pricing consumers. Scheduling uses durable
leases and database time. Billing reservations and provider task-family journals
remain distinct from protocol transport. Do not delete journals or reset
persisted state as part of ordinary maintenance or source reorganization.

Acceptance uses disposable SQLite data and an isolated Docker smoke probe; it
does not deploy the gateway. External-store CI requires disposable databases
and explicit schema-reset authorization. Credential-gated payment/OAuth/provider
validation is not implied by local acceptance; see historical limitations in
`../parity/EXTERNAL_VALIDATION.md`.
