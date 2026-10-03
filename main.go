package main

import (
	"encoding/json"
	"fmt"
	"guestbook/constants"
	"log"
	"net/http"
	"os"
	"strconv"
	textTemplate "text/template"
	"time"

	"github.com/fatih/color"
	"github.com/go-chi/cors"
	"github.com/spf13/viper"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/go-chi/chi/middleware"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/httprate"

	"gorm.io/driver/sqlite"
)

var db *gorm.DB
var messageCache *MessageCache
var databaseLogger = logger.New(log.Default(), logger.Config{
	LogLevel:             logger.Warn,
	SlowThreshold:        200 * time.Millisecond,
	ParameterizedQueries: true,
})

func main() {
	if err := runApplication(); err != nil {
		log.Printf("Application stopped: %v", err)
		os.Exit(1)
	}
}

func loadConfiguration() error {
	viper.SetConfigName("config")
	viper.AddConfigPath(".")
	err := viper.ReadInConfig()
	if err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); ok {
			return fmt.Errorf("config.yaml not found in the working directory. " +
				"Copy config.example.yaml to config.yaml and edit it before starting. " +
				"For Docker, mount it with `-v $(pwd)/config.yaml:/app/config.yaml:ro`")
		}
		return fmt.Errorf("read configuration: %w", err)
	}
	return initRuntimeConfig()
}

func initDatabase() error {
	var err error
	db, err = gorm.Open(sqlite.Open("file:guestbook.db?mode=rwc&_journal_mode=WAL&_busy_timeout=5000"), &gorm.Config{Logger: databaseLogger})
	if err != nil {
		return fmt.Errorf("connect database: %w", err)
	}

	// Migrate the schema
	err = db.AutoMigrate(&Guestbook{}, &Message{}, &AdminUser{})
	if err != nil {
		return fmt.Errorf("migrate database: %w", err)
	}
	return nil
}

func initCache() error {
	var err error
	// Initialize cache with 1000 entries and 10 minute TTL
	messageCache, err = NewMessageCache(1000, 10*time.Minute)
	if err != nil {
		return fmt.Errorf("initialize cache: %w", err)
	}
	log.Println("Message cache initialized (size: 1000, TTL: 10m)")
	return nil
}

func initRouter() *chi.Mux {

	r := chi.NewRouter()

	CORSMiddleware := cors.New(cors.Options{
		AllowedOrigins:   []string{"*"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "X-CSRF-Token"},
		ExposedHeaders:   []string{"Link", "X-Guestbooks-Fresh-Proof"},
		AllowCredentials: false,
		MaxAge:           300,
	})

	r.Use(CORSMiddleware.Handler)
	r.Use(RealIPMiddleware)
	r.Use(Logger)
	r.Use(func(next http.Handler) http.Handler {
		limited := httprate.LimitByIP(100, time.Minute)(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/healthz" {
				next.ServeHTTP(w, r)
			} else {
				limited.ServeHTTP(w, r)
			}
		})
	})
	r.Use(middleware.Recoverer)
	r.Get("/healthz", HealthHandler)

	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		renderAdminTemplate(w, r, "landing_page", nil)
	})

	r.Get("/verify-email", VerifyEmailHandler)
	r.With(adminCSRF).Get("/reset-password", ResetPasswordFormHandler)
	r.With(adminCSRF).Post("/reset-password", ResetPasswordHandler)

	r.Get("/terms-and-conditions", func(w http.ResponseWriter, r *http.Request) {
		renderAdminTemplate(w, r, "terms_and_conditions", nil)
	})

	r.With(adminCSRF).Get("/forgot-password", ForgotPasswordHandler)
	r.With(adminCSRF).Post("/forgot-password", ForgotPasswordHandler)

	r.With(adminCSRF, AdminAuthMiddleware).Route("/admin", func(r chi.Router) {
		r.Get("/", AdminGuestbookList)
		r.Get("/settings", AdminUserSettings)

		r.Post("/settings", AdminUserSettings)
		r.Post("/settings/resend-verification", AdminResendVerification)
		r.Post("/change-password", AdminChangePassword)

		r.Get("/signin", AdminSignIn)
		r.Post("/signin", AdminSignIn)

		if appConfig.AllowSignups {
			r.Get("/signup", AdminSignUp)
			r.Post("/signup", AdminSignUp)
		}

		r.Post("/logout", AdminLogout)

		r.Get("/guestbook/new", AdminCreateGuestbook)
		r.Post("/guestbook/new", AdminCreateGuestbook)

		r.Route("/guestbook/{guestbookID}", func(r chi.Router) {
			r.Use(validRouteIDs)
			r.Get("/", AdminShowGuestbook)
			r.Get("/embed", AdminEmbedGuestbook)

			r.Get("/edit", AdminEditGuestbook)
			r.Post("/edit", AdminUpdateGuestbook)

			r.Post("/delete", AdminDeleteGuestbook)

			r.Post("/messages/bulk-delete", AdminBulkDeleteMessages)
			r.Post("/messages/bulk-approve", AdminBulkApproveMessages)

			r.Route("/message/{messageID}", func(r chi.Router) {
				r.Use(validRouteIDs)
				r.Get("/edit", AdminEditMessage)
				r.Post("/edit", AdminEditMessage)
				r.Post("/delete", AdminDeleteMessage)
				r.Post("/reply", AdminReplyToMessage)
			})
		})
	})

	r.Route("/guestbook", func(r chi.Router) {
		r.With(validRouteIDs).Get("/{guestbookID}", GuestbookPage)

		// this means the user has at most N attempts to submit a message to a given guestbook in a minute
		submitRateLimiter := httprate.Limit(
			5,           // requests
			time.Minute, // per duration
			httprate.WithKeyFuncs(httprate.KeyByIP, func(r *http.Request) (string, error) {
				id, err := parsePositiveID(chi.URLParam(r, "guestbookID"))
				return strconv.FormatUint(uint64(id), 10), err
			}),
			httprate.WithLimitHandler(func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, `Rate limited. Please slow down.`, http.StatusTooManyRequests)
			}),
		)

		r.With(validRouteIDs, submitRateLimiter).
			Post("/{guestbookID}/submit", GuestbookSubmit)
	})

	fileServer := http.FileServer(http.Dir("./assets"))
	r.Handle("/assets/*", http.StripPrefix("/assets", fileServer))

	r.Route("/resources", func(r chi.Router) {
		r.Route("/js", func(r chi.Router) {
			r.With(validRouteIDs).Get("/embed_script/{guestbookID}/script.js", func(w http.ResponseWriter, r *http.Request) {
				guestbookID := chi.URLParam(r, "guestbookID")
				template, err := textTemplate.ParseFiles("templates/resources/embed_javascript.js")
				if err != nil {
					log.Printf("Error parsing embed script: %v", err)
					http.Error(w, "Embed script unavailable", http.StatusInternalServerError)
					return
				}

				hostUrl := PublicURL()
				if constants.DEBUG_MODE {
					hostUrl = "//" + r.Host
				}

				var guestbook Guestbook
				result := activeGuestbooksQuery(db.WithContext(r.Context())).First(&guestbook, "guestbooks.id = ?", guestbookID)
				if result.Error != nil {
					recordLookupError(w, result.Error, "Guestbook")
					return
				}

				templateData := struct {
					Guestbook  Guestbook
					HostUrl    string
					ConfigJSON string
				}{
					Guestbook: guestbook,
					HostUrl:   hostUrl,
				}
				configJSON, err := json.Marshal(struct {
					CollectEmail           bool   `json:"collectEmail"`
					MaxEmailBytes          int    `json:"maxEmailBytes"`
					ChallengeFailedMessage string `json:"challengeFailedMessage"`
					Question               string `json:"question"`
					Hint                   string `json:"hint"`
					MaxMessageCharacters   int    `json:"maxMessageCharacters"`
					MaxNameCharacters      int    `json:"maxNameCharacters"`
					MaxWebsiteBytes        int    `json:"maxWebsiteBytes"`
				}{guestbook.CollectEmail, maxEmailBytes, guestbook.ChallengeFailedMessage,
					guestbook.ChallengeQuestion, guestbook.ChallengeHint,
					constants.MAX_MESSAGE_LENGTH, maxNameCharacters, maxWebsiteBytes})
				if err != nil {
					http.Error(w, "Error rendering embed configuration", http.StatusInternalServerError)
					return
				}
				templateData.ConfigJSON = string(configJSON)

				w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
				if err := template.Execute(w, templateData); err != nil {
					log.Printf("Render embed script: %v", err)
				}
			})
		})
	})
	r.With(validRouteIDs).Get("/resources/css/guestbook/{guestbookID}.css", GuestbookStyles)

	r.With(validRouteIDs).Get("/api/pow-challenge/{guestbookID}", PowChallengeHandler)

	r.Get("/api/v1/get-guestbook-messages/{guestbookID}", PublicMessagesV1)
	r.Get("/api/v2/get-guestbook-messages/{guestbookID}", PublicMessagesV2)

	return r
}

func Logger(next http.Handler) http.Handler {
	// Define color functions
	gray := color.New(color.FgHiBlack).SprintFunc()
	blue := color.New(color.FgBlue).SprintFunc()
	magenta := color.New(color.FgMagenta).SprintFunc()

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		// Create a response writer wrapper to capture status code
		ww := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}

		// Process the request
		next.ServeHTTP(ww, r)

		// Log the request details (without IP address for GDPR compliance)
		duration := time.Since(start)

		// Determine log level and status color based on status code
		var logLevel string
		var statusStr string
		switch {
		case ww.statusCode >= 500:
			logLevel = "ERROR"
			statusStr = color.New(color.FgRed).Sprintf("%d", ww.statusCode)
		case ww.statusCode >= 400:
			logLevel = "WARN"
			statusStr = color.New(color.FgYellow).Sprintf("%d", ww.statusCode)
		case ww.statusCode >= 300:
			logLevel = "INFO"
			statusStr = color.New(color.FgCyan).Sprintf("%d", ww.statusCode)
		case ww.statusCode >= 200:
			logLevel = "INFO"
			statusStr = color.New(color.FgGreen).Sprintf("%d", ww.statusCode)
		default:
			logLevel = "INFO"
			statusStr = color.New(color.FgWhite).Sprintf("%d", ww.statusCode)
		}

		// Format duration with appropriate color
		var durationStr string
		if duration > 500*time.Millisecond {
			durationStr = color.New(color.FgRed).Sprintf("%v", duration)
		} else if duration > 100*time.Millisecond {
			durationStr = color.New(color.FgYellow).Sprintf("%v", duration)
		} else {
			durationStr = color.New(color.FgGreen).Sprintf("%v", duration)
		}

		// Format response size
		var sizeStr string
		if ww.bytesWritten > 1024*1024 {
			sizeStr = fmt.Sprintf("%.1fMB", float64(ww.bytesWritten)/(1024*1024))
		} else if ww.bytesWritten > 1024 {
			sizeStr = fmt.Sprintf("%.1fKB", float64(ww.bytesWritten)/1024)
		} else {
			sizeStr = fmt.Sprintf("%dB", ww.bytesWritten)
		}

		log.Printf("%s %s %s %s %s %s",
			gray(fmt.Sprintf("[%s]", logLevel)), // [INFO] in gray
			blue(r.Method),                      // GET in blue
			magenta(r.URL.Path),                 // /path in magenta
			statusStr,                           // 200 in appropriate color
			durationStr,                         // 2ms in appropriate color
			gray(fmt.Sprintf("(%s)", sizeStr)),  // (1.2KB) in gray
		)
	})
}

// responseWriter is a wrapper to capture the status code
type responseWriter struct {
	http.ResponseWriter
	statusCode   int
	bytesWritten int
	wroteHeader  bool
}

func (rw *responseWriter) WriteHeader(code int) {
	if rw.wroteHeader {
		return
	}
	rw.statusCode = code
	rw.wroteHeader = true
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *responseWriter) Write(b []byte) (int, error) {
	if !rw.wroteHeader {
		rw.WriteHeader(http.StatusOK)
	}
	n, err := rw.ResponseWriter.Write(b)
	rw.bytesWritten += n
	return n, err
}

// RealIPMiddleware extracts the client's real IP address from the
// X-Forwarded-For header and sets it on the request's RemoteAddr field. Useful
// for when the app is running behind a reverse proxy
func RealIPMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if address := clientAddress(r); address.IsValid() {
			r.RemoteAddr = address.String()
		}
		next.ServeHTTP(w, r)
	})
}
