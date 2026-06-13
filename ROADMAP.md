# Roadmap

Open and future work only. Shipped history lives in [`CHANGELOG.md`](CHANGELOG.md).

Effort key: **S** ≈ a few lines + a test · **M** ≈ a focused change across 2–3 files ·
**L** ≈ a feature or new surface. Cross-references point to
[`architecture/known-risks-and-gaps.md`](architecture/known-risks-and-gaps.md).

## Open items

- **Release binaries** — _M_ — no goreleaser/release workflow exists yet (CI only builds and
  tests). Cross-compile and attach binaries to tags.
- **Bound unbounded table growth** — _M_ — automatic event retention + comments retention. Pruning
  is manual/offline only (`am db prune`); add a server-side scheduler or size/age cap so `events`
  can't grow unbounded without operator action, and give `comments` the pagination/retention that
  only `events` has today.
- **Webhook notifications** — _L_ — outbound webhook on events, gated by an egress allowlist.
- **Copyable `vault_path` in the dashboard** — _S_ — a read-only copy affordance (the field is
  currently editable-only, in the project-edit modal).
- **Scoped export** — _S_ — `am db export -c <category>` to export a single category's data.

## Deferred by design — Security posture for a wider bind

agentman is loopback-only with no auth; the bind **is** the access control, hardened by the
Host allowlist + write-CSRF guard + CSP. The item below is **intentionally not done** and only
matters if the network bind ever widens. (`architecture/security.md`)

- **Remote / multi-user** — _L_ — treat it as an **auth + CSRF + TLS** project, not a feature
  bolt-on: real authentication (the `X-Agent` actor is a spoofable label, not a credential), TLS,
  rate limiting, and per-resource authorization. Until then, these are accepted residuals for the
  stated scope.
