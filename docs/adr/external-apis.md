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
set-secret`/`rm-secret`/`secrets`. Authored-tool integrations (§1B) work through it now.

**ScrapingAnt is BUILT, but as a separate `scrape` tool — not the `web_fetch` enhancement §1A
proposed. See §5 for why that decision was reversed.** Still not built: any plugin/MCP path
(§1C, deliberately deferred).

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

- **Config:** a `secrets` map in `config.json` (`{"scrapingant": "…", "…": "…"}`), set via
  `agent config set-secret <name> <value>` (writing the same file `openai_key` lives in — same trust
  boundary, already as sensitive as the API key). File-mode `0600`. **Deployment:** each secret is
  *also* overridable from the environment as `AI_AGENT_SECRET_<NAME>` (lowercased; env wins over
  config), so an automated deploy injects it via the platform's secret store — `fly secrets set
  AI_AGENT_SECRET_SCRAPINGANT=…` — with no boot-time write and nothing on the state volume (the
  12-factor path, matching how the Telegram token is provided).
- **Capability shape:** `Capability` carries an optional `Secret` (a *name*, not a value) and a
  placement `SecretIn`: `header:<Name>`, `query:<param>`, or `bearer` (shorthand for
  `Authorization: Bearer <token>`, so the stored secret is the raw token). The broker, on `HTTPGet`,
  looks up the secret and sets the header/param — **the value never enters the Lua `LState`, the tool
  source, `tools.json`, or the audit log** (the audit records `[secret:scrapingant]`, not the value),
  and it is bounded to the cap's host allowlist (+ the redirect re-validation), so it can't be
  steered off the approved host. Fail-closed: a named-but-unresolvable secret denies the call.
- **Approval:** a `Secret`-bearing capability **always** requires human approval — even on
  `permissive`, since the host+secret grant is the one place to catch a credential aimed at the wrong
  host. The human approves "http_get → api.scrapingant.com (secret "scrapingant" in header:x-api-key)"
  at authoring time.

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
  *(Resolved: built-in, but its own tool — §5.)*

---

## 5. Amendment — ScrapingAnt ships as `scrape`, not as a `web_fetch` flag

§1A chose to fold ScrapingAnt into `web_fetch` behind a `render_js` argument. **That is reversed:
it ships as a separate built-in `scrape` tool** (`internal/tools/scrape.go`). Still a built-in, as
§1A decided and §4 confirmed — only the *surface* changed. Four reasons, of which the first is
decisive:

1. **Cost visibility.** `web_fetch` is free and the model reaches for it constantly; ScrapingAnt
   bills per call. An argument on a free tool lets the model spend credits by flipping a boolean
   it half-understands, indistinguishable in the transcript from an ordinary fetch. A separate
   tool name makes each paid call a deliberate choice and one auditable line
   (`capability=scrape`, host + `[browser]`, never the key).
2. **The contracts differ.** ScrapingAnt has real options worth exposing — browser rendering,
   `proxy_country`, `wait_for_selector`, HTML vs text. Hanging them off `web_fetch` inflates a
   description paid for in context tokens on *every* run, including the overwhelming majority of
   fetches that are a plain GET.
3. **The failure modes are unrelated.** 403 bad key, 409 concurrency, 423 target-blocked, 429 out
   of credits — each with different retry advice — versus `web_fetch`'s "the page didn't load".
   Merged, both get harder for the model to recover from and for the operator to debug.
4. **It dissolves §4's open question.** "Route everything through it, or only on JS-render
   failure, or an explicit arg?" is a policy question that only exists because of the merge. Two
   tools, and the model simply picks; the tool description carries the policy ("try `web_fetch`
   first; don't retry in a loop").

**Two properties the implementation holds to.** The key is read from the §2 secret store under the
name `scrapingant` rather than a bespoke `scrapingant_key` config field, so `set-secret`, the
`AI_AGENT_SECRET_*` deployment path, and `config secrets` all work unchanged, and there is one
place credentials live. And the tool is **registered only when that secret resolves** — a paid tool
the model can only ever fail with wastes turns discovering that.

Note the asymmetry with §1B this creates, and it is intentional: `scrape` is a *trusted built-in*
that reads the secret directly host-side, so it needs no capability grant and no approval prompt —
the operator authorized it by storing the token. An *authored* tool reaching the same API goes
through the broker, names the secret in a capability, and always prompts. Both are correct: the
built-in's host and behavior are fixed at compile time and reviewable; an authored tool's are
chosen by the model at runtime, which is exactly what the approval catches.
