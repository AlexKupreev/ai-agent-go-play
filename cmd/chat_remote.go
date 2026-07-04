package cmd

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"

	"ai-agent-go-play/internal/api"
)

// runRemoteChat is `agent chat --addr`: a REPL that drives a running engine's
// persistent session over HTTP+SSE instead of an in-process executor. Because the
// session lives on the engine, the conversation survives quitting and can be resumed
// here (--session) or from another client (e.g. Telegram) — the payoff a local chat,
// whose history is in-process, cannot give.
func runRemoteChat(addr string) error {
	c := api.NewClient("http://" + addr)
	ctx := context.Background()

	if chatListFlag {
		return listRemoteSessions(ctx, c, addr)
	}

	sessionID, resumed, turns, err := attachSession(ctx, c, addr)
	if err != nil {
		return err
	}

	if resumed {
		fmt.Fprintf(os.Stderr, "agent chat — engine %s, session %s  (resumed, %d turns)\n", addr, sessionID, turns)
	} else {
		fmt.Fprintf(os.Stderr, "agent chat — engine %s, session %s  (new)\n", addr, sessionID)
	}
	fmt.Fprintln(os.Stderr, "(/new or /reset new conversation, /end close, /exit or Ctrl-D detach)")

	// SIGINT cancels the current turn rather than killing the REPL; drained at each
	// prompt so a stray Ctrl-C while idle doesn't cancel the next turn.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)
	defer signal.Stop(sigCh)

	// One scanner for the whole REPL, shared with the approval watcher (see
	// watchApprovalsScan): the prompt and approval answers never read stdin at the
	// same time, so a single buffer avoids two bufio readers racing.
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for {
		fmt.Fprint(os.Stderr, "\n> ")
		if !scanner.Scan() {
			fmt.Fprintf(os.Stderr, "\n(detached — resume with: agent chat --addr %s --session %s)\n", addr, sessionID)
			return nil
		}
		line := strings.TrimSpace(scanner.Text())
		switch line {
		case "":
			continue
		case "/exit", "/quit":
			fmt.Fprintf(os.Stderr, "(detached — resume with: agent chat --addr %s --session %s)\n", addr, sessionID)
			return nil
		case "/end":
			if err := c.CloseSession(ctx, sessionID); err != nil {
				fmt.Fprintf(os.Stderr, "close session: %v\n", err)
			} else {
				fmt.Fprintln(os.Stderr, "(session closed)")
			}
			return nil
		case "/reset", "/new":
			// No server-side "clear history"; a fresh session is the honest
			// equivalent. Close the old one so it doesn't linger. /new + /reset are
			// aliases here to match the Telegram bot's session-control verbs.
			if err := c.CloseSession(ctx, sessionID); err != nil {
				fmt.Fprintf(os.Stderr, "close old session: %v\n", err)
			}
			newID, err := c.StartSession(ctx)
			if err != nil {
				return fmt.Errorf("start session: %w", err)
			}
			sessionID = newID
			fmt.Fprintf(os.Stderr, "(new conversation — session %s)\n", sessionID)
			continue
		}

		if err := runRemoteTurn(sigCh, c, sessionID, line, scanner); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
		}
	}
}

// listRemoteSessions prints the engine's resumable sessions and returns.
func listRemoteSessions(ctx context.Context, c *api.Client, addr string) error {
	infos, err := c.ListSessions(ctx)
	if err != nil {
		return err
	}
	if len(infos) == 0 {
		fmt.Printf("no sessions on %s (a message starts one)\n", addr)
		return nil
	}
	fmt.Printf("%-18s %6s  %-19s  %s\n", "SESSION", "TURNS", "LAST ACTIVE", "TITLE")
	for _, in := range infos {
		fmt.Printf("%-18s %6d  %-19s  %s\n", in.ID, in.Turns, in.UpdatedAt.Local().Format("2006-01-02 15:04:05"), in.Title)
	}
	return nil
}

// attachSession resolves the session to talk to: --session resumes an existing one
// (validated against the engine's list), otherwise a new session is created.
func attachSession(ctx context.Context, c *api.Client, addr string) (id string, resumed bool, turns int, err error) {
	if chatSessionFlag != "" {
		infos, err := c.ListSessions(ctx)
		if err != nil {
			return "", false, 0, err
		}
		for _, in := range infos {
			if in.ID == chatSessionFlag {
				return in.ID, true, in.Turns, nil
			}
		}
		return "", false, 0, fmt.Errorf("no session %q on %s (list them with: agent chat --addr %s --list)", chatSessionFlag, addr, addr)
	}
	id, err = c.StartSession(ctx)
	if err != nil {
		return "", false, 0, fmt.Errorf("start session: %w", err)
	}
	return id, false, 0, nil
}

// runRemoteTurn posts one turn to the session and streams its reply. Ctrl-C cancels
// just this turn: it stops the remote run (so it doesn't keep executing headless) and
// returns to the prompt.
func runRemoteTurn(sigCh <-chan os.Signal, c *api.Client, sessionID, line string, scanner *bufio.Scanner) error {
	// Discard any Ctrl-C that arrived while idle at the prompt.
	select {
	case <-sigCh:
	default:
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runID, err := c.PostTurn(ctx, sessionID, line)
	if err != nil {
		return err
	}

	// Approvals have no server push over SSE, so poll for parked requests and prompt
	// the operator until the turn ends. Shares the REPL's stdin scanner.
	watchCtx, cancelWatch := context.WithCancel(ctx)
	defer cancelWatch()
	go watchApprovalsScan(watchCtx, c, scanner)

	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-sigCh:
			fmt.Fprintln(os.Stderr, "\n(interrupted)")
			cancel()
			// Stop the remote run with a fresh context — ctx is already cancelled.
			stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer stopCancel()
			_ = c.StopRun(stopCtx, runID)
		case <-done:
		}
	}()

	err = c.StreamEvents(ctx, runID, printEvent)
	if ctx.Err() != nil {
		return nil // the turn was cancelled by Ctrl-C — expected, not an error
	}
	if err == nil {
		if info, statusErr := c.RunStatus(context.Background(), runID); statusErr == nil {
			printRunUsage(info)
		}
	}
	return err
}
