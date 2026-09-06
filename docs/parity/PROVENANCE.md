# Provenance and License Record

Last reviewed: 2026-09-06

## Audited scope

TokenRouter is maintained as an independent implementation of an AI gateway.
During this parity audit:

- `/Users/hongzhe/Code/TokenRouter` was the writable target;
- the reference checkout
  `/Users/hongzhe/Code/new-api@ccd535ef8e50cf6e5846a59278c40b7ff59d1b7d`
  was read-only;
- implementation decisions were derived from observable behavior, route and
  entity inventories, protocol contracts, and independently written tests;
- the reference checkout was not modified, executed against production data, or
  contacted as a network service.

The audit process did not intentionally copy reference source, comments, or
documentation into TokenRouter. Interoperability facts such as standardized
provider field names, HTTP paths, status values, and persisted identifiers may
necessarily coincide. This record describes the process used in this audit; it
is not a file-by-file authorship certification for every pre-existing line in
the repository.

## Repository and dependency licenses

The target repository currently contains an MIT `LICENSE` file. Third-party
packages remain governed by their own licenses and notices.
`THIRD-PARTY-LICENSES.md` is only a partial notice list: it does not yet cover
every current direct backend dependency, frontend dependency, or transitive
package and must not be treated as a release-complete SBOM or notice bundle.

Whether any implementation is a derivative work is a legal conclusion, not a
build or source-scanning result. This document therefore does not assert that
the MIT file alone resolves all obligations arising from earlier contributions
or reference access. Before public distribution, a qualified reviewer should
confirm contribution provenance, dependency notices, trademarks, and any
applicable copyleft obligations.

TokenRouter must not be represented as the reference project or its
organization.

## Reproducibility and chain of custody

The audited source snapshot is based on target Git revision
`a88784d16269d86f1c37298b5a185e6cfe7fccd0` and contains 1,141 tracked or
non-ignored paths, including this record. Because the working tree is
intentionally dirty, the base revision alone does not identify the snapshot.

The reproducible snapshot fingerprint is:

`7f741e5b5a1e3284b458f7aa5920443e3135e99dbe24afb661ac6519a77f4585`

It is SHA-256 over the lexicographically sorted 1,140-path source set excluding
this self-referential file. For each path, the digest input is the UTF-8 path,
one NUL byte, and the raw SHA-256 digest of the file contents. The source list
is produced by:

```text
git ls-files -z --cached --others --exclude-standard -- . \
  ':(exclude)docs/parity/PROVENANCE.md'
```

The complete acceptance gate passed first in the assembled working tree and
again in a clean export containing exactly every tracked and non-ignored path.
The clean directory had a newly initialized Git index and no copied ignored
dependencies, frontend build, acceptance artifacts, or developer environment;
the gate recreated all required generated state and finished with `ACCEPTANCE:
ALL CHECKS PASSED`.

The current Git HEAD still does not contain all audited paths. Before a release
is cut, the intended changes must be reviewed and placed under version control;
that action was outside this audit's authorization.

No commit, push, persistent or external deployment, paid-provider request, or
production-data operation was performed as part of this audit. Docker resolved
its public base-image metadata during the build; every built application image,
database, and process used for validation was disposable or an existing local
test fixture, and no application endpoint was published externally.
