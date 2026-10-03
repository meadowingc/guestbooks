package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"
)

func buildSMTPMessage(from, to *mail.Address, subject, body string) ([]byte, error) {
	if hasMailControl(subject) || !utf8.ValidString(subject) || !utf8.ValidString(body) {
		return nil, errors.New("invalid mail content")
	}
	var message bytes.Buffer
	writeMailHeader(&message, "Date", []string{time.Now().UTC().Format(time.RFC1123Z)})
	domain := from.Address[strings.LastIndexByte(from.Address, '@')+1:]
	writeMailHeader(&message, "Message-ID", []string{"<" + rand.Text() + "@" + domain + ">"})
	writeMailHeader(&message, "From", mailAddressWords(from))
	writeMailHeader(&message, "To", mailAddressWords(to))
	writeMailHeader(&message, "Subject", encodedMailWords(subject))
	message.WriteString("MIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n")
	writer := quotedprintable.NewWriter(&message)
	// Normalize lone CR as well as LF; quoted-printable applies SMTP-safe wrapping.
	body = strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\r", "\n")
	if _, err := writer.Write([]byte(body)); err != nil {
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	if !bytes.HasSuffix(message.Bytes(), []byte("\r\n")) {
		message.WriteString("\r\n")
	}
	return message.Bytes(), nil
}

func mailAddressWords(address *mail.Address) []string {
	words := encodedMailWords(address.Name)
	return append(words, "<"+mailEnvelopeAddress(address)+">")
}

func encodedMailWords(value string) []string {
	var words []string
	for len(value) > 0 {
		end := min(len(value), 42)
		for end < len(value) && !utf8.RuneStart(value[end]) {
			end--
		}
		words = append(words, "=?UTF-8?B?"+base64.StdEncoding.EncodeToString([]byte(value[:end]))+"?=")
		value = value[end:]
	}
	return words
}

func writeMailHeader(message *bytes.Buffer, name string, words []string) {
	message.WriteString(name + ":")
	column := len(name) + 1
	for _, word := range words {
		if column+1+len(word) > 78 {
			message.WriteString("\r\n ")
			column = 1
		} else {
			message.WriteByte(' ')
			column++
		}
		message.WriteString(word)
		column += len(word)
	}
	message.WriteString("\r\n")
}

type mailDeadlineConn struct {
	net.Conn
	deadline time.Time
}

func (conn mailDeadlineConn) bounded(deadline time.Time) time.Time {
	if deadline.IsZero() || deadline.After(conn.deadline) {
		return conn.deadline
	}
	return deadline
}

func (conn mailDeadlineConn) SetDeadline(deadline time.Time) error {
	return conn.Conn.SetDeadline(conn.bounded(deadline))
}

func (conn mailDeadlineConn) SetReadDeadline(deadline time.Time) error {
	return conn.Conn.SetReadDeadline(conn.bounded(deadline))
}

func (conn mailDeadlineConn) SetWriteDeadline(deadline time.Time) error {
	return conn.Conn.SetWriteDeadline(conn.bounded(deadline))
}

func sendSMTP(ctx context.Context, config mailerConfig, recipient *mail.Address, subject, body string) error {
	message, err := buildSMTPMessage(config.from, recipient, subject, body)
	if err != nil {
		return mailFailure(ctx, "smtp", "prepare", "invalid_input", 0, err)
	}
	return sendSMTPMessage(ctx, config, mailEnvelopeAddress(recipient), message, &tls.Config{
		ServerName: config.smtpHost, MinVersion: tls.VersionTLS12,
	})
}

func smtpFailure(ctx context.Context, phase string, cause error) error {
	category, status := "transport", 0
	if phase == "tls" {
		category = "tls"
	}
	var response *smtp.SMTPError
	if errors.As(cause, &response) {
		status = response.Code
		switch {
		case status == 530 || status == 534 || status == 535 || status == 538:
			category = "authentication"
		case status >= 400 && status < 500:
			category = "temporary_rejection"
		case status >= 500 && status < 600:
			category = "permanent_rejection"
		default:
			category = "invalid_response"
		}
	}
	return mailFailure(ctx, "smtp", phase, category, status, cause)
}

func sendSMTPMessage(ctx context.Context, config mailerConfig, recipient string, message []byte, tlsConfig *tls.Config) error {
	ctx, cancel := context.WithTimeout(ctx, mailSendTimeout)
	defer cancel()
	connection, err := (&net.Dialer{Timeout: mailSendTimeout}).DialContext(ctx, "tcp", net.JoinHostPort(config.smtpHost, config.smtpPort))
	if err != nil {
		return smtpFailure(ctx, "connect", err)
	}
	defer connection.Close()
	stop := context.AfterFunc(ctx, func() { _ = connection.Close() })
	defer stop()
	deadline := time.Now().Add(mailSendTimeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	bounded := mailDeadlineConn{connection, deadline}
	if err := bounded.SetDeadline(deadline); err != nil {
		return smtpFailure(ctx, "connect", err)
	}
	var client *smtp.Client
	if config.smtpPort == "465" {
		secure := tls.Client(bounded, tlsConfig)
		if err := secure.HandshakeContext(ctx); err != nil {
			return smtpFailure(ctx, "tls", err)
		}
		client = smtp.NewClient(secure)
	} else {
		client, err = smtp.NewClientStartTLS(bounded, tlsConfig)
		if err != nil {
			return smtpFailure(ctx, "tls", err)
		}
	}
	defer client.Close()
	client.CommandTimeout, client.SubmissionTimeout = mailSendTimeout, mailSendTimeout
	if err := client.Auth(sasl.NewLoginClient(config.username, config.password)); err != nil {
		return smtpFailure(ctx, "auth", err)
	}
	if err := client.SendMail(mailEnvelopeAddress(config.from), []string{recipient}, bytes.NewReader(message)); err != nil {
		return smtpFailure(ctx, "submit", err)
	}
	// The DATA response is authoritative; a failed QUIT must not invite a duplicate send.
	return nil
}
