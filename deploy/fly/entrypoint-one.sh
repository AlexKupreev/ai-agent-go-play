#!/usr/bin/env bash
# Single-agent launcher for Fly.
#
# The OpenAI key has no in-app env override (it is only read from config.json), so
# we write it from the OPENAI_API_KEY secret into the config dir on the mounted
# volume. The Telegram token + allowlist ARE read from the environment by `serve`
# directly (AI_AGENT_TELEGRAM_TOKEN / AI_AGENT_TELEGRAM_ALLOWED_USERS), so they
# just need to be set as Fly secrets/env — nothing to do here.
set -euo pipefail

: "${OPENAI_API_KEY:?set it once: fly secrets set OPENAI_API_KEY=sk-...}"

# All persistent state lives on the volume (default mount /data). config/tools/
# audit under the config dir; per-run transcripts under the sessions dir; memory
# + spaces under the workspace's .agent/ (we cd into WORKDIR below, so they land
# on the volume too — docs/adr/spaces.md).
export AI_AGENT_CONFIG_DIR="${AI_AGENT_CONFIG_DIR:-/data/config}"
export AI_AGENT_SESSIONS_DIR="${AI_AGENT_SESSIONS_DIR:-/data/sessions}"
WORKDIR="${AGENT_WORKDIR:-/data/workspace}" # cwd for the shell tool + memory/spaces home; persisted

mkdir -p "$AI_AGENT_CONFIG_DIR" "$AI_AGENT_SESSIONS_DIR" "$WORKDIR"

# Persist the key into the config dir (idempotent; merges, preserving model/tier).
agent config set-key "$OPENAI_API_KEY" >/dev/null

if [ -z "${AI_AGENT_TELEGRAM_TOKEN:-}" ]; then
  echo "WARNING: AI_AGENT_TELEGRAM_TOKEN is unset — the bot is disabled and the" >&2
  echo "         localhost engine is unreachable. Set it: fly secrets set AI_AGENT_TELEGRAM_TOKEN=..." >&2
fi

cd "$WORKDIR"

# Bind to localhost only: the engine has no auth; the Telegram allowlist is the
# gate, and the bot reaches the engine over 127.0.0.1. Never expose this port.
exec agent serve --addr 127.0.0.1:8080 --tier "${AGENT_TIER:-balanced}"
