package main

import (
	"fmt"
	"html/template"
	"log"
	"strings"

	"github.com/spf13/viper"
)

// runtimeConfig holds values loaded from config.yaml at startup. Values are
// read once via initRuntimeConfig() and exposed through small accessor
// functions so call sites stay terse.
type runtimeConfig struct {
	PublicURL     string
	Port          int
	AllowSignups  bool
	ShowCredits   bool
	SupportURL    string
	ExtraHeadHTML template.HTML
}

var appConfig runtimeConfig

// initRuntimeConfig populates appConfig from viper. Call after
// viper.ReadInConfig(). Defaults are chosen so a fresh clone with a minimal
// config.yaml runs as a clean, unbranded, self-hosted instance.
func initRuntimeConfig() {
	viper.SetDefault("server.port", 6235)
	viper.SetDefault("server.public_url", "")
	viper.SetDefault("admin.allow_signups", true)
	viper.SetDefault("branding.show_credits", false)
	viper.SetDefault("branding.support_url", "")
	viper.SetDefault("templates.extra_head_html", "")

	appConfig = runtimeConfig{
		PublicURL:     strings.TrimRight(viper.GetString("server.public_url"), "/"),
		Port:          viper.GetInt("server.port"),
		AllowSignups:  viper.GetBool("admin.allow_signups"),
		ShowCredits:   viper.GetBool("branding.show_credits"),
		SupportURL:    strings.TrimSpace(viper.GetString("branding.support_url")),
		ExtraHeadHTML: template.HTML(viper.GetString("templates.extra_head_html")),
	}

	if appConfig.PublicURL == "" {
		// Fall back to a localhost URL so dev/test runs work without
		// requiring config. Warn loudly so self-hosters notice.
		appConfig.PublicURL = fmt.Sprintf("http://localhost:%d", appConfig.Port)
		log.Printf("WARN: server.public_url not set in config.yaml; defaulting to %s. "+
			"Set server.public_url to your public origin (e.g. https://guestbooks.example.com) "+
			"so emails and CSRF checks use the correct URL.", appConfig.PublicURL)
	}

	if appConfig.AllowSignups {
		log.Printf("WARN: admin.allow_signups is enabled — anyone who can reach this " +
			"instance can register an account. Create your user, then set " +
			"admin.allow_signups: false in config.yaml and restart.")
	}
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
