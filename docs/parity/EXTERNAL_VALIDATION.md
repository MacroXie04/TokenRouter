# External Validation

Status of validation against live external systems.

## Live providers (OpenAI, Anthropic, Gemini, ...)

**BLOCKED_MISSING_CREDENTIALS** — no live provider credentials are available in
this environment and `ALLOW_LIVE_EXTERNAL_TESTS=false`. Provider behavior was
validated against a local mock upstream (an OpenAI-compatible HTTP server) that
exercises non-stream and SSE-stream responses, usage extraction, and settlement.

## OAuth applications

**BLOCKED_MISSING_CREDENTIALS** — no OAuth client credentials configured.

## Payments (Stripe, EPay, Creem, Waffo)

**BLOCKED_MISSING_CREDENTIALS** — no sandbox keys configured; no live calls made.

## Reference runtime comparison

The reference system was inspected read-only. A side-by-side normalized behavior
comparison (golden request/response fixtures through both runtimes) has not been
run because it requires the reference runtime to be executed, which is out of
scope for this environment. Protocol DTOs were reconciled against the reference's
OpenAPI/DTO inventory instead.
