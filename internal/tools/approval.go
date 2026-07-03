package tools

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
)

// ApprovalRequest describes an action awaiting human approval. It is structured
// (not a bare string) so a frontend can render it and an asynchronous approver
// can key a pending request by run while it waits for a remote decision.
type ApprovalRequest struct {
	Kind   string // category, e.g. "shell.destructive", "tool.capability"
	Title  string // short human summary
	Detail string // full text: the command, or the capability list
	RunID  string // owning run, for routing/queueing (may be empty)
}

// Question is a free-text clarifying question the agent poses mid-run when the task is
// ambiguous. It is the ask counterpart of ApprovalRequest — same routing model (park on
// the owning run, block for the answer), but the answer is text rather than a yes/no.
type Question struct {
	Prompt string // the question to ask the human
	RunID  string // owning run, for routing/queueing (may be empty)
}

// HumanGate is the single human-in-the-loop seam: it routes a prompt to whoever owns the
// run and blocks for the human's response. Approve gates a risky action (yes/no); Ask
// poses a free-text question and returns the typed answer. Both may block — a CLI prompt,
// or an async gate waiting on a frontend — so both take a context and callers must honor
// cancellation/timeout. A non-nil error means no response could be obtained: Approve
// callers treat that as "not approved", Ask callers surface it to the model.
//
// This is the seam that lets human interaction move off stdin and over any client: the
// synchronous StdinGate serves the CLI; a queue-backed gate (the API's ApprovalQueue)
// registers the request and waits for a frontend (serve/Telegram) to resolve it.
type HumanGate interface {
	Approve(ctx context.Context, req ApprovalRequest) (bool, error)
	Ask(ctx context.Context, q Question) (string, error)
}

// ApproverFunc adapts a plain approve function to HumanGate (tests, simple policies).
// Its Ask is unsupported — use GateFuncs when a gate also needs to answer questions.
type ApproverFunc func(ctx context.Context, req ApprovalRequest) (bool, error)

func (f ApproverFunc) Approve(ctx context.Context, req ApprovalRequest) (bool, error) {
	return f(ctx, req)
}

func (ApproverFunc) Ask(context.Context, Question) (string, error) {
	return "", fmt.Errorf("ask_user not supported by this gate")
}

// GateFuncs adapts a pair of plain functions to HumanGate (tests, simple policies). A nil
// ApproveFn approves everything; a nil AskFn returns an empty answer.
type GateFuncs struct {
	ApproveFn func(ctx context.Context, req ApprovalRequest) (bool, error)
	AskFn     func(ctx context.Context, q Question) (string, error)
}

func (g GateFuncs) Approve(ctx context.Context, req ApprovalRequest) (bool, error) {
	if g.ApproveFn == nil {
		return true, nil
	}
	return g.ApproveFn(ctx, req)
}

func (g GateFuncs) Ask(ctx context.Context, q Question) (string, error) {
	if g.AskFn == nil {
		return "", nil
	}
	return g.AskFn(ctx, q)
}

// StdinGate is the CLI human-gate: it prints the prompt and reads the answer from stdin
// (a y/N for Approve, a line of text for Ask). The interactive read is not cancellable,
// so it does not honor ctx — acceptable for the CLI; async gates honor ctx.
type StdinGate struct{}

func (StdinGate) Approve(_ context.Context, req ApprovalRequest) (bool, error) {
	fmt.Printf("\n[approve] %s\n  %s\n  proceed? [y/N] > ", req.Title, req.Detail)
	scanner := bufio.NewScanner(os.Stdin)
	if scanner.Scan() {
		ans := strings.ToLower(strings.TrimSpace(scanner.Text()))
		return ans == "y" || ans == "yes", nil
	}
	return false, nil
}

func (StdinGate) Ask(_ context.Context, q Question) (string, error) {
	fmt.Printf("\n[agent asks] %s\n> ", q.Prompt)
	scanner := bufio.NewScanner(os.Stdin)
	if scanner.Scan() {
		return strings.TrimSpace(scanner.Text()), nil
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("failed to read user input: %w", err)
	}
	return "", nil
}
