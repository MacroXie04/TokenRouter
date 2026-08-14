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

3. **Relay-mode task platforms.** Midjourney/Suno/video task routes return a
   structured "not configured" result. The reference implements these against
   specific third-party APIs; TokenRouter's equivalents require the same
   external credentials and are pending (tracked as reference placeholders for
   the unimplemented subset).

4. **JSON codec.** TokenRouter uses `jsoniter` for its JSON wrapper (the
   reference may use a different codec); the wrapper API surface is identical
   (`Marshal`/`Unmarshal`/etc.) and all business code routes through it.

5. **Billing price convention.** TokenRouter stores model prices as USD per 1M
   tokens (a single, unambiguous unit) rather than reproducing the reference's
   historical ratio-table conventions. `QuotaPerUnit = 500000` is preserved for
   quota-accounting compatibility.

6. **No ClickHouse driver dependency by default.** ClickHouse log storage is
   stubbed; the primary SQLite/MySQL/PostgreSQL log path is fully implemented.

7. **Frontend i18n key-set scope.** TokenRouter's 7-language i18n covers the
   strings in the implemented frontend (~45 keys per language, validated for
   completeness). The reference's ~5,266 keys per language reflect its much
   larger UI surface, which TokenRouter does not yet fully reproduce. This is a
   scope difference, not a missing i18n mechanism.

8. **Settings pages are grouped, not page-for-page.** The reference exposes ~40
   dedicated settings pages (site, auth, billing, model, security, console, ops),
   each a form editing a subset of options. TokenRouter exposes the same
   option-editing behavior through one grouped settings editor (Site /
   Authentication / Billing / Model & routing categories + a generic editor).
   Behavioral parity is complete; the 1:1 page inventory is intentionally not
   reproduced, to avoid manufacturing near-identical cosmetic files.
