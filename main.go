package main

import (
	"encoding/json"
	"fmt"
	"guestbook/constants"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	textTemplate "text/template"
	"time"

	"github.com/fatih/color"
	"github.com/go-chi/cors"
	"github.com/spf13/viper"

	"gorm.io/gorm"

	"github.com/go-chi/chi/middleware"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/httprate"

	"gorm.io/driver/sqlite"
)

var db *gorm.DB
var messageCache *MessageCache

func main() {
	viper.SetConfigName("config")
	viper.AddConfigPath(".")
	err := viper.ReadInConfig()
	if err != nil {
		panic(fmt.Errorf("fatal error config file: %w", err))
	}

	initDatabase()
	initCache()

	// Setup a channel to listen for termination signals
	signals := make(chan os.Signal, 1)
	// Notify signals channel on SIGINT and SIGTERM
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)

	r := initRouter()

	const portNum = ":6235"
	go func() {
		log.Printf("Running on http://localhost%s", portNum)
		if err := http.ListenAndServe(portNum, r); err != nil {
			log.Printf("HTTP server stopped: %v", err)
		}
	}()

	// Block until a signal is received
	<-signals
	log.Println("Shutting down gracefully...")

	// Close the database connection
	sqlDB, err := db.DB()
	if err != nil {
		log.Printf("Error on closing database connection: %v", err)
	} else {
		if err := sqlDB.Close(); err != nil {
			log.Printf("Error on closing database connection: %v", err)
		}
	}
}

func initDatabase() {
	var err error
	db, err = gorm.Open(sqlite.Open("file:guestbook.db?cache=shared&mode=rwc&_journal_mode=WAL"), &gorm.Config{})
	if err != nil {
		log.Fatalf("failed to connect database: %v", err)
	}

	// Migrate the schema
	err = db.AutoMigrate(&Guestbook{}, &Message{}, &AdminUser{})
	if err != nil {
		log.Fatalf("failed to migrate database: %v", err)
	}
}

func initCache() {
	var err error
	// Initialize cache with 1000 entries and 10 minute TTL
	messageCache, err = NewMessageCache(1000, 10*time.Minute)
	if err != nil {
		log.Fatalf("failed to initialize cache: %v", err)
	}
	log.Println("Message cache initialized (size: 1000, TTL: 10m)")
}

func initRouter() *chi.Mux {

	r := chi.NewRouter()

	CORSMiddleware := cors.New(cors.Options{
		AllowedOrigins:   []string{"*"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "X-CSRF-Token"},
		ExposedHeaders:   []string{"Link"},
		AllowCredentials: false,
		MaxAge:           300,
	})

	r.Use(CORSMiddleware.Handler)
	r.Use(RealIPMiddleware)
	r.Use(Logger)
	r.Use(httprate.LimitByIP(100, time.Minute)) // general rate limiter for all routes (shared across all routes)
	r.Use(middleware.Recoverer)

	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		renderAdminTemplate(w, r, "landing_page", nil)
	})

	r.Get("/verify-email", VerifyEmailHandler)
	r.Get("/reset-password", ResetPasswordFormHandler)
	r.Post("/reset-password", ResetPasswordHandler)

	r.Get("/terms-and-conditions", func(w http.ResponseWriter, r *http.Request) {
		renderAdminTemplate(w, r, "terms_and_conditions", nil)
	})

	r.Get("/forgot-password", ForgotPasswordHandler)
	r.Post("/forgot-password", ForgotPasswordHandler)

	r.With(AdminAuthMiddleware).Route("/admin", func(r chi.Router) {
		// Basic CSRF guard for state-changing requests: allow only same-origin POSTs.
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodDelete {
					origin := r.Header.Get("Origin")
					referer := r.Header.Get("Referer")
					// In production, PUBLIC_URL should be the absolute origin like https://example.com
					allowed := constants.PUBLIC_URL
					if constants.DEBUG_MODE {
						// Accept current host as origin in debug
						allowed = "//" + r.Host
					}

					// If an Origin is present, require it to contain the allowed host; otherwise, use Referer as a fallback.
					if origin != "" {
						if !strings.Contains(origin, r.Host) && !strings.Contains(origin, strings.TrimPrefix(allowed, "//")) {
							http.Error(w, "CSRF check failed", http.StatusForbidden)
							return
						}
					} else if referer != "" {
						if !strings.Contains(referer, r.Host) && !strings.Contains(referer, strings.TrimPrefix(allowed, "//")) {
							http.Error(w, "CSRF check failed", http.StatusForbidden)
							return
						}
					}
				}
				next.ServeHTTP(w, r)
			})
		})
		r.Get("/", AdminGuestbookList)
		r.Get("/settings", AdminUserSettings)

		r.Post("/settings", AdminUserSettings)
		r.Post("/change-password", AdminChangePassword)

		r.Get("/signin", AdminSignIn)
		r.Post("/signin", AdminSignIn)

		r.Get("/signup", AdminSignUp)
		r.Post("/signup", AdminSignUp)

		r.Post("/logout", AdminLogout)

		r.Get("/guestbook/new", AdminCreateGuestbook)
		r.Post("/guestbook/new", AdminCreateGuestbook)

		r.Route("/guestbook/{guestbookID}", func(r chi.Router) {
			r.Get("/", AdminShowGuestbook)
			r.Get("/embed", AdminEmbedGuestbook)

			r.Get("/edit", AdminEditGuestbook)
			r.Post("/edit", AdminUpdateGuestbook)

			r.Post("/delete", AdminDeleteGuestbook)

			r.Post("/messages/bulk-delete", AdminBulkDeleteMessages)

			r.Route("/message/{messageID}", func(r chi.Router) {
				r.Get("/edit", AdminEditMessage)
				r.Post("/edit", AdminEditMessage)
				r.Post("/delete", AdminDeleteMessage)
				r.Post("/reply", AdminReplyToMessage)
			})
		})
	})

	r.Route("/guestbook", func(r chi.Router) {
		r.Get("/{guestbookID}", GuestbookPage)

		// this means the user has at most N attempts to submit a message to a given guestbook in a minute
		submitRateLimiter := httprate.Limit(
			20,          // requests
			time.Minute, // per duration
			httprate.WithKeyFuncs(httprate.KeyByIP, httprate.KeyByEndpoint),
			httprate.WithLimitHandler(func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, `Rate limited. Please slow down.`, http.StatusTooManyRequests)
			}),
		)

		r.With(submitRateLimiter).
			Post("/{guestbookID}/submit", GuestbookSubmit)
	})

	fileServer := http.FileServer(http.Dir("./assets"))
	r.Handle("/assets/*", http.StripPrefix("/assets", fileServer))

	r.Route("/resources", func(r chi.Router) {
		r.Route("/js", func(r chi.Router) {
			r.Get("/embed_script/{guestbookID}/script.js", func(w http.ResponseWriter, r *http.Request) {
				guestbookID := chi.URLParam(r, "guestbookID")
				template, err := textTemplate.ParseFiles("templates/resources/embed_javascript.js")
				if err != nil {
					log.Fatalf("Error parsing guestbook page template: %v", err)
				}

				hostUrl := constants.PUBLIC_URL
				if constants.DEBUG_MODE {
					hostUrl = "//" + r.Host
				}

				var guestbook Guestbook
				result := db.First(&guestbook, guestbookID)
				if result.Error != nil {
					http.Error(w, "Guestbook not found", http.StatusInternalServerError)
					return
				}

				templateData := struct {
					Guestbook Guestbook
					HostUrl   string
				}{
					Guestbook: guestbook,
					HostUrl:   hostUrl,
				}

				w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
				template.Execute(w, templateData)
			})
		})
	})

	r.Route("/api", func(r chi.Router) {
		r.Route("/v1", func(r chi.Router) {
			r.Get("/get-guestbook-messages/{guestbookID}", func(w http.ResponseWriter, r *http.Request) {
				guestbookID := chi.URLParam(r, "guestbookID")
				guestbookIDUint, err := strconv.ParseUint(guestbookID, 10, 32)
				if err != nil {
					log.Fatal(err)
				}

				// Enforce allowed origins
				var allowedOrigins string
				db.Model(&Guestbook{}).Select("allowed_origins").Where("id = ?", guestbookIDUint).Scan(&allowedOrigins)
				matchedOrigin, originAllowed := checkOriginAllowed(r, allowedOrigins)
				if !originAllowed {
					http.Error(w, "Origin not allowed", http.StatusForbidden)
					return
				}
				setOriginHeaders(w, allowedOrigins, matchedOrigin, false)

				// Try to get from cache first
				if cachedMessages, ok := messageCache.GetMessages(uint(guestbookIDUint)); ok {
					w.Header().Set("Content-Type", "application/json")
					w.Header().Set("X-Cache", "HIT")
					json.NewEncoder(w).Encode(cachedMessages)
					return
				}

				// v1 API - return all messages at top level of response, without pagination (backward compatibility)
				var messages []Message
				result := db.Where(&Message{GuestbookID: uint(guestbookIDUint), Approved: true, ParentMessageID: nil}).
					Order("created_at DESC").
					Preload("Replies", "approved = ?", true).
					Find(&messages)
				if result.Error != nil {
					http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
					return
				}

				// Store in cache
				messageCache.SetMessages(uint(guestbookIDUint), messages)

				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("X-Cache", "MISS")
				json.NewEncoder(w).Encode(messages)
			})
		})

		r.Route("/v2", func(r chi.Router) {
			r.Get("/get-guestbook-messages/{guestbookID}", func(w http.ResponseWriter, r *http.Request) {
				guestbookID := chi.URLParam(r, "guestbookID")
				guestbookIDUint, err := strconv.ParseUint(guestbookID, 10, 32)
				if err != nil {
					log.Fatal(err)
				}

				// Enforce allowed origins
				var allowedOrigins string
				db.Model(&Guestbook{}).Select("allowed_origins").Where("id = ?", guestbookIDUint).Scan(&allowedOrigins)
				matchedOrigin, originAllowed := checkOriginAllowed(r, allowedOrigins)
				if !originAllowed {
					http.Error(w, "Origin not allowed", http.StatusForbidden)
					return
				}
				setOriginHeaders(w, allowedOrigins, matchedOrigin, false)

				pageStr := r.URL.Query().Get("page")
				limitStr := r.URL.Query().Get("limit")

				page := 1
				limit := 20 // Default page size

				if pageStr != "" {
					if p, err := strconv.Atoi(pageStr); err == nil && p > 0 {
						page = p
					}
				}

				if limitStr != "" {
					if l, err := strconv.Atoi(limitStr); err == nil && l > 0 && l <= 100 {
						limit = l
					}
				}

				// Try to get from cache first
				if cachedResponse, ok := messageCache.GetPaginatedResponse(uint(guestbookIDUint), page, limit); ok {
					w.Header().Set("Content-Type", "application/json")
					w.Header().Set("X-Cache", "HIT")
					json.NewEncoder(w).Encode(cachedResponse)
					return
				}

				offset := (page - 1) * limit

				// Try to get count from cache
				var totalCount int64
				var countCached bool
				if totalCount, countCached = messageCache.GetCount(uint(guestbookIDUint)); !countCached {
					countResult := db.Model(&Message{}).Where(&Message{GuestbookID: uint(guestbookIDUint), Approved: true}).Count(&totalCount)
					if countResult.Error != nil {
						http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
						return
					}
					messageCache.SetCount(uint(guestbookIDUint), totalCount)
				}

				var messages []Message
				result := db.Where(&Message{GuestbookID: uint(guestbookIDUint), Approved: true, ParentMessageID: nil}).
					Order("created_at DESC").
					Preload("Replies", "approved = ?", true).
					Limit(limit).
					Offset(offset).
					Find(&messages)
				if result.Error != nil {
					http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
					return
				}

				totalPages := int((totalCount + int64(limit) - 1) / int64(limit))

				response := map[string]any{
					"messages": messages,
					"pagination": map[string]any{
						"page":        page,
						"limit":       limit,
						"total":       totalCount,
						"totalPages":  totalPages,
						"hasNext":     page < totalPages,
						"hasPrevious": page > 1,
					},
				}

				// Store in cache
				messageCache.SetPaginatedResponse(uint(guestbookIDUint), page, limit, response)

				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("X-Cache", "MISS")

				json.NewEncoder(w).Encode(response)
			})
		})
	})

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
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			// This assumes the first IP in the X-Forwarded-For list is the client's real IP
			// This may need to be adjusted depending on your reverse proxy setup
			i := strings.Index(xff, ", ")
			if i == -1 {
				i = len(xff)
			}
			r.RemoteAddr = xff[:i]
		}
		next.ServeHTTP(w, r)
	})
}
