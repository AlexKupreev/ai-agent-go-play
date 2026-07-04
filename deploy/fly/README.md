# Deploying to Fly.io

Run the agent on a small always-on Fly machine, driven from **Telegram**. This
matches the project's deployment model (`docs/design.md` §1, `docs/usage.md`): a
private box for a small trusted group, with the engine unattended and reachable
from a phone.

## How it maps to Fly

- **No public port.** The engine binds to `127.0.0.1` and has **no auth** — the
  Telegram user-id allowlist is the only gate. So these configs expose **no**
  `[http_service]`. The machine is a worker: the Telegram bot long-polls
  `api.telegram.org` *outbound*; nothing reaches in.
- **State on a volume.** `tools.json`, `memory.json`, `audit.jsonl`, sessions, and
  the shell workspace live under a mounted volume (`/data`) so they survive
  restarts and deploys. The stores are single-writer JSON/JSONL — **run exactly one
  machine** (do not `scale count` > 1).
- **Secrets, not baked images.** The OpenAI key and Telegram token come from
  `fly secrets`. The OpenAI key has no in-app env override, so the entrypoint writes
  it into the config dir at boot via `agent config set-key`; the Telegram token and
  allowlist are read from the environment by `serve` directly.

Prerequisites: [`flyctl`](https://fly.io/docs/flyctl/install/), a Fly account, a
Telegram bot token from [@BotFather](https://t.me/BotFather), and your Telegram
numeric user id (e.g. via [@userinfobot](https://t.me/userinfobot)).

Files here:

| File | Purpose |
| --- | --- |
| `Dockerfile` | Multi-stage build; Alpine runtime (bash for the `shell` tool + CA certs). |
| `entrypoint-one.sh` | Single-agent launcher. |
| `entrypoint-two.sh` | Two-agent supervisor (two `serve` processes, one machine). |
| `fly.one-agent.toml` | One agent. |
| `fly.two-agent.toml` | Two independent agents on one machine. |

All commands below are run **from the repo root** (the build context must see the
Go source).

---

## One agent

```bash
# 1. Edit fly.one-agent.toml: set a unique `app` name and your `primary_region`,
#    and fill AI_AGENT_TELEGRAM_ALLOWED_USERS with your Telegram id(s).

# 2. Create the app + a volume for state (same region as primary_region).
fly apps create ai-agent            # or: fly launch --no-deploy (skip if named in toml)
fly volumes create agent_data --region waw --size 1 -a ai-agent

# 3. Set secrets.
fly secrets set OPENAI_API_KEY=sk-... -a ai-agent
fly secrets set AI_AGENT_TELEGRAM_TOKEN=123456:abcdef -a ai-agent

# 4. Deploy.
fly deploy -c deploy/fly/fly.one-agent.toml

# 5. Message your bot on Telegram. /new (or /reset) starts a session; send a task.
#    /end closes it; /reload re-reads prompt/agent-type files without a restart.
```

Watch it come up with `fly logs`. You should see `telegram: bot enabled`. If the
token is missing/rejected you'll see `telegram: … — running without the bot` and
the engine will be idle (localhost-only, unreachable) — that's the fail-safe.

---

## Two agents

Two fully independent agents (separate tools, memory, audit, Telegram bots) on one
machine — the Fly rendering of the docs' "two `serve` processes on one box".

```bash
# 1. Edit fly.two-agent.toml: unique `app`, your region, and the two allowlists
#    (WORK_ALLOWED_USERS / HOME_ALLOWED_USERS).

fly apps create ai-agent-duo
fly volumes create agent_data --region waw --size 1 -a ai-agent-duo

# 2. Secrets: one OpenAI key (shared) + one Telegram token per agent.
fly secrets set OPENAI_API_KEY=sk-... -a ai-agent-duo
fly secrets set WORK_TELEGRAM_TOKEN=111:aaa -a ai-agent-duo
fly secrets set HOME_TELEGRAM_TOKEN=222:bbb -a ai-agent-duo
# Optional separate keys: WORK_OPENAI_API_KEY / HOME_OPENAI_API_KEY

fly deploy -c deploy/fly/fly.two-agent.toml
```

The `work` agent defaults to tier `safe` on `:8080`; `home` to `balanced` on
`:8081`. Ports don't matter externally (both localhost) — each agent is addressed
through its own Telegram bot.

### Alternative: two agents = two apps (stronger isolation)

The single-machine layout above shares one crash domain (if one `serve` dies, Fly
restarts the whole machine). For **independent restart/crash domains**, just deploy
the *one-agent* config twice as two apps:

```bash
# work
fly apps create agent-work
fly volumes create agent_data --region waw --size 1 -a agent-work
fly secrets set OPENAI_API_KEY=sk-... AI_AGENT_TELEGRAM_TOKEN=111:aaa -a agent-work
fly deploy -c deploy/fly/fly.one-agent.toml -a agent-work

# home (repeat with its own app, volume, token, and AGENT_TIER)
fly apps create agent-home
fly volumes create agent_data --region waw --size 1 -a agent-home
fly secrets set OPENAI_API_KEY=sk-... AI_AGENT_TELEGRAM_TOKEN=222:bbb -a agent-home
fly deploy -c deploy/fly/fly.one-agent.toml -a agent-home
```

Two machines, two volumes, two bills — but a crash or redeploy of one never touches
the other. This is closest to the design's "separate OS processes, isolated at every
level" intent.

---

## Admin / operating it

The HTTP API is localhost-only by design. To use the management CLI against a
running engine, SSH into the machine and talk to `127.0.0.1`:

```bash
fly ssh console -a ai-agent
# then, inside the machine:
agent audit  --addr 127.0.0.1:8080            # browse the audit log
agent tool   list --addr 127.0.0.1:8080       # list authored tools
agent client --addr 127.0.0.1:8080 "some task"
# For the two-agent machine, use :8080 (work) or :8081 (home), and set
# AI_AGENT_CONFIG_DIR=/data/work or /data/home for `tool`/`audit` on the local files.
```

Prefer this to exposing the port. If you truly need remote API access, bind `serve`
to the machine's private 6PN address and `fly proxy` over WireGuard — but that trades
the localhost boundary for "trust Fly's private network", and the engine still has no
auth, so only do it in a single-user org.

**Reloading prompts without SSH.** After editing the prompt/agent-type files on the
volume, you no longer need to SSH in to run `agent reload`: send **`/reload`** to the
bot in Telegram (allowlist-gated) and the edits take effect on your next message. SSH is
still the way to *edit* those files on `/data`; `/reload` just applies them live.

## Notes

- **Costs / always-on.** The machine must stay up to long-poll Telegram; there's no
  auto-stop (no services). A `shared-cpu-1x` is enough.
- **Region.** Put the volume in the same region as `primary_region`.
- **Updating.** `fly deploy -c …` rebuilds and rolls the machine; the volume (and
  thus tools/memory/audit) persists across deploys.
- **Egress only.** The agent needs outbound HTTPS to OpenAI, Telegram, and whatever
  `web_fetch`/`web_search` reach. No inbound is required or opened.
