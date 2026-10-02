package main

import (
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	"guestbook/constants"
)

type SubmissionAction string

const (
	SubmissionUnchanged SubmissionAction = ""
	SubmissionMessage   SubmissionAction = "message"
	SubmissionRedirect  SubmissionAction = "redirect"

	maxConfirmationLength = 2000
	maxRedirectBytes      = 2048
	maxEmailLabelLength   = 120
	maxEmailHelpLength    = 500
	maxEmailBytes         = 254
)

func readGuestbookOptions(r *http.Request, guestbook *Guestbook) error {
	if err := r.ParseForm(); err != nil {
		return errors.New("invalid guestbook settings form")
	}
	// Older forms must not reset settings they do not contain.
	if r.PostForm.Has("submissionAction") {
		guestbook.SubmissionAction = SubmissionAction(r.PostForm.Get("submissionAction"))
		if r.PostForm.Has("submissionMessage") {
			guestbook.SubmissionMessage = strings.TrimSpace(r.PostForm.Get("submissionMessage"))
		}
		if r.PostForm.Has("submissionRedirectURL") {
			guestbook.SubmissionRedirectURL = strings.TrimSpace(r.PostForm.Get("submissionRedirectURL"))
		}
	}
	if r.PostForm.Has("emailCollectionSettings") || r.PostForm.Has("emailFieldLabel") || r.PostForm.Has("collectEmail") {
		guestbook.CollectEmail = r.PostForm.Get("collectEmail") == "on"
		if r.PostForm.Has("emailFieldLabel") {
			guestbook.EmailFieldLabel = strings.TrimSpace(r.PostForm.Get("emailFieldLabel"))
		}
		if r.PostForm.Has("emailFieldHelp") {
			guestbook.EmailFieldHelp = strings.TrimSpace(r.PostForm.Get("emailFieldHelp"))
		}
	}

	for _, field := range []struct {
		name  string
		value string
		limit int
	}{
		{"Confirmation text", guestbook.SubmissionMessage, maxConfirmationLength},
		{"Email field label", guestbook.EmailFieldLabel, maxEmailLabelLength},
		{"Email field help", guestbook.EmailFieldHelp, maxEmailHelpLength},
	} {
		if !utf8.ValidString(field.value) || utf8.RuneCountInString(field.value) > field.limit {
			return fmt.Errorf("%s must contain at most %d characters", field.name, field.limit)
		}
	}
	if len(guestbook.SubmissionRedirectURL) > maxRedirectBytes {
		return fmt.Errorf("redirect URL must contain at most %d bytes", maxRedirectBytes)
	}
	switch guestbook.SubmissionAction {
	case SubmissionUnchanged:
	case SubmissionMessage:
		if guestbook.SubmissionMessage == "" {
			return errors.New("enter a confirmation message")
		}
	case SubmissionRedirect:
		if _, err := validatedRedirect(r, guestbook.SubmissionRedirectURL, false); err != nil {
			return err
		}
	default:
		return errors.New("unknown submission action")
	}
	if guestbook.CollectEmail && guestbook.EmailFieldLabel == "" {
		return errors.New("enter a label for the optional email field")
	}
	return nil
}

func validatedRedirect(r *http.Request, value string, allowRelative bool) (string, error) {
	if value == "" || len(value) > maxRedirectBytes || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return "", fmt.Errorf("redirect URL must be a valid HTTP(S) URL of at most %d bytes", maxRedirectBytes)
	}
	target, err := url.Parse(value)
	if err != nil {
		return "", errors.New("invalid redirect URL")
	}
	if !target.IsAbs() {
		if !allowRelative {
			return "", errors.New("enter an absolute HTTP(S) redirect URL")
		}
		base, err := url.Parse(PublicURL())
		if err != nil {
			return "", errors.New("the guestbook public URL is invalid")
		}
		if constants.DEBUG_MODE {
			base.Scheme = "http"
			if r.TLS != nil {
				base.Scheme = "https"
			}
			base.Host = r.Host
		}
		base = base.ResolveReference(r.URL)
		target = base.ResolveReference(target)
	}
	if (target.Scheme != "http" && target.Scheme != "https") || target.Hostname() == "" || target.User != nil {
		return "", errors.New("redirect URL must use HTTP(S), with a host and no embedded credentials")
	}
	if len(target.String()) > maxRedirectBytes {
		return "", fmt.Errorf("redirect URL must contain at most %d bytes", maxRedirectBytes)
	}
	return target.String(), nil
}

func optionalEmail(value string) (*string, error) {
	if strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return nil, errors.New("enter a single email address without control characters")
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	if len(value) > maxEmailBytes {
		return nil, fmt.Errorf("email address must contain at most %d bytes", maxEmailBytes)
	}
	address, err := mail.ParseAddress(value)
	if err != nil || address.Name != "" || address.Address != value {
		return nil, errors.New("enter a single email address, without a display name")
	}
	return &value, nil
}
