package telegram

import (
	"context"
	"fmt"
	"os"
	"strings"
)

// maxMessageRunes is the Telegram Bot API's per-message text limit. It counts
// *characters* (runes), not bytes: a 4,096-rune answer of Cyrillic or emoji is well over
// 4,096 bytes and is still accepted, while a byte-based split would cut a multi-byte rune
// in half and produce a message Telegram rejects outright.
const maxMessageRunes = 4096

// splitMessage renders one logical message as the sequence of Telegram messages that
// carries it: every chunk is at most maxMessageRunes runes long, and the split prefers a
// paragraph break, then a line break, then a word boundary, before finally cutting mid-word
// (a single 5,000-character line has no better option).
//
// Whitespace at a chosen boundary is dropped rather than carried into the next chunk, so a
// paragraph split doesn't open the following message with a blank line. Empty chunks are
// dropped too — Telegram rejects an empty message, and a run of blank lines can produce one.
// A short message returns as a single chunk with its text untouched, which keeps the common
// case (every status reply the bot sends) byte-identical to what it sent before.
func splitMessage(text string) []string {
	runes := []rune(text)
	if len(runes) <= maxMessageRunes {
		if strings.TrimSpace(text) == "" {
			return nil
		}
		return []string{text}
	}
	var out []string
	for len(runes) > maxMessageRunes {
		cut := breakPoint(runes[:maxMessageRunes])
		if chunk := strings.TrimRight(string(runes[:cut]), " \t\r\n"); chunk != "" {
			out = append(out, chunk)
		}
		runes = runes[cut:]
	}
	if tail := strings.TrimRight(string(runes), " \t\r\n"); tail != "" {
		out = append(out, tail)
	}
	return out
}

// breakPoint returns how many runes of window belong to the current chunk: the position
// just after the last paragraph break in it, else after the last line break, else after the
// last space, else the whole window (an unbroken line longer than the limit).
//
// The result is always at least 1, so a caller consuming window[:breakPoint(window)] always
// makes progress.
func breakPoint(window []rune) int {
	for i := len(window) - 2; i >= 0; i-- {
		if window[i] == '\n' && window[i+1] == '\n' {
			return i + 2
		}
	}
	for i := len(window) - 1; i >= 0; i-- {
		if window[i] == '\n' {
			return i + 1
		}
	}
	for i := len(window) - 1; i >= 0; i-- {
		if window[i] == ' ' {
			return i + 1
		}
	}
	return len(window)
}

// send is the one way text leaves the bot: it splits the text into deliverable chunks and
// posts them in order, attaching any inline keyboard to the final chunk (the buttons belong
// under the end of what they are about, and Telegram would otherwise leave a keyboard
// stranded above the rest of the message).
//
// It returns the first delivery failure, naming the chunk that failed — an undelivered
// message is a failed turn, not a cosmetic problem, and no caller may discard this error.
func (b *Bot) send(ctx context.Context, chatID int64, text string, buttons []Button) error {
	chunks := splitMessage(text)
	if len(chunks) == 0 {
		return nil
	}
	for i, chunk := range chunks {
		var attach []Button
		if i == len(chunks)-1 {
			attach = buttons
		}
		if err := b.transport.Send(ctx, chatID, chunk, attach); err != nil {
			if len(chunks) == 1 {
				return err
			}
			return fmt.Errorf("part %d/%d: %w", i+1, len(chunks), err)
		}
	}
	return nil
}

// notify sends a message whose delivery the bot cannot act on: a usage hint, a command
// acknowledgement, or an error report that is itself the response to a failure. There is
// nowhere left to report such a failure *to* — the chat is the only channel — so it goes to
// the operator's log. This exists so that "nothing to do about it" is stated once, here,
// rather than as ~30 silent `_ =` discards.
func (b *Bot) notify(ctx context.Context, chatID int64, text string) {
	if err := b.send(ctx, chatID, text, nil); err != nil {
		fmt.Fprintf(os.Stderr, "telegram: deliver to chat %d: %v\n", chatID, err)
	}
}

// answerCallback acknowledges a button press. Like notify, the toast is the reply to an
// action the user already took, so a failure is logged rather than propagated.
func (b *Bot) answerCallback(ctx context.Context, callbackID, text string) {
	if err := b.transport.Answer(ctx, callbackID, text); err != nil {
		fmt.Fprintf(os.Stderr, "telegram: answer callback %s: %v\n", callbackID, err)
	}
}
