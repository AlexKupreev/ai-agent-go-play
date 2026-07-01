// Package telegram is an optional chat frontend: a peer client of the headless
// engine (it drives api.Client exactly like `agent client` does, with no special
// access). It is split into transport-neutral bot logic (this file) and a live
// transport that talks to the Telegram Bot API (transport_http.go) — so the bot is
// fully testable with a fake transport and no network, and adding the real bot later
// is a single well-scoped file.
//
// The frontend is the auth boundary: the engine trusts localhost, so the bot gates
// who may reach it via a Telegram user-id allowlist. It is entirely optional — the
// engine runs unchanged when no token is configured (see cmd/serve.go).
package telegram

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"

	"ai-agent-go-play/internal/api"
)

// Button is one inline-keyboard button: a label plus the callback data delivered
// back as Callback.Data when it is pressed.
type Button struct {
	Text string
	Data string
}

// Message is an inbound text message from a chat.
type Message struct {
	ChatID int64
	UserID int64
	Text   string
}

// Callback is an inbound inline-button press.
type Callback struct {
	ID     string // callback query id, to acknowledge via Transport.Answer
	ChatID int64
	UserID int64
	Data   string // the pressed button's callback data (e.g. "approve:a3")
}

// Update is one inbound event: exactly one of Message/Callback is set.
type Update struct {
	Message  *Message
	Callback *Callback
}

// Transport abstracts the chat backend. The production implementation talks to the
// Telegram Bot API (transport_http.go); tests use a fake. Keeping the bot logic
// behind this seam is what lets the whole frontend be exercised without a live bot.
type Transport interface {
	// Updates returns a channel of inbound updates. It closes when ctx is done or the
	// transport stops.
	Updates(ctx context.Context) (<-chan Update, error)
	// Send posts a message to a chat, optionally with one row of inline buttons.
	Send(ctx context.Context, chatID int64, text string, buttons []Button) error
	// Answer acknowledges a callback (button press) with a short toast.
	Answer(ctx context.Context, callbackID, text string) error
}

// Client is the slice of api.Client the bot needs. Declaring it here (rather than
// depending on the concrete type) keeps the bot testable and documents the exact
// surface the frontend uses. *api.Client satisfies it. The bot talks to the engine in
// terms of sessions (persistent conversations), so each chat is a running dialogue.
type Client interface {
	StartSession(ctx context.Context) (string, error)
	PostTurn(ctx context.Context, sessionID, text string) (runID string, err error)
	CloseSession(ctx context.Context, sessionID string) error
	StreamEvents(ctx context.Context, runID string, onEvent func(api.Event)) error
	Resolve(ctx context.Context, id string, approved bool) error
}

// Bot wires a chat Transport to the engine Client, gating access by an allowlist of
// Telegram user ids. Each chat is mapped to an engine session (a persistent
// conversation): a message runs a turn and streams its events back to the chat; a
// parked approval becomes an Approve/Deny inline keyboard resolved over the API.
// /new starts a fresh session, /end terminates the current one.
type Bot struct {
	transport Transport
	client    Client
	allowed   map[int64]bool

	mu       sync.Mutex
	sessions map[int64]string // chat id -> engine session id
}

// NewBot builds a bot. allowed is the set of Telegram user ids permitted to reach the
// engine; an empty set denies everyone (fail closed — the bot is the auth gate).
func NewBot(transport Transport, client Client, allowed []int64) *Bot {
	set := make(map[int64]bool, len(allowed))
	for _, id := range allowed {
		set[id] = true
	}
	return &Bot{transport: transport, client: client, allowed: set, sessions: map[int64]string{}}
}

// Run reads updates and dispatches them until ctx is cancelled or the update stream
// closes.
func (b *Bot) Run(ctx context.Context) error {
	updates, err := b.transport.Updates(ctx)
	if err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case up, ok := <-updates:
			if !ok {
				return nil
			}
			b.dispatch(ctx, up)
		}
	}
}

func (b *Bot) dispatch(ctx context.Context, up Update) {
	switch {
	case up.Message != nil:
		b.handleMessage(ctx, *up.Message)
	case up.Callback != nil:
		b.handleCallback(ctx, *up.Callback)
	}
}

// allow reports whether a Telegram user id may drive the engine.
func (b *Bot) allow(userID int64) bool { return b.allowed[userID] }

func (b *Bot) handleMessage(ctx context.Context, m Message) {
	if !b.allow(m.UserID) {
		_ = b.transport.Send(ctx, m.ChatID, "not authorized", nil)
		return
	}
	if strings.HasPrefix(m.Text, "/") {
		b.handleCommand(ctx, m)
		return
	}

	// A normal message is a turn on this chat's session (created on first use).
	sessionID, err := b.sessionFor(ctx, m.ChatID)
	if err != nil {
		_ = b.transport.Send(ctx, m.ChatID, "could not start a session: "+err.Error(), nil)
		return
	}
	runID, err := b.client.PostTurn(ctx, sessionID, m.Text)
	if err != nil {
		_ = b.transport.Send(ctx, m.ChatID, "failed to run turn: "+err.Error(), nil)
		return
	}
	// Stream the turn's events back to the chat. It outlives this update, so give the
	// stream its own goroutine.
	go b.stream(ctx, m.ChatID, runID)
}

// handleCommand handles the chat control commands: /new (fresh session), /end
// (terminate the current session). Unknown commands get a short hint.
func (b *Bot) handleCommand(ctx context.Context, m Message) {
	switch strings.Fields(m.Text)[0] {
	case "/new", "/start":
		b.closeChat(ctx, m.ChatID) // end any existing session first
		if _, err := b.sessionFor(ctx, m.ChatID); err != nil {
			_ = b.transport.Send(ctx, m.ChatID, "could not start a session: "+err.Error(), nil)
			return
		}
		_ = b.transport.Send(ctx, m.ChatID, "started a new session — send a message to begin", nil)
	case "/end", "/stop":
		if b.closeChat(ctx, m.ChatID) {
			_ = b.transport.Send(ctx, m.ChatID, "session ended", nil)
		} else {
			_ = b.transport.Send(ctx, m.ChatID, "no active session", nil)
		}
	default:
		_ = b.transport.Send(ctx, m.ChatID, "commands: /new (start a session), /end (terminate it)", nil)
	}
}

// sessionFor returns the chat's engine session, creating one on first use.
func (b *Bot) sessionFor(ctx context.Context, chatID int64) (string, error) {
	b.mu.Lock()
	id, ok := b.sessions[chatID]
	b.mu.Unlock()
	if ok {
		return id, nil
	}
	id, err := b.client.StartSession(ctx)
	if err != nil {
		return "", err
	}
	b.mu.Lock()
	b.sessions[chatID] = id
	b.mu.Unlock()
	return id, nil
}

// closeChat terminates the chat's session (if any) on the engine and drops the
// mapping. Returns whether a session was active.
func (b *Bot) closeChat(ctx context.Context, chatID int64) bool {
	b.mu.Lock()
	id, ok := b.sessions[chatID]
	delete(b.sessions, chatID)
	b.mu.Unlock()
	if !ok {
		return false
	}
	if err := b.client.CloseSession(ctx, id); err != nil {
		fmt.Fprintf(os.Stderr, "telegram: close session %s: %v\n", id, err)
	}
	return true
}

// stream relays a run's events to a chat until the stream ends.
func (b *Bot) stream(ctx context.Context, chatID int64, runID string) {
	err := b.client.StreamEvents(ctx, runID, func(e api.Event) {
		switch e.Kind {
		case api.KindApprovalRequested:
			b.sendApproval(ctx, chatID, e)
		case api.KindDone:
			// Terminal marker only: its Text duplicates the final EvResponse
			// (already forwarded by the default branch), so don't send it again.
		case api.KindError:
			_ = b.transport.Send(ctx, chatID, "run error: "+e.Text, nil)
		default:
			// Assistant prose (agent EvResponse) carries Text; forward anything with
			// visible text, skip the noisier structured events.
			if e.Text != "" {
				_ = b.transport.Send(ctx, chatID, e.Text, nil)
			}
		}
	})
	if err != nil && ctx.Err() == nil {
		_ = b.transport.Send(ctx, chatID, "stream ended: "+err.Error(), nil)
	}
}

// sendApproval renders a parked escalation as an Approve/Deny inline keyboard.
func (b *Bot) sendApproval(ctx context.Context, chatID int64, e api.Event) {
	text := fmt.Sprintf("Approval needed — %s: %s", e.Tool, e.Text)
	if e.Input != "" {
		text += "\n" + e.Input
	}
	buttons := []Button{
		{Text: "Approve", Data: "approve:" + e.ApprovalID},
		{Text: "Deny", Data: "deny:" + e.ApprovalID},
	}
	_ = b.transport.Send(ctx, chatID, text, buttons)
}

func (b *Bot) handleCallback(ctx context.Context, c Callback) {
	if !b.allow(c.UserID) {
		_ = b.transport.Answer(ctx, c.ID, "not authorized")
		return
	}
	action, id, ok := parseCallback(c.Data)
	if !ok {
		_ = b.transport.Answer(ctx, c.ID, "unrecognized action")
		return
	}
	approved := action == "approve"
	if err := b.client.Resolve(ctx, id, approved); err != nil {
		_ = b.transport.Answer(ctx, c.ID, "could not resolve: "+err.Error())
		return
	}
	if approved {
		_ = b.transport.Answer(ctx, c.ID, "approved")
	} else {
		_ = b.transport.Answer(ctx, c.ID, "denied")
	}
}

// parseCallback splits button callback data of the form "approve:<id>" / "deny:<id>".
func parseCallback(data string) (action, id string, ok bool) {
	action, id, found := strings.Cut(data, ":")
	if !found || id == "" || (action != "approve" && action != "deny") {
		return "", "", false
	}
	return action, id, true
}
