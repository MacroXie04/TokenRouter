# Provenance & License Audit

## Implementation provenance

TokenRouter is an **independent reimplementation** of the functional behavior of
the reference system (an AGPL-3.0 licensed AI API gateway). All source code in
this repository was written from scratch for TokenRouter by Claude (Anthropic)
during this engagement, based on:

1. A read-only forensic inspection of the reference repository (routes,
   entities, domain vocabulary, DTO shapes, and behavioral conventions), and
2. The neutral functional specification captured in `docs/parity/INVENTORY.md`.

No reference source code, comments, or documentation was copied into TokenRouter.
The reference repository was not modified. Publicly-known domain facts that are
not copyrightable expression — provider names, wire-protocol field names,
channel-type enumeration order, and HTTP route shapes dictated by the
OpenAI-compatible API contract — are necessarily reproduced for interoperability.

## License status

Because TokenRouter is original work and does not incorporate AGPL-licensed
reference code, it is **not** an AGPL derivative. TokenRouter is distributed
under the MIT License (see `LICENSE`).

TokenRouter is not, and must not be represented as, the reference project or its
organization. The "TokenRouter" name and identity are independent.

## Third-party dependencies

TokenRouter uses third-party open-source libraries (Gin, GORM, jwt, go-redis,
tiktoken-go, expr-lang, etc.), each under its own permissive license. Their
licenses and notices are recorded in `THIRD-PARTY-LICENSES.md`. No notices
required by these dependencies were removed.

## Legal review note

This provenance record is provided for legal review. If legal review determines
that any portion of TokenRouter is an AGPL derivative, TokenRouter will be
distributed under AGPL-3.0 and all required notices and attribution preserved.
