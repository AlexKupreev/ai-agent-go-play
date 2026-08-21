package telegram

import (
	"errors"
	"fmt"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// TestRetryAfter pins which send failures the live transport retries. Chunking posts several
// messages back to back, so flood control is now an expected, transient outcome — but it is
// the only one: retrying a permanent rejection would just delay the error the user needs.
func TestRetryAfter(t *testing.T) {
	flood := func(seconds int) error {
		return &tgbotapi.Error{
			Code:               429,
			Message:            "Too Many Requests: retry after 3",
			ResponseParameters: tgbotapi.ResponseParameters{RetryAfter: seconds},
		}
	}
	cases := []struct {
		name string
		err  error
		wait time.Duration
		want bool
	}{
		{"flood control honors the hint", flood(3), 3 * time.Second, true},
		{"flood control with no hint waits a little", flood(0), time.Second, true},
		{"an implausible hint is not waited out", flood(600), 0, false},
		{"wrapped flood control is still recognized", fmt.Errorf("send: %w", flood(2)), 2 * time.Second, true},
		{"a permanent API error is final", &tgbotapi.Error{Code: 400, Message: "message is too long"}, 0, false},
		{"a transport error is final", errors.New("connection reset"), 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			wait, retry := retryAfter(c.err)
			if retry != c.want || wait != c.wait {
				t.Errorf("retryAfter(%v) = (%v, %v), want (%v, %v)", c.err, wait, retry, c.wait, c.want)
			}
		})
	}
}

func TestNativeCommands(t *testing.T) {
	got := nativeCommands([]MenuCommand{{Command: "help", Description: "List commands"}, {Command: "space", Description: "Manage spaces"}})
	if len(got) != 2 || got[0].Command != "help" || got[0].Description != "List commands" || got[1].Command != "space" {
		t.Fatalf("nativeCommands = %+v", got)
	}
}
