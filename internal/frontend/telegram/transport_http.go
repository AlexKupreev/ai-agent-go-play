package telegram

import "errors"

// ErrTransportNotBuilt is returned by NewHTTPTransport until the live Telegram Bot
// API transport is implemented. serve treats it as "run without the bot" so a
// configured token never breaks the engine.
var ErrTransportNotBuilt = errors.New("live transport not built yet")

// NewHTTPTransport is the seam for the production transport that talks to the
// Telegram Bot API (long-poll getUpdates for inbound messages/callbacks; sendMessage
// / answerCallbackQuery for outbound). It is intentionally unimplemented: the live
// bot is added later (it needs the Bot API client and outbound network access — see
// the package doc on egress). The rest of the frontend — Bot logic, allowlist,
// approval keyboards — is complete and tested against a fake Transport, so filling
// this in is the only remaining step to activate the bot.
//
// Wiring already routes a configured token here (cmd/serve.go); the day this returns
// a working Transport instead of ErrTransportNotBuilt, supplying a token activates
// the bot with no other change.
func NewHTTPTransport(token string) (Transport, error) {
	_ = token
	return nil, ErrTransportNotBuilt
}
