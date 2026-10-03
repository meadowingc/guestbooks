package main

import (
	"context"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"net/mail"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/spf13/viper"
)

var ErrMailDisabled = errors.New("email delivery is disabled")

type mailerConfig struct {
	provider string
	from     *mail.Address
	smtpHost string
	smtpPort string
	username string
	password string
	endpoint *url.URL
	key      []byte
}

type mailSnapshot struct {
	config     mailerConfig
	httpClient *http.Client
	publicURL  string
}

type mailSnapshotKey struct{}

func mailSnapshotForContext(ctx context.Context) (mailSnapshot, error) {
	if snapshot, ok := ctx.Value(mailSnapshotKey{}).(mailSnapshot); ok {
		return snapshot, nil
	}
	config, err := readMailerConfig()
	if err != nil {
		return mailSnapshot{}, err
	}
	return mailSnapshot{config: config, httpClient: mailHTTPClient, publicURL: PublicURL()}, nil
}

func validateMailerConfig() error {
	_, err := readMailerConfig()
	return err
}

func readMailerConfig() (mailerConfig, error) {
	config := mailerConfig{provider: viper.GetString("mailer.mailer_name")}
	switch config.provider {
	case "none":
		return config, nil
	case "smtp":
		config.smtpHost = viper.GetString("mailer.smtp.host")
		if !validMailHost(config.smtpHost) {
			return config, errors.New("mailer.smtp.host must be a hostname or IP address without a scheme or port")
		}
		config.smtpPort = viper.GetString("mailer.smtp.port")
		port, err := strconv.Atoi(config.smtpPort)
		if err != nil || port < 1 || port > 65535 {
			return config, errors.New("mailer.smtp.port must be an integer between 1 and 65535")
		}
		config.smtpPort = strconv.Itoa(port)
		config.username = viper.GetString("mailer.smtp.username")
		config.password = viper.GetString("mailer.smtp.password")
		if strings.TrimSpace(config.username) == "" || hasMailControl(config.username) {
			return config, errors.New("mailer.smtp.username is required and must not contain control characters")
		}
		if strings.TrimSpace(config.password) == "" || hasMailControl(config.password) {
			return config, errors.New("mailer.smtp.password is required and must not contain control characters")
		}
		config.from, err = parseMailAddress(viper.GetString("mailer.smtp.from_email"))
		if err != nil {
			return config, errors.New("mailer.smtp.from_email must be a valid sender address")
		}
	case "azure_communication_service":
		var err error
		config.endpoint, err = normalizeACSEndpoint(viper.GetString("mailer.azure_communication_service.host"))
		if err != nil {
			return config, err
		}
		config.key, err = base64.StdEncoding.DecodeString(viper.GetString("mailer.azure_communication_service.key"))
		if err != nil || len(config.key) == 0 {
			return config, errors.New("mailer.azure_communication_service.key must be a nonempty base64 access key")
		}
		config.from, err = parseMailAddress(viper.GetString("mailer.azure_communication_service.from_email"))
		if err != nil {
			return config, errors.New("mailer.azure_communication_service.from_email must be a valid sender address")
		}
	default:
		return config, errors.New("mailer.mailer_name must be none, smtp, or azure_communication_service")
	}
	return config, nil
}

func normalizeACSEndpoint(value string) (*url.URL, error) {
	invalid := errors.New("mailer.azure_communication_service.host must be an HTTPS origin or bare hostname")
	if value == "" || strings.TrimSpace(value) != value || hasMailControl(value) || strings.ContainsAny(value, "?#") {
		return nil, invalid
	}
	if !strings.Contains(value, "://") {
		value = "https://" + value
	}
	endpoint, err := url.Parse(value)
	if err != nil || endpoint.Scheme != "https" || endpoint.Opaque != "" || endpoint.User != nil ||
		!validMailHost(endpoint.Hostname()) || endpoint.RawQuery != "" || endpoint.ForceQuery ||
		endpoint.Fragment != "" || endpoint.RawFragment != "" ||
		(endpoint.Path != "" && endpoint.Path != "/") || endpoint.RawPath != "" {
		return nil, invalid
	}
	if port := endpoint.Port(); port != "" {
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 {
			return nil, invalid
		}
	} else if strings.HasSuffix(endpoint.Host, ":") {
		return nil, invalid
	}
	endpoint.Path = ""
	return endpoint, nil
}

func validMailHost(host string) bool {
	if host == "" || len(host) > 253 {
		return false
	}
	if net.ParseIP(host) != nil {
		return true
	}
	for _, label := range strings.Split(strings.TrimSuffix(host, "."), ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, char := range label {
			if !(char >= 'a' && char <= 'z') && !(char >= 'A' && char <= 'Z') &&
				!(char >= '0' && char <= '9') && char != '-' {
				return false
			}
		}
	}
	return true
}

func hasMailControl(value string) bool {
	return strings.ContainsFunc(value, unicode.IsControl)
}

func parseMailAddress(value string) (*mail.Address, error) {
	if hasMailControl(value) || !utf8.ValidString(value) {
		return nil, errors.New("email address contains control characters")
	}
	address, err := mail.ParseAddress(value)
	if err != nil {
		return nil, errors.New("invalid email address")
	}
	at := strings.LastIndexByte(address.Address, '@')
	if at < 1 || len(address.Address) > 254 || !validMailHost(address.Address[at+1:]) ||
		strings.ContainsFunc(address.Address, func(char rune) bool { return char > unicode.MaxASCII }) {
		return nil, errors.New("invalid email address")
	}
	return address, nil
}

func mailEnvelopeAddress(address *mail.Address) string {
	formatted := (&mail.Address{Address: address.Address}).String()
	return formatted[1 : len(formatted)-1]
}
