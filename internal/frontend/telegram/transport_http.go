package telegram

import (
	"context"
	"fmt"
	"io"
	"net/http"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// httpTransport is the live Telegram Bot API transport (long-poll getUpdates for
// inbound messages/callbacks; sendMessage / answerCallbackQuery for outbound). It
// adapts the tgbotapi SDK to the Transport interface the Bot logic is written against.
type httpTransport struct {
	bot *tgbotapi.BotAPI
}

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

// Send posts a message, attaching a single inline-keyboard row when buttons are given.
func (t *httpTransport) Send(_ context.Context, chatID int64, text string, buttons []Button) error {
	msg := tgbotapi.NewMessage(chatID, text)
	if len(buttons) > 0 {
		row := make([]tgbotapi.InlineKeyboardButton, len(buttons))
		for i, b := range buttons {
			row[i] = tgbotapi.NewInlineKeyboardButtonData(b.Text, b.Data)
		}
		msg.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(row)
	}
	_, err := t.bot.Send(msg)
	return err
}

// Answer acknowledges a callback (button press) with a short toast.
func (t *httpTransport) Answer(_ context.Context, callbackID, text string) error {
	_, err := t.bot.Request(tgbotapi.NewCallback(callbackID, text))
	return err
}
