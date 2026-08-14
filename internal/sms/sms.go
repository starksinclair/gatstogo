// Package sms defines the interface this app's two SMS touchpoints (a
// receipt lookup one-time code, and -- eventually -- a payment
// confirmation notice) send through. No real provider is wired up yet;
// LoggingSender is the only implementation, so nothing is actually sent.
// The interface exists so swapping in a real provider (Termii, Africa's
// Talking, Twilio) later is a one-file change, not a rewrite of every
// call site.
package sms

import (
	"context"
	"log"
)

// Sender sends a text message to a phone number.
type Sender interface {
	Send(ctx context.Context, to, message string) error
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
