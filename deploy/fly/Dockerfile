# Multi-stage build for the ai-agent binary, deployed on Fly.io.
#
# Runtime is Alpine (not scratch/distroless) on purpose: the `shell` built-in
# execs `bash -c`, so a real shell must be present, plus ca-certificates for the
# outbound TLS the agent makes (OpenAI, Telegram, web_fetch). The binary itself is
# static (CGO disabled; all deps are pure Go).
#
# Build context must be the repo root so the source is COPY-able:
#   fly deploy -c deploy/fly/fly.one-agent.toml   (run from the repo root)

# ---- build ----
FROM golang:1.25-alpine AS build
WORKDIR /src

# Cache module downloads separately from the source.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/agent .

# ---- runtime ----
FROM alpine:3.21
RUN apk add --no-cache ca-certificates bash git curl coreutils jq tini

WORKDIR /workspace

COPY --from=build /out/agent /usr/local/bin/agent
COPY deploy/fly/entrypoint-one.sh /usr/local/bin/entrypoint-one.sh
COPY deploy/fly/entrypoint-two.sh /usr/local/bin/entrypoint-two.sh
RUN chmod +x /usr/local/bin/entrypoint-one.sh /usr/local/bin/entrypoint-two.sh

# tini as PID 1 reaps children and forwards signals — important for the
# two-agent entrypoint, which supervises two background `serve` processes.
ENTRYPOINT ["/sbin/tini", "--"]

# Default to the single-agent entrypoint; each fly.toml overrides this via
# [processes]. (entrypoint-two.sh is selected by fly.two-agent.toml.)
CMD ["/usr/local/bin/entrypoint-one.sh"]
