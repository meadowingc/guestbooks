package main

import (
	"fmt"
	"html/template"
	"log"
	"net/netip"
	"net/url"
	"strconv"
	"strings"

	"github.com/spf13/viper"
)

// runtimeConfig holds values loaded from config.yaml at startup. Values are
// read once via initRuntimeConfig() and exposed through small accessor
// functions so call sites stay terse.
type runtimeConfig struct {
	PublicURL      string
	Port           int
	BindHost       string
	TrustedProxies []netip.Prefix
	AllowSignups   bool
	ShowCredits    bool
	SupportURL     string
	ExtraHeadHTML  template.HTML
}

var appConfig runtimeConfig

// initRuntimeConfig populates appConfig from viper. Call after
// viper.ReadInConfig(). Defaults are chosen so a fresh clone with a minimal
// config.yaml runs as a clean, unbranded, self-hosted instance.
func initRuntimeConfig() error {
	viper.SetDefault("server.port", 6235)
	viper.SetDefault("server.public_url", "")
	viper.SetDefault("server.bind_host", "")
	viper.SetDefault("server.trusted_proxies", []string{})
	viper.SetDefault("mailer.mailer_name", "none")
	viper.SetDefault("admin.allow_signups", true)
	viper.SetDefault("branding.show_credits", false)
	viper.SetDefault("branding.support_url", "")
	viper.SetDefault("templates.extra_head_html", "")

	port, err := strconv.Atoi(strings.TrimSpace(viper.GetString("server.port")))
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("server.port must be an integer from 1 to 65535")
	}
	bindHost := strings.TrimSpace(viper.GetString("server.bind_host"))
	if bindHost != "" && bindHost != "localhost" {
		if _, err := netip.ParseAddr(bindHost); err != nil {
			return fmt.Errorf("server.bind_host must be an IP address or localhost")
		}
	}
	var proxies []netip.Prefix
	for _, value := range viper.GetStringSlice("server.trusted_proxies") {
		prefix, err := netip.ParsePrefix(strings.TrimSpace(value))
		if err != nil {
			return fmt.Errorf("server.trusted_proxies must contain valid IP CIDRs")
		}
		proxies = append(proxies, prefix.Masked())
	}
	config := runtimeConfig{
		PublicURL:      strings.TrimSuffix(strings.TrimSpace(viper.GetString("server.public_url")), "/"),
		Port:           port,
		BindHost:       bindHost,
		TrustedProxies: proxies,
		AllowSignups:   viper.GetBool("admin.allow_signups"),
		ShowCredits:    viper.GetBool("branding.show_credits"),
		SupportURL:     strings.TrimSpace(viper.GetString("branding.support_url")),
		ExtraHeadHTML:  template.HTML(viper.GetString("templates.extra_head_html")),
	}

	if config.PublicURL == "" {
		// Fall back to a localhost URL so dev/test runs work without
		// requiring config. Warn loudly so self-hosters notice.
		config.PublicURL = fmt.Sprintf("http://localhost:%d", config.Port)
		log.Printf("WARN: server.public_url not set in config.yaml; defaulting to %s. "+
			"Set server.public_url to your public origin (e.g. https://guestbooks.example.com) "+
			"so emails and CSRF checks use the correct URL.", config.PublicURL)
	}

	origin, err := url.Parse(config.PublicURL)
	if err != nil || origin.Hostname() == "" || origin.User != nil || (origin.Scheme != "http" && origin.Scheme != "https") || origin.Path != "" || origin.RawQuery != "" || origin.ForceQuery || origin.Fragment != "" {
		return fmt.Errorf("server.public_url must be an absolute HTTP(S) origin without a path, credentials, query, or fragment")
	}
	if value := origin.Port(); value != "" {
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 || n > 65535 {
			return fmt.Errorf("server.public_url has an invalid port")
		}
	}
	if err := validateMailerConfig(); err != nil {
		return err
	}
	appConfig = config
	if config.AllowSignups {
		log.Printf("WARN: admin.allow_signups is enabled — anyone who can reach this " +
			"instance can register an account. Create your user, then set " +
			"admin.allow_signups: false in config.yaml and restart.")
	}
	return nil
}

// PublicURL returns the configured public origin (no trailing slash).
func PublicURL() string { return appConfig.PublicURL }

// templateCommon is embedded in template data so layout.html can reach
// .Branding and .ExtraHeadHTML on every page.
type templateCommon struct {
	Branding      brandingData
	ExtraHeadHTML template.HTML
}

type brandingData struct {
	ShowCredits bool
	SupportURL  string
}

func currentTemplateCommon() templateCommon {
	return templateCommon{
		Branding: brandingData{
			ShowCredits: appConfig.ShowCredits,
			SupportURL:  appConfig.SupportURL,
		},
		ExtraHeadHTML: appConfig.ExtraHeadHTML,
	}
}
