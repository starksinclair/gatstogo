// Package email defines the interface this app's email touchpoints (so
// far, just a password-reset link) send through -- the same shape
// internal/sms already established for its own single touchpoint
// (LoggingSender as the always-safe default, a real provider as an
// explicit opt-in), so swapping providers later is a one-file change, not
// a rewrite of every call site.
package email

import (
	"context"
	"log"
)

// Sender sends a plain-text email.
type Sender interface {
	Send(ctx context.Context, to, subject, body string) error
}

// LoggingSender logs the message that would have been sent instead of
// actually sending it. Safe to use in any environment precisely because
// it never contacts a real provider or costs money -- the same
// stopgap-not-a-substitute caveat internal/sms.LoggingSender's own doc
// comment already gives: a password-reset link built with this sender
// never reaches a real owner's inbox, only the server's own logs, until a
// real Sender (ResendSender) replaces it.
type LoggingSender struct{}

func NewLoggingSender() *LoggingSender { return &LoggingSender{} }

func (LoggingSender) Send(_ context.Context, to, subject, body string) error {
	log.Printf("email (not actually sent, no provider configured): to=%s subject=%q body=%q", to, subject, body)
	return nil
}
