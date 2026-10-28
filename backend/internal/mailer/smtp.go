// Package mailer sends login codes over plain SMTP (Mailpit locally).
package mailer

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/smtp"
)

type SMTP struct{ Addr, From string }

func (m SMTP) SendCode(_ context.Context, email, code string) error {
	msg := fmt.Sprintf("From: ai-tutor <%s>\r\nTo: %s\r\nSubject: =?UTF-8?B?%s?=\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\nВаш код входа в ai-tutor: %s\r\n\r\nКод одноразовый. Если вы не запрашивали вход, просто проигнорируйте письмо.\r\n",
		m.From, email, b64("Код входа в ai-tutor"), code)
	return smtp.SendMail(m.Addr, nil, m.From, []string{email}, []byte(msg))
}

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
