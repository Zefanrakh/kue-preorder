package otp

import (
	"context"
	"log/slog"

	"github.com/Zefanrakh/kue-preorder/internal/platform/whatsapp"
)

// WhatsAppSender sends codes with an approved authentication template.
type WhatsAppSender struct {
	Client   *whatsapp.Client
	Template string // the template's name in WhatsApp Manager
}

// SendCode implements Sender.
func (s WhatsAppSender) SendCode(ctx context.Context, phone, code string) error {
	_, err := s.Client.SendAuthCode(ctx, phone, s.Template, code)
	return err
}

// LogSender writes codes to the log instead of sending them, so development
// works without a WhatsApp account. Production refuses to start with it:
// a code in a log is a code anyone reading logs can use.
type LogSender struct {
	Logger *slog.Logger
}

// SendCode implements Sender.
func (s LogSender) SendCode(ctx context.Context, phone, code string) error {
	s.Logger.InfoContext(ctx, "sign-in code (development only, not sent)", slog.String("phone", phone), slog.String("code", code))
	return nil
}
