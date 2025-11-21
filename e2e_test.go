package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/spf13/viper"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

const (
	testPort    = ":16235"
	testBaseURL = "http://localhost:16235"
	testDBFile  = "test_guestbook.db"
)

var (
	testServer *http.Server
	browser    *rod.Browser
)

// TestMain sets up and tears down the test environment
func TestMain(m *testing.M) {
	// Setup
	if err := setupTestEnvironment(); err != nil {
		log.Fatalf("Failed to setup test environment: %v", err)
	}

	// Run tests
	code := m.Run()

	// Teardown
	teardownTestEnvironment()

	os.Exit(code)
}

func setupTestEnvironment() error {
	// Clean up any existing test database
	os.Remove(testDBFile)

	// Initialize test database
	var err error
	db, err = gorm.Open(sqlite.Open("file:"+testDBFile+"?cache=shared&mode=rwc&_journal_mode=WAL"), &gorm.Config{})
	if err != nil {
		return fmt.Errorf("failed to connect to test database: %w", err)
	}

	// Migrate the schema
	err = db.AutoMigrate(&Guestbook{}, &Message{}, &AdminUser{})
	if err != nil {
		return fmt.Errorf("failed to migrate test database: %w", err)
	}

	// Initialize cache
	messageCache, err = NewMessageCache(1000, 5*time.Minute)
	if err != nil {
		return fmt.Errorf("failed to initialize cache: %w", err)
	}

	// Load config (or use defaults)
	viper.SetDefault("mail.smtp_host", "localhost")
	viper.SetDefault("mail.smtp_port", 587)

	// Start test server
	r := initRouter()
	testServer = &http.Server{
		Addr:    testPort,
		Handler: r,
	}

	go func() {
		if err := testServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("Test server error: %v", err)
		}
	}()

	// Wait for server to be ready
	time.Sleep(500 * time.Millisecond)

	// Launch browser
	l := launcher.New().Headless(true).MustLaunch()
	browser = rod.New().ControlURL(l).MustConnect()

	log.Println("Test environment setup complete")
	return nil
}

func teardownTestEnvironment() {
	// Close browser
	if browser != nil {
		browser.MustClose()
	}

	// Shutdown server
	if testServer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		testServer.Shutdown(ctx)
	}

	// Close database
	if db != nil {
		sqlDB, _ := db.DB()
		if sqlDB != nil {
			sqlDB.Close()
		}
	}

	// Remove test database
	os.Remove(testDBFile)

	log.Println("Test environment teardown complete")
}

// TestGuestbookBasicFlow tests the complete user journey
func TestGuestbookBasicFlow(t *testing.T) {
	page := browser.MustPage(testBaseURL)
	defer page.MustClose()

	username := fmt.Sprintf("testuser_%d", time.Now().Unix())
	password := "testpassword123"
	websiteURL := "https://example.com"

	// Step 1: Sign up
	t.Log("Step 1: Creating admin account")
	page.MustNavigate(testBaseURL + "/admin/signup")
	page.MustElement("input[name='username']").MustInput(username)
	page.MustElement("input[name='password']").MustInput(password)
	page.MustElement("input[type='checkbox']").MustClick() // Accept terms and conditions
	page.MustElement("form button[type='submit']").MustClick()

	// Wait for redirect to admin panel
	page.MustWaitLoad()

	// Wait for the URL to actually be /admin (not /admin/signup or /admin/signin)
	// This ensures the signup succeeded and we're authenticated
	page.MustWaitStable()
	currentURL := page.MustInfo().URL
	t.Logf("After signup, current URL: %s", currentURL)

	if !strings.Contains(currentURL, "/admin") || strings.Contains(currentURL, "/signin") {
		t.Fatalf("Failed to authenticate after signup, current URL: %s", currentURL)
	}

	if !page.MustHas("h1") {
		t.Fatal("Failed to navigate to admin panel after signup")
	}

	// Step 2: Create a guestbook
	t.Log("Step 2: Creating guestbook")
	page.MustNavigate(testBaseURL + "/admin/guestbook/new")
	page.MustWaitLoad()

	page.MustElement("input[name='websiteURL']").MustInput(websiteURL)
	page.MustElement("#guestbook-edit-form button[type='submit']").MustClick() // Target form by ID

	// Wait for redirect back to guestbook list
	page.MustWaitLoad()
	time.Sleep(500 * time.Millisecond) // Give it a moment to fully load

	// Get the guestbook ID from the page - find the first link that's NOT the "new" link
	guestbookLink := page.MustElement("a[href*='/admin/guestbook/']:not([href*='/new'])").MustProperty("href").String()
	t.Logf("Created guestbook link: %s", guestbookLink)

	// Extract guestbook ID from link (format: /admin/guestbook/1)
	var guestbookID string
	fmt.Sscanf(guestbookLink, testBaseURL+"/admin/guestbook/%s", &guestbookID)
	t.Logf("Guestbook ID: %s", guestbookID)

	// Step 3: Submit a message to the guestbook (first message)
	t.Log("Step 3: Submitting first message")
	publicURL := testBaseURL + "/guestbook/" + guestbookID
	page.MustNavigate(publicURL)
	page.MustWaitLoad()

	// Wait for the form to be visible (the page includes an async script that might modify the form)
	page.MustWaitStable()
	time.Sleep(300 * time.Millisecond) // Give JavaScript time to initialize

	page.MustElement("#guestbooks___guestbook-form input[name='name']").MustInput("Test User 1")
	page.MustElement("#guestbooks___guestbook-form textarea[name='text']").MustInput("This is my first test message!")
	page.MustElement("#guestbooks___guestbook-form input[type='submit']").MustClick()
	page.MustWaitLoad()

	// Verify message appears on the page (messages are loaded via JavaScript)
	time.Sleep(500 * time.Millisecond) // Wait for JS to render messages
	pageText := page.MustElement("body").MustText()
	if !strings.Contains(pageText, "Test User 1") {
		t.Error("First message name not found on page")
	}
	if !strings.Contains(pageText, "This is my first test message!") {
		t.Error("First message text not found on page")
	}

	// Step 4: Submit a second message to test cache invalidation
	t.Log("Step 4: Submitting second message to test cache invalidation")
	page.MustElement("#guestbooks___guestbook-form input[name='name']").MustInput("Test User 2")
	page.MustElement("#guestbooks___guestbook-form textarea[name='text']").MustInput("This is my second test message!")
	page.MustElement("#guestbooks___guestbook-form input[type='submit']").MustClick()
	page.MustWaitLoad()

	// Verify both messages appear (cache should have been invalidated)
	time.Sleep(500 * time.Millisecond) // Wait for JS to render messages
	pageText = page.MustElement("body").MustText()
	if !strings.Contains(pageText, "Test User 1") {
		t.Error("First message not found after second submission")
	}
	if !strings.Contains(pageText, "Test User 2") {
		t.Error("Second message name not found on page")
	}
	if !strings.Contains(pageText, "This is my second test message!") {
		t.Error("Second message text not found on page")
	}

	// Step 5: Reload page multiple times to test caching
	t.Log("Step 5: Testing cache hits with multiple page loads")
	startTime := time.Now()
	for i := 0; i < 5; i++ {
		page.MustNavigate(publicURL)
		page.MustWaitLoad()
		time.Sleep(200 * time.Millisecond) // Wait for JS
		if !strings.Contains(page.MustElement("body").MustText(), "Test User 2") {
			t.Errorf("Message not found on reload %d", i+1)
		}
	}
	duration := time.Since(startTime)
	t.Logf("5 page loads completed in %v (should be fast due to caching)", duration)

	// Step 6: Test admin edit message functionality
	t.Log("Step 6: Testing admin message edit")
	page.MustNavigate(testBaseURL + "/admin/guestbook/" + guestbookID)
	page.MustWaitLoad()

	// Find and click the first "Edit" link
	editLink := page.MustElement("a[href*='/message/'][href*='/edit']")
	editLink.MustClick()
	page.MustWaitLoad()

	// Edit the message
	textArea := page.MustElement("textarea[name='text']")
	textArea.MustSelectAllText()
	textArea.MustInput("This message has been edited!")
	page.MustElement("form input[type='submit']").MustClick()
	page.MustWaitLoad()

	// Go back to public page and verify the edit
	page.MustNavigate(publicURL)
	page.MustWaitLoad()
	time.Sleep(500 * time.Millisecond) // Wait for JS to render
	if !strings.Contains(page.MustElement("body").MustText(), "This message has been edited!") {
		t.Error("Edited message not found on public page (cache may not have been invalidated)")
	}

	t.Log("All tests passed!")
}

// TestAPIEndpointsCaching tests the API endpoints directly
func TestAPIEndpointsCaching(t *testing.T) {
	// Create a test guestbook directly in the database
	adminUser := AdminUser{
		Username:     fmt.Sprintf("apitest_%d", time.Now().Unix()),
		PasswordHash: []byte("test"),
		SessionToken: "test_token",
	}
	db.Create(&adminUser)

	guestbook := Guestbook{
		WebsiteURL:  "https://apitest.com",
		AdminUserID: adminUser.ID,
	}
	db.Create(&guestbook)

	// Create some test messages
	for i := 1; i <= 3; i++ {
		message := Message{
			Name:        fmt.Sprintf("API User %d", i),
			Text:        fmt.Sprintf("API test message %d", i),
			GuestbookID: guestbook.ID,
			Approved:    true,
		}
		db.Create(&message)
	}

	// Test v1 API endpoint
	t.Log("Testing v1 API endpoint")
	page := browser.MustPage(testBaseURL + fmt.Sprintf("/api/v1/get-guestbook-messages/%d", guestbook.ID))
	defer page.MustClose()

	// First request should be a cache miss
	body := page.MustElement("body").MustText()
	if body == "" {
		t.Error("v1 API returned empty response")
	}
	t.Log("v1 API first request successful")

	// Second request should be a cache hit
	page.MustNavigate(testBaseURL + fmt.Sprintf("/api/v1/get-guestbook-messages/%d", guestbook.ID))
	body2 := page.MustElement("body").MustText()
	if body2 == "" {
		t.Error("v1 API second request returned empty response")
	}
	t.Log("v1 API second request successful (should be cached)")

	// Test v2 API endpoint
	t.Log("Testing v2 API endpoint")
	page.MustNavigate(testBaseURL + fmt.Sprintf("/api/v2/get-guestbook-messages/%d", guestbook.ID))
	body3 := page.MustElement("body").MustText()
	if body3 == "" {
		t.Error("v2 API returned empty response")
	}
	if body3 == body {
		t.Error("v2 API response format should differ from v1 (includes pagination)")
	}
	t.Log("v2 API request successful")

	t.Log("API endpoint tests passed!")
}
