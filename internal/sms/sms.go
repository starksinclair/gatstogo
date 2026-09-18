// Package sms defines the interface this app's SMS touchpoints send
// through. LoggingSender keeps local/dev safe, while provider-backed
// senders such as Termii can be selected at startup without rewriting
// every call site.
package sms

import (
	"context"
	"log"
)

// Sender sends a text message to a phone number.
type Sender interface {
	Send(ctx context.Context, to, message string) error
}

// SenderIDSender is implemented by providers that can override the
// sender/from ID per message. SendWithSenderID falls back to Sender.Send
// for providers that do not support this.
type SenderIDSender interface {
	SendFrom(ctx context.Context, senderID, to, message string) error
}

func SendWithSenderID(ctx context.Context, sender Sender, senderID, to, message string) error {
	if senderID != "" {
		if withSenderID, ok := sender.(SenderIDSender); ok {
			return withSenderID.SendFrom(ctx, senderID, to, message)
		}
	}
	return sender.Send(ctx, to, message)
}

// LoggingSender logs the message that would have been sent instead of
// actually sending it. Safe to use in any environment (including
// production) precisely because it never contacts a real carrier or
// costs money -- but it also means a receipt lookup code never reaches a
// real customer's phone until a real Sender replaces this one; that code
// is only ever visible in the server's own logs, never returned to the
// client (see internal/receipts.CodeStore) -- an OTP code that stayed
// server-log-only was fine as a stopgap, but should not be treated as a
// substitute for real delivery before customers actually rely on it.
type LoggingSender struct{}

func NewLoggingSender() *LoggingSender { return &LoggingSender{} }

func (LoggingSender) Send(_ context.Context, to, message string) error {
	log.Printf("sms (not actually sent, no provider configured): to=%s message=%q", to, message)
	return nil
}

func (LoggingSender) SendFrom(_ context.Context, senderID, to, message string) error {
	log.Printf("sms (not actually sent, no provider configured): sender_id=%s to=%s message=%q", senderID, to, message)
	return nil
}
