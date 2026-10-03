package main

import (
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/spf13/viper"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

var (
	testServer        *httptest.Server
	testBaseURL       string
	testDBDir         string
	testRouter        atomic.Pointer[chi.Mux]
	startBrowserTests func()
	stopBrowserTests  func()
)

func TestMain(m *testing.M) {
	if err := setupTestEnvironment(); err != nil {
		teardownTestEnvironment()
		log.Printf("Failed to set up tests: %v", err)
		os.Exit(1)
	}
	if startBrowserTests != nil {
		startBrowserTests()
	}
	code := m.Run()
	if stopBrowserTests != nil {
		stopBrowserTests()
	}
	teardownTestEnvironment()
	os.Exit(code)
}

func setupTestEnvironment() error {
	var err error
	testDBDir, err = os.MkdirTemp("", "guestbooks-tests-")
	if err != nil {
		return err
	}
	db, err = gorm.Open(sqlite.Open("file:"+filepath.Join(testDBDir, "guestbook.db")+"?mode=rwc&_journal_mode=WAL&_busy_timeout=5000"), &gorm.Config{Logger: databaseLogger})
	if err != nil {
		return fmt.Errorf("test database: %w", err)
	}
	if err := db.AutoMigrate(&Guestbook{}, &Message{}, &AdminUser{}); err != nil {
		return err
	}
	messageCache, err = NewMessageCache(1000, 5*time.Minute)
	if err != nil {
		return err
	}
	viper.Set("mailer.mailer_name", "none")
	if err := initRuntimeConfig(); err != nil {
		return err
	}
	powChallengeStore = NewChallengeStore()
	resetTestRouter()
	testServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		testRouter.Load().ServeHTTP(w, r)
	}))
	testBaseURL = testServer.URL
	appConfig.PublicURL = testBaseURL
	return nil
}

func resetTestRouter() {
	testRouter.Store(initRouter())
}

func teardownTestEnvironment() {
	if testServer != nil {
		testServer.Close()
	}
	if db != nil {
		if connection, err := db.DB(); err == nil {
			if err := connection.Close(); err != nil {
				log.Printf("Close test database: %v", err)
			}
		}
	}
	if testDBDir != "" {
		for _, name := range []string{"guestbook.db", "guestbook.db-wal", "guestbook.db-shm"} {
			if err := os.Remove(filepath.Join(testDBDir, name)); err != nil && !os.IsNotExist(err) {
				log.Printf("Remove test database file: %v", err)
			}
		}
		if err := os.Remove(testDBDir); err != nil {
			log.Printf("Remove test directory: %v", err)
		}
	}
}
