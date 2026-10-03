package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

const mailSendTimeout = 15 * time.Second

type mailRecipientError struct {
	recipient int
	cause     error
}

func (err *mailRecipientError) Error() string {
	return fmt.Sprintf("%s recipient=%d", safeMailError(err.cause), err.recipient)
}

func (err *mailRecipientError) Unwrap() error { return err.cause }

type mailProviderError struct {
	provider string
	phase    string
	category string
	status   int
	cause    error
}

func allowedMailDiagnostic(value string, allowed ...string) string {
	for _, candidate := range allowed {
		if value == candidate {
			return value
		}
	}
	return "unknown"
}

func (err *mailProviderError) Error() string {
	diagnostic := fmt.Sprintf("delivery failed provider=%s phase=%s category=%s",
		allowedMailDiagnostic(err.provider, "smtp", "azure_communication_service", "none"),
		allowedMailDiagnostic(err.phase, "prepare", "connect", "tls", "auth", "submit", "request", "response"),
		allowedMailDiagnostic(err.category, "disabled", "shutting_down", "canceled", "timeout",
			"invalid_input", "transport", "tls", "authentication", "rate_limited",
			"temporary_rejection", "permanent_rejection", "provider_failure", "invalid_response"))
	if err.status >= 100 && err.status <= 599 {
		diagnostic += fmt.Sprintf(" status=%d", err.status)
	}
	return diagnostic
}

func (err *mailProviderError) Unwrap() error { return err.cause }

func mailFailure(ctx context.Context, provider, phase, category string, status int, cause error) error {
	if ctx.Err() != nil && !errors.Is(cause, ctx.Err()) {
		cause = errors.Join(cause, ctx.Err())
	}
	var timeout net.Error
	switch {
	case errors.Is(cause, context.Canceled):
		category = "canceled"
	case errors.Is(cause, context.DeadlineExceeded), errors.As(cause, &timeout) && timeout.Timeout():
		category = "timeout"
	}
	return &mailProviderError{provider: provider, phase: phase, category: category, status: status, cause: cause}
}

// Only our diagnostic fields are printable; wrappers and transport messages may contain secrets.
func safeMailError(err error) string {
	switch value := err.(type) {
	case *mailRecipientError:
		return value.Error()
	case *mailProviderError:
		return value.Error()
	case interface{ Unwrap() []error }:
		var diagnostics []string
		for _, child := range value.Unwrap() {
			if child != nil {
				diagnostics = append(diagnostics, safeMailError(child))
			}
		}
		return strings.Join(diagnostics, "; ")
	case interface{ Unwrap() error }:
		return safeMailError(value.Unwrap())
	}
	category := "unknown"
	switch {
	case errors.Is(err, ErrMailDisabled):
		category = "disabled"
	case errors.Is(err, ErrMailShuttingDown):
		category = "shutting_down"
	}
	return mailFailure(context.Background(), "", "", category, 0, err).Error()
}

func SendMail(recipients []string, subject, body string) error {
	return SendMailContext(context.Background(), recipients, subject, body)
}

// SendMailContext sends separately to each recipient. ACS acceptance is not a delivery receipt.
func SendMailContext(ctx context.Context, recipients []string, subject, body string) error {
	snapshot, err := mailSnapshotForContext(ctx)
	if err != nil {
		return err
	}
	config := snapshot.config
	if config.provider == "none" {
		return ErrMailDisabled
	}
	if len(recipients) == 0 {
		return errors.New("mail requires at least one recipient")
	}
	if hasMailControl(subject) || !utf8.ValidString(subject) || !utf8.ValidString(body) {
		return errors.New("mail requires a UTF-8 body and a single-line UTF-8 subject")
	}
	ctx, cancel := context.WithTimeout(ctx, mailSendTimeout)
	defer cancel()
	var failures []error
	for index, recipient := range recipients {
		address, sendErr := parseMailAddress(recipient)
		if sendErr != nil {
			sendErr = mailFailure(ctx, config.provider, "prepare", "invalid_input", 0, sendErr)
		} else if ctx.Err() != nil {
			sendErr = mailFailure(ctx, config.provider, "prepare", "canceled", 0, ctx.Err())
		}
		if sendErr == nil {
			switch config.provider {
			case "smtp":
				sendErr = sendSMTP(ctx, config, address, subject, body)
			case "azure_communication_service":
				sendErr = sendACS(ctx, snapshot.httpClient, config, mailEnvelopeAddress(address), subject, body, time.Now())
			}
		}
		if sendErr != nil {
			failures = append(failures, &mailRecipientError{index + 1, sendErr})
		}
	}
	return errors.Join(failures...)
}

func SendVerificationEmail(recipient, token string) error {
	return SendVerificationEmailContext(context.Background(), recipient, token)
}

func SendVerificationEmailContext(ctx context.Context, recipient, token string) error {
	snapshot, err := mailSnapshotForContext(ctx)
	if err != nil {
		return err
	}
	ctx = context.WithValue(ctx, mailSnapshotKey{}, snapshot)
	subject := "[Guestbooks] Please verify your email address"
	verificationLink := snapshot.publicURL + "/verify-email?token=" + url.QueryEscape(token)
	body := fmt.Sprintf("Please click on the following link to verify your email address: %s", verificationLink)
	return SendMailContext(ctx, []string{recipient}, subject, body)
}

func SendPasswordResetEmail(recipient, token string) error {
	return SendPasswordResetEmailContext(context.Background(), recipient, token)
}

func SendPasswordResetEmailContext(ctx context.Context, recipient, token string) error {
	snapshot, err := mailSnapshotForContext(ctx)
	if err != nil {
		return err
	}
	ctx = context.WithValue(ctx, mailSnapshotKey{}, snapshot)
	subject := "[Guestbooks] Password Reset Request"
	resetLink := snapshot.publicURL + "/reset-password?token=" + url.QueryEscape(token)
	body := fmt.Sprintf(`Hello,

You recently requested to reset your password for your Guestbooks account.
Click the link below to reset it:

%s

If you did not request a password reset, please ignore this email or contact support if you have concerns.

This password reset link is only valid for 24 hours.

Regards,
The Guestbooks Team`, resetLink)
	return SendMailContext(ctx, []string{recipient}, subject, body)
}
