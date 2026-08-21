package telegram

import (
	"context"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestSplitMessage_ShortTextIsUnchanged pins the common case: everything the bot says today
// fits in one message and must go out byte-identical, whitespace and all.
func TestSplitMessage_ShortTextIsUnchanged(t *testing.T) {
	for _, text := range []string{"session ended", "line one\n\nline two\n", strings.Repeat("x", maxMessageRunes)} {
		got := splitMessage(text)
		if len(got) != 1 || got[0] != text {
			t.Errorf("splitMessage(%.20q…) = %d chunks, want the text unchanged in one", text, len(got))
		}
	}
	if got := splitMessage("   \n "); got != nil {
		t.Errorf("splitMessage(whitespace) = %q, want none — Telegram rejects an empty message", got)
	}
}

// TestSplitMessage_PrefersParagraphThenLineThenWord checks where the cut lands: a long answer
// should break between paragraphs, fall back to a line break, then to a word boundary, and
// never leave a chunk over the limit.
func TestSplitMessage_PrefersParagraphThenLineThenWord(t *testing.T) {
	paragraph := strings.Repeat("word ", 200) + "end" // ~1,000 runes, no newline inside
	cases := []struct {
		name, text string
		wantFirst  string
	}{
		{
			name:      "paragraph boundary",
			text:      strings.Repeat(paragraph+"\n\n", 8),
			wantFirst: strings.TrimRight(strings.Repeat(paragraph+"\n\n", 4), "\n"),
		},
		{
			name:      "line boundary when there is no blank line",
			text:      strings.Repeat(paragraph+"\n", 8),
			wantFirst: strings.TrimRight(strings.Repeat(paragraph+"\n", 4), "\n"),
		},
		{
			name:      "word boundary when there is no newline",
			text:      strings.Repeat("word ", 2000),
			wantFirst: strings.TrimRight(strings.Repeat("word ", 819), " "),
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			chunks := splitMessage(c.text)
			if len(chunks) < 2 {
				t.Fatalf("got %d chunks, want the text split", len(chunks))
			}
			for i, chunk := range chunks {
				if n := utf8.RuneCountInString(chunk); n > maxMessageRunes {
					t.Errorf("chunk %d is %d runes, over the %d limit", i, n, maxMessageRunes)
				}
			}
			if chunks[0] != c.wantFirst {
				t.Errorf("first chunk = %d runes ending %q, want %d runes ending %q",
					utf8.RuneCountInString(chunks[0]), tail(chunks[0]),
					utf8.RuneCountInString(c.wantFirst), tail(c.wantFirst))
			}
			// Nothing but the whitespace at the boundaries may be lost.
			if got, want := strings.Join(strings.Fields(strings.Join(chunks, " ")), " "), strings.Join(strings.Fields(c.text), " "); got != want {
				t.Errorf("rejoined chunks lost content: %d words vs %d", len(strings.Fields(got)), len(strings.Fields(want)))
			}
		})
	}
}

// TestSplitMessage_CountsRunesNotBytes is the bug this renderer exists for: Telegram's limit
// is 4,096 characters, so a multi-byte message that fits must not be split, one that doesn't
// must be split on a rune boundary, and no chunk may contain a broken rune.
func TestSplitMessage_CountsRunesNotBytes(t *testing.T) {
	// 4,000 emoji: 16,000 bytes, but only 4,000 characters — one legal message.
	fits := strings.Repeat("😀", 4000)
	if got := splitMessage(fits); len(got) != 1 {
		t.Errorf("4,000 emoji split into %d chunks; the limit is characters, not bytes", len(got))
	}

	// 10,000 unbroken characters: split at the limit, mid-word because there is no boundary.
	long := strings.Repeat("привет", 2000) // 12,000 runes, 22,000 bytes
	chunks := splitMessage(long)
	if len(chunks) != 3 {
		t.Fatalf("got %d chunks, want 3 (12,000 runes at %d per message)", len(chunks), maxMessageRunes)
	}
	for i, chunk := range chunks {
		if !utf8.ValidString(chunk) {
			t.Errorf("chunk %d is not valid UTF-8 — a rune was cut in half", i)
		}
		if n := utf8.RuneCountInString(chunk); n > maxMessageRunes {
			t.Errorf("chunk %d is %d runes, over the %d limit", i, n, maxMessageRunes)
		}
	}
	if joined := strings.Join(chunks, ""); joined != long {
		t.Errorf("rejoined chunks (%d runes) != original (%d runes)", utf8.RuneCountInString(joined), utf8.RuneCountInString(long))
	}
}

// TestBotSend_AttachesButtonsToLastChunk: a keyboard belongs under the end of what it is
// about, so a long approval request must not strand its Approve/Deny row above the command.
func TestBotSend_AttachesButtonsToLastChunk(t *testing.T) {
	tr := newFakeTransport()
	bot := NewBot(tr, newFakeClient(), []int64{42})
	buttons := []Button{{Text: "Approve", Data: "approve:a1"}, {Text: "Deny", Data: "deny:a1"}}

	if err := bot.send(context.Background(), 100, strings.Repeat("word ", 3000), buttons); err != nil {
		t.Fatalf("send: %v", err)
	}

	tr.mu.Lock()
	defer tr.mu.Unlock()
	if len(tr.sent) < 2 {
		t.Fatalf("sent %d messages, want the text chunked", len(tr.sent))
	}
	for i, m := range tr.sent[:len(tr.sent)-1] {
		if len(m.buttons) != 0 {
			t.Errorf("chunk %d carries buttons; only the last one may", i)
		}
	}
	if last := tr.sent[len(tr.sent)-1]; len(last.buttons) != 2 {
		t.Errorf("last chunk has %d buttons, want 2", len(last.buttons))
	}
}

// TestBotSend_ReturnsFailure: a send failure is the renderer's return value, naming which
// part of a chunked message was lost. No call site may discard it.
func TestBotSend_ReturnsFailure(t *testing.T) {
	tr := newFakeTransport()
	boom := errors.New("bot was blocked by the user")
	tr.sendFail = func(string) error { return boom }
	bot := NewBot(tr, newFakeClient(), []int64{42})

	err := bot.send(context.Background(), 100, "short answer", nil)
	if !errors.Is(err, boom) {
		t.Fatalf("send error = %v, want %v", err, boom)
	}

	// A chunked message names the failing part, so the log says how much got through.
	tr.sendFail = func(text string) error {
		if strings.HasPrefix(text, "word") && len(text) > 100 {
			return boom
		}
		return nil
	}
	err = bot.send(context.Background(), 100, strings.Repeat("word ", 3000), nil)
	if !errors.Is(err, boom) || !strings.Contains(err.Error(), "part 1/") {
		t.Fatalf("chunked send error = %v, want one naming the failed part and wrapping %v", err, boom)
	}
}

// tail is the last few characters of s, for readable failure output on long chunks.
func tail(s string) string {
	r := []rune(s)
	if len(r) > 24 {
		r = r[len(r)-24:]
	}
	return string(r)
}
