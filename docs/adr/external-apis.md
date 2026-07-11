# External API services — embed, author, or plugin? (+ secret injection)

How the agent should reach a **third-party HTTP API that needs a key** — ScrapingAnt (JS-rendered
scraping) as the motivating case, but the decision generalizes to any keyed external service. The
question posed: *embed it, or distribute it as a plugin?* The answer turns on a primitive the system
doesn't have yet — **secret injection into brokered capabilities** — so this ADR settles both.

Companion to [`../security.md`](../security.md) (the capability broker + tiers this builds on),
[`../tools.md`](../tools.md) (`author_tool`, the self-extension path), and
[`../design.md`](../design.md) §1 (single static binary, one trusted box — the constraint that rules
plugins out).

**Status: the secret-injection primitive (§2) is BUILT.** The `Capability` carries `secret`/
`secret_in`; the broker resolves the value from config (`secrets`) and injects it into an
`http_get` host-side (header or query param), bounded to the cap's host allowlist — never entering
the sandbox, the tool source, or the audit log (only the name is recorded). Secret-bearing caps
always require operator approval (even on permissive). Managed with `agent config
set-secret`/`rm-secret`/`secrets`. Authored-tool integrations (§1B) work through it now. **Not
built:** the ScrapingAnt-specific `web_fetch` enhancement (§1A) — with §2 in place it is a thin
optional add whenever wanted; and any plugin/MCP path (§1C, deliberately deferred).

---

## 0. Current state (traced in code)

- **Brokered HTTP already exists.** `capability.Capability{Kind: "http_get", Hosts: […]}` +
  `Broker.HTTPGet` (`internal/capability`) let an authored Lua tool fetch a host on its allowlist,
  capped at `max_http_bytes`, every call audited. `web_fetch` / `web_search` are built-ins exposed to
  the sandbox.
- **No secret plumbing.** A `Capability` carries `Hosts`/`PathPrefix`/`Tools` — **no notion of a
  credential.** An authored tool that needs an API key has nowhere to get one except **hardcoding it
  into the Lua source**, which then lands in the tool catalog (`tools.json`) and every audit/transcript
  in cleartext. This is the blocker for the "just author a scrape tool" answer.
- **No plugin ABI.** There is no dynamic-load / subprocess / out-of-process extension mechanism, and
  by design: the build is a single `CGO_ENABLED=0` static binary for a low-resource trusted box
  (design §1, §11). Config keys (`openai_key`, `telegram_token`) are the established pattern for
  "external service credential."

---

## 1. The three shapes, weighed

| Option | What it is | Verdict |
|---|---|---|
| **A. Config-gated built-in** | A `scrapingant_key` config; when set, `web_fetch` routes JS-heavy fetches through ScrapingAnt, else direct. Key handled host-side, never model-visible. | **Chosen for the concrete integration.** Consistent with `openai_key`/`telegram_token`; simplest; key never touches model surface. |
| **B. Authored tool over `http_get`** | The agent authors a `scrape(url)` tool at runtime hitting `api.scrapingant.com`. The self-extension system's whole point. | **Enabled by, and gated on, secret injection (§2).** The right path for *arbitrary* future APIs the operator hasn't pre-built — but useless (or unsafe) until a tool can reference a secret it can't read. |
| **C. Distributed plugin** | A separate `.so` / subprocess / MCP server providing the integration. | **Rejected for now.** Contradicts the single-static-binary constraint (design §1); heavy infrastructure for one scraper. If a real distributed-extension need ever appears, **MCP (client-side, over the existing OpenAI-compatible stack)** is the shape to reach for — a Phase-5-scale decision, not this. |

**So: neither "embedded" nor "plugin" as a binary either/or.** ScrapingAnt specifically ships as a
config-gated enhancement to `web_fetch` (A). The *general* answer to "an external keyed API" is an
authored tool (B) — which requires the secret primitive below. A plugin system (C) is explicitly not
built.

---

## 2. Secret injection — the missing primitive (unblocks B, and A cleanly)  *(BUILT)*

The reusable thing to build. Give a capability a **named secret reference** the broker resolves
host-side and injects at call time; the script names the secret but never sees its value.

- **Config:** a `secrets` map in `config.json` (`{"scrapingant": "…", "…": "…"}`), set via a new
  `agent config set-secret <name> <value>` (writing the same file `openai_key` lives in — same trust
  boundary, already as sensitive as the API key). File-mode `0600` as today.
- **Capability shape:** extend `Capability` with an optional `Secret string` (a *name*, not a value)
  and, for HTTP, a placement (`header:Authorization` / `header:x-api-key` / `query:token`). The broker,
  on `HTTPGet`, looks up `secrets[name]` and sets the header/param — **the value never enters the Lua
  `LState`, the tool source, `tools.json`, or the audit log** (the audit records `secret:scrapingant
  used`, not its value).
- **Approval:** granting a tool a `Secret`-bearing capability is a tier-gated escalation like any
  network cap — the human approves "may use secret `scrapingant` against `api.scrapingant.com`" at
  authoring time.

This keeps the invariant the sandbox is built on — *scripts get capabilities, not credentials* — and
generalizes: the next keyed API is `set-secret` + an authored tool, no code change.

---

## 3. Decision

1. **Build secret injection (§2)** — the general, reusable primitive; the highest-leverage piece.
2. **Ship ScrapingAnt as a config-gated `web_fetch` enhancement (§1A)** — thin, immediate value,
   independent of the model authoring anything.
3. **Authored-tool integrations (§1B)** then fall out for free for any *other* external API, using
   the §2 primitive — no per-API code.
4. **No plugin system (§1C)** — revisit only as MCP, only on a concrete distributed-extension need.

---

## 4. Open questions

- **Secret rotation / listing** — `config set-secret` implies a `config rm-secret` / `secrets` list
  (names only, never values), mirroring the engine-alias commands. Add with the primitive.
- **Non-HTTP secrets** — the placement enum (§2) assumes HTTP header/query. Fine for now; a secret
  consumed by a future non-HTTP capability is a later generalization.
- **ScrapingAnt fallback policy** — route *all* fetches through it, or only on JS-render failure /
  an explicit `render_js` arg? Lean: opt-in per call (an arg on `web_fetch`), so the default stays the
  free direct fetch and the paid API is deliberate.
- **Built-in vs authored for ScrapingAnt specifically** — A is chosen for ergonomics, but once §2
  exists, ScrapingAnt *could* instead be a shipped authored tool. Keep it a built-in: `web_fetch`
  augmentation is lower friction than a catalog entry the operator must not revoke.
