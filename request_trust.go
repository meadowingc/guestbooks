package main

import (
	"crypto/rand"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"

	"guestbook/constants"

	"github.com/gorilla/csrf"
)

var csrfKey = func() []byte {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		panic(err)
	}
	return key
}()

func adminCSRF(next http.Handler) http.Handler {
	secure := strings.HasPrefix(PublicURL(), "https://")
	protected := csrf.Protect(csrfKey, csrf.Secure(secure), csrf.Path("/"),
		csrf.SameSite(csrf.SameSiteLaxMode),
		csrf.ErrorHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "This form could not be verified. Reload the page and try again.", http.StatusForbidden)
		})),
	)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-CSRF-Token", csrf.Token(r))
		next.ServeHTTP(w, r)
	}))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin, _ := url.Parse(PublicURL())
		if constants.DEBUG_MODE {
			origin = &url.URL{Scheme: "http", Host: r.Host}
			if r.TLS != nil {
				origin.Scheme = "https"
			}
		}
		mutation := r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions
		if mutation {
			for _, header := range []string{"Origin", "Referer"} {
				value := r.Header.Get(header)
				if value == "" {
					continue
				}
				source, err := url.Parse(value)
				if err != nil || source.User != nil || !sameOrigin(source, origin) {
					http.Error(w, "CSRF origin check failed", http.StatusForbidden)
					return
				}
				if header == "Origin" && (source.Path != "" || source.RawQuery != "" || source.Fragment != "") {
					http.Error(w, "CSRF origin check failed", http.StatusForbidden)
					return
				}
			}
		}
		// Origin checks above use the external origin, not the proxy's HTTP hop.
		// The token check remains mandatory, including when both headers are absent.
		copy := r.Clone(r.Context())
		copy.URL.Scheme, copy.URL.Host = origin.Scheme, origin.Host
		copy.Host = origin.Host
		if mutation {
			// Supplied headers were validated above. Preserve token-only automation
			// while giving the library the canonical external origin.
			copy.Header.Set("Origin", origin.Scheme+"://"+origin.Host)
		}
		if origin.Scheme == "http" {
			copy = csrf.PlaintextHTTPRequest(copy)
		}
		protected.ServeHTTP(w, copy)
	})
}

func sameOrigin(a, b *url.URL) bool {
	port := func(u *url.URL) string {
		if u.Port() != "" {
			return u.Port()
		}
		if strings.EqualFold(u.Scheme, "https") {
			return "443"
		}
		return "80"
	}
	return strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Hostname(), b.Hostname()) && port(a) == port(b)
}

func trustedProxy(address netip.Addr) bool {
	for _, prefix := range appConfig.TrustedProxies {
		if prefix.Contains(address.Unmap()) {
			return true
		}
	}
	return false
}

func clientAddress(r *http.Request) netip.Addr {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}
	}
	peer = peer.Unmap()
	if !trustedProxy(peer) {
		return peer
	}
	chain := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
	current := peer
	for i := len(chain) - 1; i >= 0 && trustedProxy(current); i-- {
		address, err := netip.ParseAddr(strings.TrimSpace(chain[i]))
		if err != nil {
			return peer
		}
		current = address.Unmap()
	}
	return current
}
