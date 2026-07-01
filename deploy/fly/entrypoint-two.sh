#!/usr/bin/env bash
# Two-agent launcher for Fly — mirrors the docs' "two serve processes on one box,
# each with its own config dir + port" model (docs/usage.md → Running multiple
# independent agents). The two agents share NOTHING: separate config dirs (tools,
# memory, audit), separate sessions, separate Telegram bots.
#
# Each agent is driven by its own Telegram bot, so each needs its own token:
#   fly secrets set WORK_TELEGRAM_TOKEN=... HOME_TELEGRAM_TOKEN=...
# The OpenAI key is shared by default (OPENAI_API_KEY); override per agent with
# WORK_OPENAI_API_KEY / HOME_OPENAI_API_KEY if you want separate billing/keys.
#
# Trade-off vs. two separate Fly apps: this is one machine and one crash domain —
# if either `serve` exits, the container goes down and Fly restarts the whole
# machine (both agents). For full isolation (independent restart/crash domains),
# deploy the one-agent config twice instead — see deploy/fly/README.md.
set -euo pipefail

: "${OPENAI_API_KEY:?set it: fly secrets set OPENAI_API_KEY=sk-...}"

# run_agent NAME ADDR TIER TOKEN ALLOWED KEY
# Starts one `serve` in the background with its own volume-backed state dirs.
run_agent() {
  local name=$1 addr=$2 tier=$3 token=$4 allowed=$5 key=$6
  local base="/data/$name"

  mkdir -p "$base/config" "$base/sessions" "$base/workspace"

  # Each agent's OpenAI key lives in its own config dir.
  AI_AGENT_CONFIG_DIR="$base/config" agent config set-key "$key" >/dev/null

  if [ -z "$token" ]; then
    echo "WARNING: no Telegram token for agent '$name' — its bot is disabled (engine unreachable)." >&2
  fi

  # Export per-agent config so this `serve` (and only it) picks it up. The child
  # captures these at fork time, so the next run_agent overwriting them is safe.
  (
    export AI_AGENT_CONFIG_DIR="$base/config"
    export AI_AGENT_SESSIONS_DIR="$base/sessions"
    export AI_AGENT_TELEGRAM_TOKEN="$token"
    export AI_AGENT_TELEGRAM_ALLOWED_USERS="$allowed"
    cd "$base/workspace"
    exec agent serve --addr "$addr" --tier "$tier"
  ) &
}

# Defaults mirror the docs' example: a conservative "work" agent (safe) and a
# looser "home" agent (balanced), on two localhost ports.
run_agent work 127.0.0.1:8080 "${WORK_TIER:-safe}" \
  "${WORK_TELEGRAM_TOKEN:-}" "${WORK_ALLOWED_USERS:-}" "${WORK_OPENAI_API_KEY:-$OPENAI_API_KEY}"

run_agent home 127.0.0.1:8081 "${HOME_TIER:-balanced}" \
  "${HOME_TELEGRAM_TOKEN:-}" "${HOME_ALLOWED_USERS:-}" "${HOME_OPENAI_API_KEY:-$OPENAI_API_KEY}"

# If either serve exits, tear the whole container down so Fly restarts it cleanly
# (rather than limping along with one agent silently dead).
wait -n
echo "an agent process exited — shutting down so Fly restarts the machine" >&2
kill 0
exit 1
