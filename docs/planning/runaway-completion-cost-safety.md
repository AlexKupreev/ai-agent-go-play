# Runaway completion cost-safety plan

**Status:** planned, not implemented
**Incident date:** 2026-08-21
**Affected frontend:** Telegram session turns on `agent serve`
**Affected model:** `gpt-5.1-2025-11-13`

## 1. Executive summary

A Telegram weather conversation triggered one GPT-5.1 Chat Completions response that consumed
exactly 128,000 output tokens, the model's maximum output allowance. The response contained a
128,123-character, truncated `web_search` function argument. It was invalid JSON, so the tool
failed with:

```text
tool error: invalid tool args: unexpected end of JSON input
```

The malformed assistant tool call was nevertheless retained in agent history. Later requests
repeatedly resent that payload, producing 825,589 cumulative input tokens for the turn and severe
latency. The malformed response itself was absent from `run.jsonl`: tool inputs are represented as
`json.RawMessage`, and the logger silently drops a record when JSON marshaling fails.

This was one uncapped model completion inside the deliberate planner/executor/critic workflow.
Planning and critique increase the blast radius, but disabling critique alone cannot prevent
another single-call 128,000-token loss.

Do not re-enable routine Telegram use until the Phase 1 containment work is deployed.

## 2. Incident evidence

The process-wide audit ledger reported:

```json
{
  "day": "2026-08-21",
  "runs": 5,
  "input_tokens": 950403,
  "output_tokens": 136023,
  "cached_tokens": 898176,
  "model_calls": 33
}
```

Per-run usage identified the affected run:

```text
129589  9   825589  c73c67e283f97dd6
3031    11  60922   4a56170113297569
1384    6   36994   7a1f089ebb57ba7c
1133    3   10371   f07f87e92ca75099
886     4   16527   beec0969564d4dfc
```

The initiating message for `c73c67e283f97dd6` was:

```text
Польша, недалеко от Кракова
```

The five transcripts contained 32 logged responses and 8,023 logged output tokens. The audit
contained 33 calls and 136,023 output tokens:

```text
missing calls:          33 - 32        = 1
missing output tokens:  136023 - 8023 = 128000
```

The failed tool result remained in the transcript and exposed the argument size:

```text
128123  web_search  2026-08-21T18:28:13.010760406Z
69      web_fetch   2026-08-21T18:58:27.664710381Z
63      web_search  2026-08-21T18:58:19.88930025Z
61      web_search  2026-08-21T18:58:25.461062387Z
60      web_search  2026-08-21T18:58:22.775871967Z
51      web_search  2026-08-21T18:58:18.27270086Z
```

This proves that one response hit the 128,000-token ceiling while constructing `web_search`
arguments, stopped midway through JSON, and polluted later context.

## 3. Root cause

1. **No per-call completion cap.** `Agent.Run` leaves `StepRequest.MaxTokens` unset, so the
   OpenAI adapter omits an output limit. The adapter also maps a nonzero value to deprecated
   `max_tokens`; GPT-5-family calls need `max_completion_tokens`.
2. **`StopMaxTokens` is ignored.** The adapter maps `finish_reason: length`, but `Agent.Run`
   still processes text and tool calls from that truncated response.
3. **History mutates before validation.** The assistant tool-call message is appended before
   `executeTool` parses its arguments. Invalid and oversized calls therefore survive failures.
4. **Tool arguments use `json.RawMessage`.** Provider arguments are untrusted strings until
   validated. A truncated value makes transcript marshaling fail.
5. **Transcript failures are silent.** `internal/logger/logger.go` ignores marshaling and write
   errors, hiding the response while the usage observer still counts it.
6. **Limits are per agent, not per user turn.** Planner, executor, critic, revisions, and
   sub-agents can each consume independent iterations; there is no shared token/call/time budget.
7. **Network deadlines are inconsistent.** `web_search`, `web_fetch`, and Telegram downloads use
   `http.DefaultClient`, which has no overall timeout. This was not the 128k root cause, but it is
   another unbounded-latency risk.

## 4. Phase 1: emergency containment

Deploy this phase before re-enabling routine Telegram use.

### 4.1 Add role-specific completion limits

Replace or rename provider-neutral `StepRequest.MaxTokens` with `MaxOutputTokens`, and map it to
OpenAI `max_completion_tokens`.

| Role | Proposed maximum completion tokens |
|---|---:|
| planner | 6,144 |
| critic | 3,072 |
| executor/sub-agent | 12,288 |

Expose these limits in `config.json`, effective config, and the `status` tool. Keep the provider
field vendor-neutral; only the OpenAI adapter should know the wire parameter name.

Primary files:

- `internal/provider/types.go`
- `internal/provider/openai/openai.go`
- `internal/agent/agent.go`
- `cmd/config.go`
- `cmd/effective.go`
- `internal/api/config.go`
- `internal/tools/status.go`

### 4.2 Fail closed on output truncation

After emitting usage, check `resp.Stop` before processing text or tools. For `StopMaxTokens`:

1. preserve usage and safe response metadata;
2. do not execute partial tool calls;
3. do not append the response to history;
4. return a typed `model output limit reached` error;
5. let the engine persist `run_usage` for the failed turn.

Do not deliver partial output as a successful answer.

### 4.3 Validate tool calls before history mutation

Before appending any assistant tool-call message:

- require nonempty call IDs and names;
- enforce an argument-byte limit;
- require valid JSON with an object at the top level;
- optionally validate against the selected tool's input schema before dispatch.

Proposed limits:

| Tool class | Maximum argument bytes |
|---|---:|
| `web_search` | 2 KiB |
| `web_fetch` and URL-only tools | 8 KiB |
| shell and ordinary built-ins | 16 KiB |
| code and tool authoring | 64 KiB |

Abort malformed or oversized calls without appending them. Do not create an error tool-result
message that requires retaining the malformed assistant message in the next provider request.

### 4.4 Store tool arguments as strings

Change `provider.ToolCall.Input` from `json.RawMessage` to `string` (or an explicit raw-string
type). Parse at the validation/dispatch boundary. OpenAI also represents function arguments as a
string, so this models the response accurately and makes malformed arguments loggable.

### 4.5 Phase 1 regression test

Use a fake provider response with `StopMaxTokens`, 128 KiB of truncated `web_search` arguments,
and usage containing 128,000 output tokens. Assert:

- exactly one provider call;
- usage retains 128,000 output tokens;
- no tool executes;
- no malformed content enters history;
- no subsequent request occurs;
- the transcript contains safe metadata and the terminal error;
- `run_usage` is recorded for the failed turn.

## 5. Phase 2: turn-wide budgets and deadlines

### 5.1 Share one budget across the orchestration tree

Introduce a concurrency-safe budget shared by planner, executor, critic, every revision, and all
sub-agents. Proposed initial defaults:

```json
{
  "max_model_calls_per_turn": 16,
  "max_output_tokens_per_turn": 32000,
  "turn_timeout_seconds": 600
}
```

Before every request:

1. reserve one call slot;
2. compute remaining output tokens;
3. set the cap to `min(role_cap, remaining_turn_output)`;
4. refuse the call if either budget is exhausted.

Enforce this below the planner/executor split so revisions and sub-agents cannot receive fresh
allowances.

### 5.2 Add a turn deadline

Create a bounded turn context and propagate it through model calls, tools, revisions, and
sub-agents. Return a distinct budget/deadline error. Human approvals need a separate policy:
pause the compute deadline while parked or give approvals their own longer deadline.

### 5.3 Bound HTTP work

Replace `http.DefaultClient` in built-in web operations and Telegram downloads with dedicated
clients:

- 30-second overall timeout;
- bounded response body before parsing;
- cancellation through the turn context;
- no automatic unchanged retries;
- typed timeout/network/status errors.

Primary files include `internal/api/engine.go`, `cmd/deliberate.go`, `cmd/serve.go`,
`internal/agent/agent.go`, sub-agent wiring, `internal/tools/websearch_ddg.go`,
`internal/tools/webfetch.go`, and `internal/frontend/telegram/transport_http.go`.

## 6. Phase 3: lossless observability

### 6.1 Never silently lose a transcript record

Use typed, serializable log records where practical and retain write errors. If a full response
cannot be encoded, write a minimal fallback containing timestamp, model, role, iteration, usage,
stop reason, tool names, argument byte counts, validity flags, and the logging error. Surface the
first transcript failure in final run metadata and server logs.

### 6.2 Attribute usage precisely

Extend provider usage and response events with:

- reasoning-token count;
- stop reason and effective model;
- role (`planner`, `executor`, `critic`, or sub-agent type);
- tool-argument byte count and JSON-validity status.

The incident should have appeared directly as:

```text
role=executor
stop=max_tokens
output_tokens=128000
tool=web_search
argument_bytes=128123
valid_json=false
```

### 6.3 Audit budget termination

Record distinct reasons for per-call caps, malformed/oversized tool arguments, call-budget
exhaustion, output-budget exhaustion, turn deadlines, and HTTP timeouts.

## 7. Test plan

Add or extend tests in:

- `internal/provider/openai/openai_test.go`: `max_completion_tokens`, no deprecated field, and
  reasoning-token details;
- `internal/agent/limits_test.go`: role caps and shared budgets;
- `internal/agent/chat_test.go`: stop on max tokens and reject bad tool calls without mutation;
- `internal/agent/spawn_test.go`: child calls consume the parent budget;
- `internal/logger/logger_test.go`: malformed arguments remain loggable and failures surface;
- `cmd/deliberate_test.go`: all roles and revisions share one budget;
- `internal/api/usage_test.go`: failed capped turns retain exact usage;
- web-tool tests: hanging servers time out and oversized bodies are bounded;
- Telegram/API integration: a failed turn emits one terminal message and releases its lock.

Verification:

```bash
go test ./internal/provider/openai ./internal/agent ./internal/logger ./internal/tools ./internal/api ./cmd
go test -race ./internal/agent ./internal/api ./internal/frontend/telegram
docker build -f deploy/fly/Dockerfile .
```

## 8. Rollout

1. Keep Telegram disabled or restricted until Phase 1 is deployed.
2. Implement and test Phase 1 as the emergency patch.
3. Deploy to Fly and run a constrained, low-cost smoke test.
4. Verify effective config reports every role cap.
5. Ask one weather question and confirm bounded calls, small arguments, matching transcript/audit
   totals, and normal latency.
6. Implement Phase 2 budgets/timeouts and Phase 3 observability.
7. Review OpenAI project/API-key budgets separately; platform alerts do not replace application
   limits.

## 9. Fresh-session handoff

Start a new coding session with:

```text
Implement Phase 1 from docs/planning/runaway-completion-cost-safety.md. Preserve unrelated
worktree changes. Run the targeted provider, agent, logger, API, and cmd tests. Do not deploy.
```

Before editing, inspect `git status`. At the time this plan was written, unrelated user changes
already existed in:

- `docs/planning/flexible-orchestration.md`
- `docs/planning/roadmap.md`

This investigation also added `jq` to the Alpine runtime packages in `deploy/fly/Dockerfile`.
That diagnostic improvement does not implement the cost-safety fix.
