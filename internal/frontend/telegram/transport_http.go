package telegram

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// httpTransport is the live Telegram Bot API transport (long-poll getUpdates for
// inbound messages/callbacks; sendMessage / answerCallbackQuery for outbound). It
// adapts the tgbotapi SDK to the Transport interface the Bot logic is written against.
type httpTransport struct {
	bot *tgbotapi.BotAPI
}

func (t *httpTransport) Username() string { return t.bot.Self.UserName }

// NewHTTPTransport connects to the Telegram Bot API with token and returns the live
// transport. It validates the token with a getMe call, so it needs outbound network
// access to api.telegram.org; a bad token or no egress returns an error (serve then
// runs without the bot).
func NewHTTPTransport(token string) (Transport, error) {
	bot, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	return &httpTransport{bot: bot}, nil
}

// SetCommands installs the registry-derived top-level commands in Telegram's native
// slash menu. SDK types stay confined to this live adapter.
func (t *httpTransport) SetCommands(_ context.Context, commands []MenuCommand) error {
	items := nativeCommands(commands)
	if _, err := t.bot.Request(tgbotapi.NewSetMyCommands(items...)); err != nil {
		return fmt.Errorf("setMyCommands: %w", err)
	}
	return nil
}

func nativeCommands(commands []MenuCommand) []tgbotapi.BotCommand {
	items := make([]tgbotapi.BotCommand, 0, len(commands))
	for _, command := range commands {
		items = append(items, tgbotapi.BotCommand{Command: command.Command, Description: command.Description})
	}
	return items
}

// Updates long-polls getUpdates and maps each Telegram update onto our neutral Update
// type. The channel closes when ctx is cancelled or polling stops.
func (t *httpTransport) Updates(ctx context.Context) (<-chan Update, error) {
	cfg := tgbotapi.NewUpdate(0)
	cfg.Timeout = 30
	cfg.AllowedUpdates = []string{"message", "callback_query"}
	raw := t.bot.GetUpdatesChan(cfg)

	out := make(chan Update)
	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				t.bot.StopReceivingUpdates()
				return
			case u, ok := <-raw:
				if !ok {
					return
				}
				up, keep := mapUpdate(u)
				if !keep {
					continue
				}
				select {
				case out <- up:
				case <-ctx.Done():
					t.bot.StopReceivingUpdates()
					return
				}
			}
		}
	}()
	return out, nil
}

// mapUpdate projects a Telegram update onto our Update; keep is false for updates we
// don't handle (a message that is neither text nor a supported attachment: stickers,
// locations, service messages, …).
func mapUpdate(u tgbotapi.Update) (Update, bool) {
	switch {
	case u.Message != nil && u.Message.From != nil && attachment(u.Message) != nil:
		// A file/photo message: its caption (possibly empty) is the user's accompanying text.
		return Update{Message: &Message{
			ChatID: u.Message.Chat.ID,
			UserID: u.Message.From.ID,
			Text:   u.Message.Caption,
			File:   attachment(u.Message),
		}}, true
	case u.Message != nil && u.Message.Text != "" && u.Message.From != nil:
		return Update{Message: &Message{
			ChatID: u.Message.Chat.ID,
			UserID: u.Message.From.ID,
			Text:   u.Message.Text,
		}}, true
	case u.CallbackQuery != nil && u.CallbackQuery.From != nil:
		c := u.CallbackQuery
		var chatID int64
		if c.Message != nil {
			chatID = c.Message.Chat.ID
		}
		return Update{Callback: &Callback{
			ID:     c.ID,
			ChatID: chatID,
			UserID: c.From.ID,
			Data:   c.Data,
		}}, true
	}
	return Update{}, false
}

// attachment projects the file a message carries onto our neutral File, or nil when it
// carries none. Documents keep their sender-supplied name (untrusted — the engine sanitizes
// it before it becomes a path); a photo has no name, so one is derived from its unique id.
// Telegram delivers a photo in several sizes, largest last, and we take the largest.
func attachment(m *tgbotapi.Message) *File {
	switch {
	case m.Document != nil:
		d := m.Document
		return &File{ID: d.FileID, Name: d.FileName, MIME: d.MimeType, Size: int64(d.FileSize)}
	case len(m.Photo) > 0:
		p := m.Photo[len(m.Photo)-1]
		return &File{ID: p.FileID, Name: "photo-" + p.FileUniqueID + ".jpg", MIME: "image/jpeg", Size: int64(p.FileSize)}
	}
	return nil
}

// Download streams a file's content from Telegram's file endpoint. The direct URL embeds the
// bot token, so it is never put in an error or a log line.
func (t *httpTransport) Download(ctx context.Context, fileID string) (io.ReadCloser, error) {
	url, err := t.bot.GetFileDirectURL(fileID)
	if err != nil {
		return nil, fmt.Errorf("resolve file %s: %w", fileID, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("download file %s: %w", fileID, err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download file %s: %w", fileID, err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("download file %s: %s", fileID, resp.Status)
	}
	return resp.Body, nil
}

// sendAttempts bounds how many times one message is posted before its failure is returned.
// Only flood control is retried (retryAfter), and Telegram says how long to wait, so a small
// number is enough: three attempts survive a burst without letting a chat's output stall for
// minutes behind a limit that is not going to lift.
const sendAttempts = 3

// maxRetryAfter caps how long a flood-control hint is honored. Telegram's hint is normally a
// few seconds; a far larger one means the chat is rate-limited beyond what a turn should wait
// out, and returning the error is more useful than blocking the run's remaining output.
const maxRetryAfter = 30 * time.Second

// Send posts a message, attaching a single inline-keyboard row when buttons are given, and
// retries flood control with the delay Telegram asks for. The retry lives here because
// chunking (render.go) turns one long answer into several messages posted back to back,
// which is exactly what trips per-chat flood control — and a 429 is the one send failure that
// is expected to succeed unchanged a moment later.
func (t *httpTransport) Send(ctx context.Context, chatID int64, text string, buttons []Button) error {
	msg := tgbotapi.NewMessage(chatID, text)
	if len(buttons) > 0 {
		row := make([]tgbotapi.InlineKeyboardButton, len(buttons))
		for i, b := range buttons {
			row[i] = tgbotapi.NewInlineKeyboardButtonData(b.Text, b.Data)
		}
		msg.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(row)
	}
	for attempt := 1; ; attempt++ {
		_, err := t.bot.Send(msg)
		if err == nil {
			return nil
		}
		wait, retry := retryAfter(err)
		if !retry || attempt == sendAttempts {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
}

// retryAfter reports whether err is Telegram's flood-control response and how long it asks
// the bot to wait. Every other API failure — a blocked bot, a chat that no longer exists, a
// message Telegram refuses — is permanent: retrying it would only delay the error the caller
// needs to see. A 429 with no usable hint still gets a short default rather than an immediate
// re-post, which would just be refused again.
func retryAfter(err error) (time.Duration, bool) {
	var apiErr *tgbotapi.Error
	if !errors.As(err, &apiErr) || apiErr.Code != http.StatusTooManyRequests {
		return 0, false
	}
	wait := time.Duration(apiErr.RetryAfter) * time.Second
	if wait <= 0 {
		wait = time.Second
	}
	if wait > maxRetryAfter {
		return 0, false
	}
	return wait, true
}

// Answer acknowledges a callback (button press) with a short toast.
func (t *httpTransport) Answer(_ context.Context, callbackID, text string) error {
	_, err := t.bot.Request(tgbotapi.NewCallback(callbackID, text))
	return err
}
