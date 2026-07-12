# Vision — letting the agent see an image

The agent can already *receive* an image: a Telegram photo (or any image document) uploads, is
sanitized, stored in the session scratch dir, and recorded in the artifact manifest with
`origin: user` ([`file-upload.md`](file-upload.md)). What it cannot do is **look at it** — the model
surface is text-only, so today `uploadTurnText` honestly tells the agent it cannot see image content
rather than letting it invent a description.

This doc scopes closing that gap. **Status: proposal, not built.** The recommendation is §4 —
**vision as a tool** (a `view_image` built-in that makes a one-shot side call), *not* images in the
conversation, which is a much larger commitment for a benefit this deployment doesn't yet need (§5).

Companion to [`file-upload.md`](file-upload.md) (how the bytes get here) and
[`chat-planner.md`](../adr/chat-planner.md) §D3 (references travel, not payloads — the principle
this proposal follows).

---

## 0. Current state (traced in code)

- **`provider.ContentBlock`** (`internal/provider/types.go:48`) has exactly three kinds: `text`,
  `tool_call`, `tool_result`. There is no image variant, so **nothing but text can reach a model**.
- **The OpenAI adapter** (`internal/provider/openai/openai.go:80`) maps a user message with
  `oai.UserMessage(textOf(m))` — it flattens the message's blocks to a **string**. This one line is
  the whole wire-level obstacle. The adapter is the only file in the tree that imports the OpenAI
  SDK, and `gpt-4o-mini` (the built-in default model) is already a vision model.
- **A turn is text.** `PostTurn(sessionID, text string)` (`internal/api/engine.go`) and the
  `TurnRunner` interface carry a string. A frontend has no channel to hand the engine anything else —
  which is why an upload's reference travels *inside the turn text* today
  ([`file-upload.md`](file-upload.md) §3 D3).
- **A tool result is text.** `tools.Tool.Run` returns `(string, error)` (`internal/tools/tools.go:19`).
- **`internal/tools` already imports `internal/provider`** (`usage.go:8`), so a tool may hold a
  `provider.Provider` with **no import cycle**. (Contrast `spawn_agent`, which had to live in
  `internal/agent` because it needs a `*Agent`.)

**Net:** the vendor mapping is a small, contained change. The expensive part is not the pixels — it
is *which channel* carries them.

---

## 1. The fork

There are two fundamentally different places an image can live.

**(A) In the conversation.** The image becomes a content block on a user message and sits in the
session history, exactly like text. The model sees it in context on every step of every subsequent
turn. This is what "multimodal chat" normally means.

**(B) In a tool.** The image never enters the conversation. A tool takes a path plus a question,
makes a **one-shot side call** to a vision model, and returns *text* — an ordinary tool result. The
main conversation stays text-only.

Everything below is the case for **B first**.

---

## 2. What (A) actually costs

Not the mapping — the three things behind it:

1. **The turn shape.** `PostTurn(sessionID, text)` must grow attachments, rippling through
   `Engine.PostTurn`, the `TurnRunner` interface, both turn runners in `cmd/serve.go`, the
   planner/executor seeding, and every frontend. [`file-upload.md`](file-upload.md) §3 D3 already
   flagged the turn-text note as "a stopgap… a structured `attachments []Ref` field is the migration
   path" — this is the change that forces it.
2. **Token cost, forever.** An image in the history is **re-sent on every subsequent step and turn**.
   Images are expensive in tokens. One photo dropped into a long conversation quietly taxes the whole
   remainder of it. There is no cheap fix inside model A: it is what "in context" means.
3. **Persistence.** Session history is `[]provider.Message` serialized to JSON. A `BlockImage`
   carrying raw bytes would base64 itself into `sessions/*.json` **for free** — and that is the
   problem, not the feature: multi-megabyte session files, re-read on every turn. Avoiding it means
   persisting a *path* and rehydrating the bytes at request time, which puts a reference the provider
   cannot resolve into the neutral type and starts to wobble the seam's purity. **This is the real
   open design question**, and it deserves a deliberate answer rather than being tripped over.

None of these is fatal. All of them are a bad trade for "the user sent a photo, tell me what's in it."

---

## 3. Why (B) fits this codebase

It is the same principle the artifact cache already runs on
([`chat-planner.md`](../adr/chat-planner.md) §D3): **the filesystem is working memory; references
travel, not payloads.** The agent doesn't hold a CSV in context — it holds a path and reads what it
needs. An image is the same shape of thing: a file in the scratch dir the agent can *interrogate*.

And it needs no new plumbing, because the file is already there: the upload path put it in the
session scratch dir and recorded it in the manifest, so the agent already knows the path.

---

## 4. Proposal — `view_image`, a vision tool

```
user sends photo ──▶ (existing upload path) ──▶ /…/session-scratch/<id>/photo-x.jpg
                                                            │
agent: view_image(path, "what does this receipt total?") ───┤
                                                            ▼
                                     one-shot StepRequest: [user: BlockImage + BlockText]
                                                            │
                                          ◀─── "The total is €42.10." (text tool result)
```

### 4.1 The change, in three pieces

1. **`provider.BlockImage`** (`internal/provider/types.go`) — a fourth `BlockKind`, with an
   `Image *ImageBlock` field carrying `{Data []byte, MediaType string}`. Bytes, not a path: the
   provider package stays pure and filesystem-free (the tool reads the file).
2. **The adapter** (`internal/provider/openai/openai.go`) — `RoleUser` stops flattening to a string
   and builds a multi-part user message (text parts + image parts as base64 data URIs). ~30 lines.
   Every other role is unchanged; the assistant never emits an image.
3. **`internal/tools/viewimage.go`** — `NewViewImageTool(p provider.Provider, model string, roots
   []string) tools.Tool`. It reads the file, checks containment, calls `p.Step` once with a
   throwaway two-block user message, and returns the response text. No tool schema change, no turn
   change, no session change: `Run` returns a `string`, which is all a tool result has ever been.

### 4.2 Decisions to make (with a lean)

- **V1 — Which model?** *Lean: the session's own model when it is vision-capable, with a
  `vision_model` config key as an override* (default `gpt-4o-mini`, which is vision-capable). The
  override matters because `openai_base_url` lets the engine point at a local llama.cpp / Ollama /
  vLLM server whose model may be text-only — vision must not become a hard dependency of running
  against a local model. A clear "this model cannot see images" error beats a confusing API failure.
- **V2 — Which paths may it read?** *Lean: containment, like `record_artifact`* — the session scratch
  dir and the workspace, rejecting anything outside (`withinDir`, `internal/tools/record_artifact.go`).
  A tool that reads an arbitrary path and ships the bytes to a third party is an exfiltration
  primitive; the same containment the manifest already enforces is the right bound.
- **V3 — Trusted or exposed?** *Lean: trusted, **not** in `exposedBuiltins`* — sandboxed authored
  tools (`call_tool`) should not be able to send arbitrary files to the model API. Same posture as
  `shell`.
- **V4 — Does it cost tokens the user can see?** The side call's usage is real spend. *Lean: record it
  through the existing `usage` accounting (Phase 6a) so it lands in the audit/usage totals like any
  other step, rather than being invisible.*
- **V5 — What does the agent get told?** The "you cannot see image content" line in
  `uploadTurnText` (`internal/frontend/telegram/telegram.go`) becomes "…call `view_image` with the
  path and a question." One line.

### 4.3 What it does *not* buy

The agent never *sees* the picture in the conversation; it asks a question and gets text back. For
"what's in this photo", "read this receipt", "what does this chart say", "transcribe this screenshot"
— indistinguishable. For sustained multi-turn reasoning about one image, it re-queries (each call is
a fresh look, and it pays the image's tokens again). If that turns out to be the dominant use, that
is the evidence that (A) is worth building — see §5.

---

## 5. When to build (A) anyway

Pull in full in-context vision if — and only if — one of these shows up:

- **Sustained visual reasoning:** conversations that go back and forth about the same image, where
  re-querying via a tool is both clumsy and (because each call re-sends the image) not actually
  cheaper.
- **The model must see the image to plan**, not merely to answer — e.g. the planner needs the picture
  to decide what to do at all.
- **Assistant-authored images** (charts the agent produces and then reasons about), which want a
  symmetric image channel.

Then the sequence is: the structured `attachments []Ref` turn field
([`file-upload.md`](file-upload.md) D3's v2) → a persistence answer for §2.3 (near-certainly: store
the path in the session, rehydrate bytes at request time) → a context policy for §2.2 (near-certainly:
only the most recent N images stay in context). `BlockImage` and the adapter mapping from §4.1 are
**reused unchanged** — building B first costs nothing against A. That is the main reason to start
there.

---

## 6. Sizing

| Piece | Size |
|---|---|
| `BlockImage` + `ImageBlock` in `provider` | trivial |
| OpenAI adapter multi-part user message | ~30 lines + tests |
| `view_image` tool (read, contain, call, return) | small; the only new file |
| Config `vision_model` + wiring into `ExecutorConfig` | small (a **field**, per plan.md's note) |
| Prompt/turn-text update (V5) | one line |
| **Total** | **~a day, no change to the engine, the turn shape, the session format, or any frontend** |

Compare: (A) touches the API, the engine, the turn runners, session persistence, and every frontend,
and adds a permanent per-turn token tax.

---

## 7. Open questions

- **Image size / dimensions.** Uploads are capped at 20 MB, but a vision API bills by resolution and
  may reject very large images. Do we downscale before the call (an image-decode dependency), or just
  surface the provider's error? *Lean: surface the error first; downscale only if it actually bites.*
- **Non-photo images.** A PDF page or a screenshot of a table is an image to Telegram but a *document*
  to the user. Nothing special is needed for v1 (it is a file with a path either way), but OCR-shaped
  requests may want a dedicated prompt.
- **Local/OpenAI-compatible endpoints** (`openai_base_url`) vary in multi-part support. V1's
  `vision_model` override plus a clean error is the answer, but it is worth an explicit test against
  at least one non-OpenAI endpoint before claiming support.
