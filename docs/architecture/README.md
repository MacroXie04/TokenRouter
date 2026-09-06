# Architecture

TokenRouter is a Go HTTP gateway with an embedded React dashboard. The root Go
module owns process assembly, HTTP behavior, business rules, provider execution,
and persistence. The nested `protocolkit/` module owns protocol DTOs and pure
conversion logic without depending back on the root module.

This document describes the maintained source layout. The migration records are
[Repository reorganization](../development/reorganization.md) and
[Boundary cleanup](../development/structure-boundaries.md); `../parity/` is
historical compatibility evidence and is not a description of the current tree.

## Runtime assembly

The executable and HTTP composition path is:

```text
cmd/tokenrouter
    -> internal/app
        -> internal/httpapi/router
            -> internal/httpapi/middleware
            -> internal/httpapi/handlers/<business-domain>
        -> web.Dist
```

`cmd/tokenrouter` is intentionally a small process entry point. `internal/app`
owns startup order, runtime initialization, background-job startup, server
configuration, embedded-dashboard mounting, and graceful shutdown. The router
owns route registration; domain handler packages translate HTTP requests and
responses but do not become a second business-service layer.

`app/recovery.go` explicitly connects the Stripe payment resolver and the named
task recovery jobs to the scheduler after runtime initialization. Reassembly
replaces asynchronous callbacks. Node-local journal promotion still runs before
cluster-leased reconciliation; provider imports do not register scheduler jobs.

`web/embed.go` embeds `web/dist` as `web.Dist`. Build the frontend before building
the root Go binary when the embedded distribution has changed.

## Backend ownership

| Package | Owned responsibility | Dependency boundary |
| --- | --- | --- |
| `internal/httpapi/router` | Route groups and handler registration | May compose handlers and middleware; business packages do not import it |
| `internal/httpapi/handlers/*` | HTTP decoding, validation, status codes, and response shapes by domain | Calls business packages and shared HTTP utilities; no catch-all handlers package |
| `internal/httpapi/{middleware,requestctx,dto,pagination}` | Cross-route policy, request-scoped state, shared envelopes and pagination | HTTP-facing only; domain request DTOs stay beside their handlers |
| `internal/auth` | Authentication, authorization, sessions, credentials, passkeys, OAuth, and 2FA | May use the lower business layers shown below |
| `internal/billing` | Pricing, quota, subscriptions, payments, audit delivery, and durable accounting | May use channels, users, settings, and store |
| `internal/channels` | Channel lifecycle, health, selection, affinity, and upstream configuration | May use users, settings, store, relay contracts, and explicit provider adapters |
| `internal/users` | User lifecycle, notification settings, and user-facing account state | May use settings and store |
| `internal/settings` | Typed runtime configuration and persisted option loading | Uses store plus focused leaf validators/calculators |
| `internal/payments/{stripe,creem,waffo}` | Payment-provider HTTP clients, request construction and response validation | Uses billing contracts and transport infrastructure; never imports HTTP handlers |
| `internal/operations` | Cross-domain administration, schedules, leases, and runtime operations | Orchestrates auth, billing, channels, users, settings, and store |
| `internal/catalog` | Model and deployment catalog behavior | Uses domain services and focused provider clients |
| `internal/store` | Database initialization, entities, migrations, hooks, and transaction primitives | Persistence boundary; does not import HTTP composition |
| `internal/platform/*` | Domain-independent clock, cache, crypto, environment, HTTP, logging, mail, and text utilities | Leaf infrastructure |
| `internal/testutil` | Shared database and assertion fixtures | Test files only; production files must not import it |

The dominant business-package direction is:

```text
operations -> {auth, billing, channels, users, settings, store}
auth -> billing -> channels -> users -> settings/store
```

An arrow means the package on the left may coordinate packages to its right.
Focused leaves such as `auth/roles`, `billing/quota`, `billing/expression`, and
`channels/catalog` provide shared policy, math, or vocabulary without reversing
ownership. `operations` is the explicit home for workflows that genuinely cross
several domains; those workflows should not be hidden in the router or in generic
helpers.

Business packages and relay packages must not import `internal/httpapi` or
`internal/app`. In particular, provider correlation fingerprints belong to
`platform/cryptoutil`, not request-context utilities. `platform/mail` owns SMTP
transport configuration and delivery invariants; `settings/smtp.go` decodes
persisted options and `settings/smtp_runtime.go` resolves deployment overrides.
The application injects that resolver when initializing the mailer. Each send
still reads one complete current snapshot, preserving configuration hot reload.

### Persistence cohesion

`internal/store` deliberately remains a cohesive entity-and-migration package.
GORM package globals, model hooks, migration ordering, dialect setup, and
transaction references tie these declarations together. Splitting each entity
behind placeholder repository layers would either introduce cycles, weaken
transaction visibility, or obscure hook and migration registration. Focused
subpackages such as `store/locking` and `store/importer` are used where they own a
real boundary.

## Relay ownership

Relay code is divided by behavior rather than by a generic adapter bucket:

| Package | Owned responsibility |
| --- | --- |
| `internal/relay/contract` | Shared request state, provider contracts, response limits, task/error constants, and wire-level observations |
| `internal/relay/providers/<provider>` | Explicit provider codecs, authentication, request construction, and response translation |
| `internal/relay/engine` | Synchronous relay selection, request execution, streaming, validation, and accounting coordination |
| `internal/relay/tasks` | Durable asynchronous task submission, polling, review, recovery, journaling, and settlement |
| `internal/relay/policy` | Model limits, moderation, and other relay policy decisions |
| `internal/relay/customconfig` | Validated custom-provider configuration |

Provider implementations remain separate because their wire protocols and state
machines differ. Task families remain together in `internal/relay/tasks` as
family-named modules: their recovery, review, journaling, operation, and
settlement paths share a dense private dependency graph. Keeping those internals
private is preferable to manufacturing exports or package cycles solely to create
smaller directories.

The ordinary OpenAI HTTP adapter decodes and validates requests before calling
`engine.Execute`. `contract.RequestState` carries the captured identity, token,
model permissions and authorized groups into engine/task lifecycles. These
packages never import HTTP middleware or request-context helpers. Existing
provider streaming, affinity and native-protocol adapters still use Gin's
transport interfaces; this is not a claim that all relay code is framework-free.

## Frontend and protocol module

The frontend is organized as `web/src/app` for bootstrap, routing, session
coordination, and layouts; `web/src/features` for domain behavior and adjacent
tests; and `web/src/shared` for reusable UI, browser, configuration, and transport
code. The former `web/src/views` and `web/src/lib` trees are not part of the
maintained layout.
`web/src/styles.css` is the stylesheet entry point. Shared element/form styles
live in `shared/ui/base.css`, shell styles in `app/layout/shell.css`, and business
styles beside their features. App composition directly selects feature pages;
there is no cross-domain AdminConsole. Large profile/channel/wallet views keep
coordination and shared mutation ownership separate from their focused panels.
Model metadata and deployment requests have distinct API and contract modules.

Production features consume another feature only through its `index.ts` public
entry point. Shared modules cannot import features/app, and features cannot
import app composition. The frontend lint command enforces these boundaries.

Assembled HTTP tests remain in `httpapi/router` as external-package contract
tests. Database lifetime, dashboard sessions, request helpers and channel-specific
fixtures have separate named files. These fixtures use process-global application
state and remain serial; splitting them does not make them safe for `t.Parallel`.

`protocolkit/` has its own `go.mod` and its own test inventory. The root module
requires it and uses a local `replace` during repository builds. Root packages may
import `github.com/tokenrouter/tokenrouter/protocolkit`; protocolkit must never
import `github.com/tokenrouter/tokenrouter/internal/...` or any other root-module
package.

## Layout and test inventory guard

Run the dependency-free repository guard from the repository root:

```sh
node scripts/verify-repository-layout.mjs
node scripts/verify-repository-layout.mjs --self-test
```

The guard rejects legacy source paths, broken command/web/protocolkit
relationships, protocolkit-to-root imports, and production imports of
`internal/testutil`. It enforces production dependency boundaries: infrastructure
stays below application domains, store/settings use their explicitly permitted
leaves, business/relay code cannot import HTTP adapters, providers cannot import
relay lifecycle orchestration, and HTTP components cannot import the router.
Integration tests may import the complete application graph and are excluded
from production dependency restrictions. It also compares every top-level
`func Test*` declaration in a `*_test.go` file with
`scripts/manifests/go-tests.json`. Root-module selectors
and standalone protocolkit selectors are recorded separately. Functions named
`Test*` in production files are intentionally not tests and are not inventoried.
`TestMain` is also excluded: it is a package lifecycle hook, not a runnable test.

Every explicit `./package:TestName` selector in `.github/workflows/ci.yml` must
resolve to exactly one current test declaration. Missing, newly added, moved, or
duplicate selectors fail the default check. After a deliberate test addition,
removal, or package move, review the change and refresh the manifest explicitly:

```sh
node scripts/verify-repository-layout.mjs --update-manifest
```
