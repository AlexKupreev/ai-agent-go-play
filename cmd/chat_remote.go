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
	"ai-agent-go-play/internal/capability"
	"ai-agent-go-play/internal/guidance"
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

	// --model / --tier / --space seed a freshly-created session's sticky overrides (the tier is
	// validated here so a bad one fails before we open a session). They do not disturb a
	// resumed session, which keeps its stored stickies. /model, /tier, and /space change them
	// live via PATCH mid-REPL. The space goes over as typed — spaces live workspace-side, so
	// the engine resolves the name or id and refuses an unknown one when the session is
	// created, naming the spaces that exist.
	initialOpts := api.RunOptions{Model: modelFlag, Tier: tierFlag, Space: chatSpaceFlag}
	if err := validateTierFlag(tierFlag); err != nil {
		return err
	}

	sessionID, resumed, turns, curModel, curTier, curSpace, err := attachSession(ctx, c, addr, initialOpts)
	if err != nil {
		return err
	}

	if resumed {
		fmt.Fprintf(os.Stderr, "agent chat — engine %s, session %s  (resumed, %d turns)\n", addr, sessionID, turns)
	} else {
		fmt.Fprintf(os.Stderr, "agent chat — engine %s, session %s  (new)\n", addr, sessionID)
	}
	fmt.Fprintf(os.Stderr, "model %s, tier %s, space %s\n", modelLabel(curModel), tierLabel(curTier), spaceLabel(curSpace))
	fmt.Fprintln(os.Stderr, "(/new or /reset new conversation, /model [id] & /tier [t] & /space [name] switch, /guidance manages standing instructions, /end close, /purge delete for good, /exit or Ctrl-D detach)")

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
		// /model [id] and /tier [t] take an optional argument, so they are matched by prefix
		// ahead of the exact-match switch. They set the session's sticky override on the engine
		// (PATCH /sessions/{id}), effective from the next turn; no arg shows the current value.
		if arg, ok := strings.CutPrefix(line, "/model"); ok {
			arg = strings.TrimSpace(arg)
			if arg == "" {
				fmt.Fprintf(os.Stderr, "(model: %s; usage: /model <id>)\n", modelLabel(curModel))
				continue
			}
			info, err := c.UpdateSession(ctx, sessionID, &arg, nil, nil)
			if err != nil {
				fmt.Fprintf(os.Stderr, "set model: %v\n", err)
				continue
			}
			curModel = info.Model
			fmt.Fprintf(os.Stderr, "(model set to %s — effective next turn)\n", modelLabel(curModel))
			continue
		}
		if arg, ok := strings.CutPrefix(line, "/tier"); ok {
			arg = strings.TrimSpace(arg)
			if arg == "" {
				fmt.Fprintf(os.Stderr, "(tier: %s; usage: /tier safe|balanced|permissive)\n", tierLabel(curTier))
				continue
			}
			if err := validateTierFlag(arg); err != nil {
				fmt.Fprintf(os.Stderr, "%v\n", err)
				continue
			}
			info, err := c.UpdateSession(ctx, sessionID, nil, &arg, nil)
			if err != nil {
				fmt.Fprintf(os.Stderr, "set tier: %v\n", err)
				continue
			}
			curTier = info.Tier
			fmt.Fprintf(os.Stderr, "(tier set to %s — effective next turn; clamped to the serve ceiling)\n", tierLabel(curTier))
			continue
		}
		if arg, ok := strings.CutPrefix(line, "/guidance"); ok && (arg == "" || strings.ContainsAny(arg[:1], " \t")) {
			result, err := applyRemoteGuidance(ctx, c, strings.TrimSpace(arg), curSpace, sessionID)
			if err != nil {
				fmt.Fprintf(os.Stderr, "/guidance: %v\n", err)
				continue
			}
			message := guidance.FormatResult(result)
			if result.Changed {
				message += " — effective from your next message"
			}
			fmt.Fprintln(os.Stderr, message)
			continue
		}
		// /space [name] switches the session's active space (spaces.md §5): the name or id
		// is sent as typed and the engine stores the canonical id as the session sticky;
		// `-` returns to the global scope. The engine resolves it against the workspace's
		// space store as it is set, so an unknown space is refused here — with the
		// available spaces named — instead of breaking the next turn.
		if arg, ok := strings.CutPrefix(line, "/space"); ok {
			arg = strings.TrimSpace(arg)
			if arg == "" {
				fmt.Fprintf(os.Stderr, "(space: %s; usage: /space <name-or-id>, /space - for global)\n", spaceLabel(curSpace))
				continue
			}
			val := arg
			if arg == "-" {
				val = ""
			}
			info, err := c.UpdateSession(ctx, sessionID, nil, nil, &val)
			if err != nil {
				fmt.Fprintf(os.Stderr, "set space: %v\n", err)
				continue
			}
			curSpace = info.Space
			fmt.Fprintf(os.Stderr, "(space set to %s — effective next turn)\n", spaceLabel(curSpace))
			continue
		}
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
		case "/purge":
			// Irreversible: hard-delete this conversation instead of archiving it. No undo,
			// so detach afterwards.
			if err := c.PurgeSession(ctx, sessionID); err != nil {
				fmt.Fprintf(os.Stderr, "purge session: %v\n", err)
			} else {
				fmt.Fprintln(os.Stderr, "(session purged — permanently deleted)")
			}
			return nil
		case "/reset", "/new":
			// No server-side "clear history"; a fresh session is the honest
			// equivalent. Close the old one so it doesn't linger. /new + /reset are
			// aliases here to match the Telegram bot's session-control verbs. The new
			// session carries the same initial stickies (--model/--tier), not the ones
			// changed mid-REPL, matching a fresh `agent chat` invocation.
			if err := c.CloseSession(ctx, sessionID); err != nil {
				fmt.Fprintf(os.Stderr, "close old session: %v\n", err)
			}
			newID, err := c.StartSession(ctx, initialOpts)
			if err != nil {
				return fmt.Errorf("start session: %w", err)
			}
			sessionID = newID
			curModel, curTier, curSpace = initialOpts.Model, initialOpts.Tier, initialOpts.Space
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
// (validated against the engine's list, inheriting its stored sticky model/tier/space),
// otherwise a new session is created seeded with opts. It also returns the session's current
// stickies so the REPL can display and track them.
func attachSession(ctx context.Context, c *api.Client, addr string, opts api.RunOptions) (id string, resumed bool, turns int, model, tier, spaceID string, err error) {
	if chatSessionFlag != "" {
		infos, err := c.ListSessions(ctx)
		if err != nil {
			return "", false, 0, "", "", "", err
		}
		for _, in := range infos {
			if in.ID == chatSessionFlag {
				return in.ID, true, in.Turns, in.Model, in.Tier, in.Space, nil
			}
		}
		return "", false, 0, "", "", "", fmt.Errorf("no session %q on %s (list them with: agent chat --addr %s --list)", chatSessionFlag, addr, addr)
	}
	id, err = c.StartSession(ctx, opts)
	if err != nil {
		return "", false, 0, "", "", "", fmt.Errorf("start session: %w", err)
	}
	return id, false, 0, opts.Model, opts.Tier, opts.Space, nil
}

// validateTierFlag accepts an empty string (inherit the engine default) or a valid tier name,
// giving the operator immediate local feedback before a request reaches the engine.
func validateTierFlag(tier string) error {
	if tier == "" {
		return nil
	}
	_, err := capability.ParseTier(tier)
	return err
}

// tierLabel renders a tier for display, naming the engine default when unset.
func tierLabel(tier string) string {
	if tier == "" {
		return "(engine default)"
	}
	return tier
}

// spaceLabel renders a space id for display, naming the global scope when unset.
func spaceLabel(id string) string {
	if id == "" {
		return "(global scope)"
	}
	return id
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

	// No per-turn override: /model and /tier set the session's sticky overrides on the engine
	// (PATCH), which PostTurn merges in server-side, so the turn request stays empty.
	runID, err := c.PostTurn(ctx, sessionID, line, api.RunOptions{})
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
