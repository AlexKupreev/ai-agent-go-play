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

// Approver decides whether an action may proceed. Approve may block — a CLI
// prompt, or an async approver waiting on a frontend — so it takes a context and
// callers must honor cancellation/timeout. A non-nil error means no decision
// could be obtained; callers treat that as "not approved".
//
// This is the seam that lets approvals move off stdin: the synchronous
// StdinApprover serves the CLI today; a queue-backed approver (Phase 4 API) will
// register the request and wait for a frontend to resolve it.
type Approver interface {
	Approve(ctx context.Context, req ApprovalRequest) (bool, error)
}

// ApproverFunc adapts a plain function to Approver (tests, simple policies).
type ApproverFunc func(ctx context.Context, req ApprovalRequest) (bool, error)

func (f ApproverFunc) Approve(ctx context.Context, req ApprovalRequest) (bool, error) {
	return f(ctx, req)
}

// StdinApprover is the CLI approver: it prints the request and reads a y/N answer
// from stdin. The interactive stdin read is not cancellable, so it does not honor
// ctx — acceptable for the CLI; async approvers honor ctx.
type StdinApprover struct{}

func (StdinApprover) Approve(_ context.Context, req ApprovalRequest) (bool, error) {
	fmt.Printf("\n[approve] %s\n  %s\n  proceed? [y/N] > ", req.Title, req.Detail)
	scanner := bufio.NewScanner(os.Stdin)
	if scanner.Scan() {
		ans := strings.ToLower(strings.TrimSpace(scanner.Text()))
		return ans == "y" || ans == "yes", nil
	}
	return false, nil
}
