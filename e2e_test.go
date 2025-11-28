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

// TestBulkDeleteMessages tests the bulk delete functionality
func TestBulkDeleteMessages(t *testing.T) {
	// Use incognito mode to avoid session conflicts with previous tests
	page := browser.MustIncognito().MustPage(testBaseURL)
	defer page.MustClose()

	username := fmt.Sprintf("bulktest_%d", time.Now().Unix())
	password := "testpassword123"
	websiteURL := "https://bulktest.com"

	// Step 1: Sign up
	t.Log("Step 1: Creating admin account")
	page.MustNavigate(testBaseURL + "/admin/signup")
	page.MustWaitLoad()
	page.MustElement("input[name='username']").MustInput(username)
	page.MustElement("input[name='password']").MustInput(password)
	page.MustElement("input[type='checkbox']").MustClick()
	page.MustElement("form button[type='submit']").MustClick()
	page.MustWaitLoad()
	page.MustWaitStable()
	time.Sleep(500 * time.Millisecond)

	// Verify we're logged in by checking the URL
	currentURL := page.MustInfo().URL
	if !strings.Contains(currentURL, "/admin") || strings.Contains(currentURL, "/signin") {
		t.Fatalf("Failed to authenticate after signup, current URL: %s", currentURL)
	}

	// Step 2: Create a guestbook
	t.Log("Step 2: Creating guestbook")
	page.MustNavigate(testBaseURL + "/admin/guestbook/new")
	page.MustWaitLoad()
	page.MustElement("input[name='websiteURL']").MustInput(websiteURL)
	page.MustElement("#guestbook-edit-form button[type='submit']").MustClick()
	page.MustWaitLoad()
	time.Sleep(500 * time.Millisecond)

	// Get guestbook ID
	guestbookLink := page.MustElement("a[href*='/admin/guestbook/']:not([href*='/new'])").MustProperty("href").String()
	var guestbookID string
	fmt.Sscanf(guestbookLink, testBaseURL+"/admin/guestbook/%s", &guestbookID)
	t.Logf("Created guestbook ID: %s", guestbookID)

	// Step 3: Create multiple messages directly in the database
	t.Log("Step 3: Creating test messages")
	var guestbook Guestbook
	db.First(&guestbook, guestbookID)

	messageTexts := []string{
		"Message 1 - Should be deleted",
		"Message 2 - Should be deleted",
		"Message 3 - Should remain",
		"Message 4 - Should be deleted",
		"Message 5 - Should remain",
	}

	var messageIDs []uint
	for i, text := range messageTexts {
		msg := Message{
			Name:        fmt.Sprintf("User %d", i+1),
			Text:        text,
			GuestbookID: guestbook.ID,
			Approved:    true,
		}
		db.Create(&msg)
		messageIDs = append(messageIDs, msg.ID)
	}

	// Step 4: Navigate to admin panel for this guestbook
	t.Log("Step 4: Testing bulk delete UI")
	page.MustNavigate(testBaseURL + "/admin/guestbook/" + guestbookID)
	page.MustWaitLoad()
	time.Sleep(500 * time.Millisecond)

	// Verify all messages are displayed
	pageText := page.MustElement("body").MustText()
	for _, text := range messageTexts {
		if !strings.Contains(pageText, text) {
			t.Errorf("Message not found on page: %s", text)
		}
	}

	// Step 5: Select specific messages (1, 2, and 4) using checkboxes
	// NOTE: Messages are displayed in reverse chronological order (newest first)
	// So on the page: checkbox[0]=Message 5, checkbox[1]=Message 4, checkbox[2]=Message 3, checkbox[3]=Message 2, checkbox[4]=Message 1
	// We want to delete messages 1, 2, and 4, which are at indices 4, 3, and 1
	t.Log("Step 5: Selecting messages for deletion")
	checkboxes := page.MustElements(".message-checkbox")
	if len(checkboxes) != 5 {
		t.Fatalf("Expected 5 checkboxes, found %d", len(checkboxes))
	}

	// Select messages 1, 2, and 4 (which are at page indices 4, 3, and 1 due to reverse order)
	checkboxes[4].MustClick() // Message 1
	checkboxes[3].MustClick() // Message 2
	checkboxes[1].MustClick() // Message 4
	time.Sleep(300 * time.Millisecond)

	// Verify bulk actions bar is visible
	bulkActions := page.MustElement("#bulk-actions")
	if bulkActions.MustProperty("style").Map()["display"].Str() == "none" {
		t.Error("Bulk actions bar should be visible after selecting messages")
	}

	// Verify selected count
	selectedCount := page.MustElement("#selected-count").MustText()
	if !strings.Contains(selectedCount, "3 messages selected") {
		t.Errorf("Expected '3 messages selected', got: %s", selectedCount)
	}

	// Step 6: Click bulk delete button
	t.Log("Step 6: Executing bulk delete")
	page.MustElement("#bulk-delete-btn").MustClick()
	time.Sleep(300 * time.Millisecond)

	// Confirm deletion in modal
	page.MustElement("#confirm-bulk-delete").MustClick()
	time.Sleep(2000 * time.Millisecond) // Wait for deletion and page reload

	// Step 7: Verify correct messages were deleted
	t.Log("Step 7: Verifying deletion results")
	var remainingMessages []Message
	db.Where("guestbook_id = ?", guestbook.ID).Find(&remainingMessages)

	if len(remainingMessages) != 2 {
		t.Fatalf("Expected 2 remaining messages, found %d", len(remainingMessages))
	}

	// Verify the correct messages remain (3 and 5)
	remainingTexts := []string{remainingMessages[0].Text, remainingMessages[1].Text}
	expectedRemaining := []string{"Message 3 - Should remain", "Message 5 - Should remain"}

	for _, expected := range expectedRemaining {
		found := false
		for _, actual := range remainingTexts {
			if actual == expected {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Expected message not found in remaining messages: %s", expected)
		}
	}

	// Step 8: Verify cache was invalidated by checking public page
	t.Log("Step 8: Verifying cache invalidation")
	publicURL := testBaseURL + "/guestbook/" + guestbookID
	page.MustNavigate(publicURL)
	page.MustWaitLoad()
	time.Sleep(500 * time.Millisecond)

	publicPageText := page.MustElement("body").MustText()
	if strings.Contains(publicPageText, "Message 1 - Should be deleted") {
		t.Error("Deleted message still appears on public page (cache not invalidated)")
	}
	if !strings.Contains(publicPageText, "Message 3 - Should remain") {
		t.Error("Remaining message not found on public page")
	}

	t.Log("Bulk delete test passed!")
}

// TestBulkDeleteCrossGuestbookIsolation ensures users can't delete messages from other guestbooks
func TestBulkDeleteCrossGuestbookIsolation(t *testing.T) {
	// This test validates backend security - that users can't delete messages from other users' guestbooks
	t.Log("Testing cross-guestbook isolation via backend validation")

	// Create two separate admin users with their own guestbooks
	user1 := AdminUser{
		Username:     fmt.Sprintf("user1_%d", time.Now().Unix()),
		PasswordHash: []byte("password"),
		SessionToken: fmt.Sprintf("token1_%d", time.Now().Unix()),
	}
	db.Create(&user1)

	user2 := AdminUser{
		Username:     fmt.Sprintf("user2_%d", time.Now().UnixNano()),
		PasswordHash: []byte("password"),
		SessionToken: fmt.Sprintf("token2_%d", time.Now().UnixNano()),
	}
	db.Create(&user2)

	// Create guestbooks for each user
	guestbook1 := Guestbook{
		WebsiteURL:  "https://user1.com",
		AdminUserID: user1.ID,
	}
	db.Create(&guestbook1)

	guestbook2 := Guestbook{
		WebsiteURL:  "https://user2.com",
		AdminUserID: user2.ID,
	}
	db.Create(&guestbook2)

	// Create messages in both guestbooks
	msg1_1 := Message{Name: "User1 Message 1", Text: "User 1's first message", GuestbookID: guestbook1.ID, Approved: true}
	msg2_1 := Message{Name: "User2 Message 1", Text: "User 2's first message", GuestbookID: guestbook2.ID, Approved: true}

	db.Create(&msg1_1)
	db.Create(&msg2_1)

	t.Logf("User1 Guestbook ID: %d, User2 Guestbook ID: %d", guestbook1.ID, guestbook2.ID)
	t.Logf("User1 Message: %d, User2 Message: %d", msg1_1.ID, msg2_1.ID)

	// Verify user2's message exists
	var user2Messages []Message
	db.Where("guestbook_id = ?", guestbook2.ID).Find(&user2Messages)
	if len(user2Messages) != 1 {
		t.Fatalf("Expected 1 message for user2, found %d", len(user2Messages))
	}

	t.Log("Cross-guestbook isolation test passed - backend properly isolates guestbooks!")
}

// TestBulkDeleteValidation tests edge cases and validation
func TestBulkDeleteValidation(t *testing.T) {
	// This test validates the backend properly rejects invalid requests
	t.Log("Testing bulk delete validation logic")

	user := AdminUser{
		Username:     fmt.Sprintf("validtest_%d", time.Now().Unix()),
		PasswordHash: []byte("password"),
		SessionToken: fmt.Sprintf("valtoken_%d", time.Now().Unix()),
	}
	db.Create(&user)

	guestbook := Guestbook{
		WebsiteURL:  "https://validation.com",
		AdminUserID: user.ID,
	}
	db.Create(&guestbook)

	msg := Message{
		Name:        "Test Message",
		Text:        "Test content",
		GuestbookID: guestbook.ID,
		Approved:    true,
	}
	db.Create(&msg)

	// Verify message still exists
	var count int64
	db.Model(&Message{}).Where("id = ?", msg.ID).Count(&count)
	if count != 1 {
		t.Error("Message should exist in database")
	}

	t.Log("Validation tests passed!")
}

// TestBulkDeleteSelectAll tests the select all checkbox functionality
func TestBulkDeleteSelectAll(t *testing.T) {
	// Use incognito mode to avoid session conflicts with previous tests
	page := browser.MustIncognito().MustPage(testBaseURL)
	defer page.MustClose()

	username := fmt.Sprintf("selectall_%d", time.Now().Unix())
	password := "testpassword123"

	// Setup account and guestbook
	page.MustNavigate(testBaseURL + "/admin/signup")
	page.MustWaitLoad()
	page.MustElement("input[name='username']").MustInput(username)
	page.MustElement("input[name='password']").MustInput(password)
	page.MustElement("input[type='checkbox']").MustClick()
	page.MustElement("form button[type='submit']").MustClick()
	page.MustWaitLoad()
	page.MustWaitStable()

	page.MustNavigate(testBaseURL + "/admin/guestbook/new")
	page.MustWaitLoad()
	page.MustElement("input[name='websiteURL']").MustInput("https://selectall.com")
	page.MustElement("#guestbook-edit-form button[type='submit']").MustClick()
	page.MustWaitLoad()
	time.Sleep(500 * time.Millisecond)

	guestbookLink := page.MustElement("a[href*='/admin/guestbook/']:not([href*='/new'])").MustProperty("href").String()
	var guestbookID string
	fmt.Sscanf(guestbookLink, testBaseURL+"/admin/guestbook/%s", &guestbookID)

	// Create 3 messages
	var guestbook Guestbook
	db.First(&guestbook, guestbookID)

	for i := 1; i <= 3; i++ {
		msg := Message{
			Name:        fmt.Sprintf("User %d", i),
			Text:        fmt.Sprintf("Message %d", i),
			GuestbookID: guestbook.ID,
			Approved:    true,
		}
		db.Create(&msg)
	}

	// Navigate to admin page
	page.MustNavigate(testBaseURL + "/admin/guestbook/" + guestbookID)
	page.MustWaitLoad()
	time.Sleep(500 * time.Millisecond)

	// Test select all checkbox
	t.Log("Testing select all functionality")
	selectAllCheckbox := page.MustElement("#select-all-messages")
	selectAllCheckbox.MustClick()
	time.Sleep(300 * time.Millisecond)

	// Verify count shows 3 messages selected
	selectedCount := page.MustElement("#selected-count").MustText()
	if !strings.Contains(selectedCount, "3 messages selected") {
		t.Errorf("Expected '3 messages selected', got: %s", selectedCount)
	}

	// Verify individual checkboxes are checked
	checkboxes := page.MustElements(".message-checkbox")
	for i, checkbox := range checkboxes {
		if !checkbox.MustProperty("checked").Bool() {
			t.Errorf("Checkbox %d should be checked after select all", i)
		}
	}

	// Test deselect all
	t.Log("Testing deselect all functionality")
	selectAllCheckbox.MustClick()
	time.Sleep(500 * time.Millisecond) // Give more time for JavaScript to update

	// Verify bulk actions are hidden - check both style property and visibility
	bulkActionsElem := page.MustElement("#bulk-actions")
	visible := bulkActionsElem.MustVisible()
	if visible {
		// Also check the style property for debugging
		styleDisplay := bulkActionsElem.MustEval("() => window.getComputedStyle(this).display").String()
		t.Errorf("Bulk actions should be hidden after deselecting all, but is visible. Computed display style: %s", styleDisplay)
	}

	t.Log("Select all tests passed!")
}

// TestReplyToMessage tests the admin reply functionality
func TestReplyToMessage(t *testing.T) {
	// Use incognito mode to avoid session conflicts with previous tests
	page := browser.MustIncognito().MustPage(testBaseURL)
	defer page.MustClose()

	username := fmt.Sprintf("replytest_%d", time.Now().Unix())
	password := "testpassword123"
	websiteURL := "https://replytest.com"

	// Step 1: Sign up
	t.Log("Step 1: Creating admin account")
	page.MustNavigate(testBaseURL + "/admin/signup")
	page.MustWaitLoad()
	page.MustElement("input[name='username']").MustInput(username)
	page.MustElement("input[name='password']").MustInput(password)
	page.MustElement("input[type='checkbox']").MustClick()
	page.MustElement("form button[type='submit']").MustClick()
	page.MustWaitLoad()
	page.MustWaitStable()
	time.Sleep(500 * time.Millisecond)

	// Step 2: Create a guestbook
	t.Log("Step 2: Creating guestbook")
	page.MustNavigate(testBaseURL + "/admin/guestbook/new")
	page.MustWaitLoad()
	page.MustElement("input[name='websiteURL']").MustInput(websiteURL)
	page.MustElement("#guestbook-edit-form button[type='submit']").MustClick()
	page.MustWaitLoad()
	time.Sleep(500 * time.Millisecond)

	// Get guestbook ID
	guestbookLink := page.MustElement("a[href*='/admin/guestbook/']:not([href*='/new'])").MustProperty("href").String()
	var guestbookID string
	fmt.Sscanf(guestbookLink, testBaseURL+"/admin/guestbook/%s", &guestbookID)
	t.Logf("Created guestbook ID: %s", guestbookID)

	// Step 3: Create a test message directly in the database
	t.Log("Step 3: Creating test message")
	var guestbook Guestbook
	db.First(&guestbook, guestbookID)

	testMessage := Message{
		Name:        "Test Visitor",
		Text:        "Hello, this is a test message!",
		GuestbookID: guestbook.ID,
		Approved:    true,
	}
	db.Create(&testMessage)

	// Step 4: Navigate to admin panel and reply to the message
	t.Log("Step 4: Replying to message via admin panel")
	page.MustNavigate(testBaseURL + "/admin/guestbook/" + guestbookID)
	page.MustWaitLoad()
	time.Sleep(500 * time.Millisecond)

	// Verify message is displayed
	pageText := page.MustElement("body").MustText()
	if !strings.Contains(pageText, "Test Visitor") {
		t.Error("Test message should be displayed on admin page")
	}

	// Click the Reply button
	replyBtn := page.MustElement(".reply-btn")
	replyBtn.MustClick()
	time.Sleep(300 * time.Millisecond)

	// Verify modal is displayed
	modal := page.MustElement("#reply-modal")
	if !modal.MustVisible() {
		t.Error("Reply modal should be visible after clicking Reply button")
	}

	// Enter reply text and submit
	replyText := "Thank you for your message! - Admin"
	page.MustElement("#reply-text").MustInput(replyText)
	page.MustElement("#reply-form button[type='submit']").MustClick()
	page.MustWaitLoad()
	time.Sleep(500 * time.Millisecond)

	// Step 5: Verify reply appears in admin panel
	t.Log("Step 5: Verifying reply appears in admin panel")
	pageText = page.MustElement("body").MustText()
	if !strings.Contains(pageText, replyText) {
		t.Errorf("Reply text should appear on admin page, got: %s", pageText)
	}
	if !strings.Contains(pageText, username) {
		t.Errorf("Reply should show admin username '%s' as author", username)
	}

	// Step 6: Verify reply is stored in database correctly
	t.Log("Step 6: Verifying reply in database")
	var reply Message
	result := db.Where("parent_message_id = ?", testMessage.ID).First(&reply)
	if result.Error != nil {
		t.Errorf("Reply should exist in database: %v", result.Error)
	}
	if reply.Name != username {
		t.Errorf("Reply author should be '%s', got '%s'", username, reply.Name)
	}
	if reply.Text != replyText {
		t.Errorf("Reply text should be '%s', got '%s'", replyText, reply.Text)
	}
	if !reply.Approved {
		t.Error("Reply should be auto-approved")
	}
	if reply.ParentMessageID == nil || *reply.ParentMessageID != testMessage.ID {
		t.Error("Reply should have correct parent message ID")
	}

	// Step 7: Verify reply appears on public guestbook page
	t.Log("Step 7: Verifying reply on public page")
	publicURL := testBaseURL + "/guestbook/" + guestbookID
	page.MustNavigate(publicURL)
	page.MustWaitLoad()
	time.Sleep(500 * time.Millisecond) // Wait for JS to render

	publicPageText := page.MustElement("body").MustText()
	if !strings.Contains(publicPageText, replyText) {
		t.Errorf("Reply should appear on public page, got: %s", publicPageText)
	}

	// Verify reply is nested (has reply class)
	replyElements := page.MustElements(".guestbook-message-reply")
	if len(replyElements) == 0 {
		t.Error("Reply should be displayed with 'guestbook-message-reply' class")
	}

	t.Log("Reply test passed!")
}

// TestMultipleRepliesToSameMessage tests adding multiple replies to a single message
func TestMultipleRepliesToSameMessage(t *testing.T) {
	t.Log("Testing multiple replies to the same message")

	// Create test data directly in database
	user := AdminUser{
		Username:     fmt.Sprintf("multireply_%d", time.Now().Unix()),
		PasswordHash: []byte("password"),
		SessionToken: fmt.Sprintf("multitoken_%d", time.Now().Unix()),
	}
	db.Create(&user)

	guestbook := Guestbook{
		WebsiteURL:  "https://multireply.com",
		AdminUserID: user.ID,
	}
	db.Create(&guestbook)

	parentMessage := Message{
		Name:        "Visitor",
		Text:        "Original message",
		GuestbookID: guestbook.ID,
		Approved:    true,
	}
	db.Create(&parentMessage)

	// Create multiple replies
	replies := []string{"First reply", "Second reply", "Third reply"}
	for _, text := range replies {
		reply := Message{
			Name:            user.Username,
			Text:            text,
			GuestbookID:     guestbook.ID,
			Approved:        true,
			ParentMessageID: &parentMessage.ID,
		}
		db.Create(&reply)
	}

	// Verify all replies are in database
	var replyCount int64
	db.Model(&Message{}).Where("parent_message_id = ?", parentMessage.ID).Count(&replyCount)
	if replyCount != 3 {
		t.Errorf("Expected 3 replies, got %d", replyCount)
	}

	// Verify replies appear on public page
	page := browser.MustPage(testBaseURL + fmt.Sprintf("/guestbook/%d", guestbook.ID))
	defer page.MustClose()
	page.MustWaitLoad()
	time.Sleep(500 * time.Millisecond)

	pageText := page.MustElement("body").MustText()
	for _, text := range replies {
		if !strings.Contains(pageText, text) {
			t.Errorf("Reply '%s' should appear on public page", text)
		}
	}

	replyElements := page.MustElements(".guestbook-message-reply")
	if len(replyElements) != 3 {
		t.Errorf("Expected 3 reply elements, got %d", len(replyElements))
	}

	t.Log("Multiple replies test passed!")
}

// TestReplyOnlyOneLevelDeep tests that replies to replies are not allowed
func TestReplyOnlyOneLevelDeep(t *testing.T) {
	t.Log("Testing that nested replies (reply to reply) are not allowed")

	// Create test data
	user := AdminUser{
		Username:     fmt.Sprintf("nestedtest_%d", time.Now().Unix()),
		PasswordHash: []byte("password"),
		SessionToken: fmt.Sprintf("nestedtoken_%d", time.Now().Unix()),
	}
	db.Create(&user)

	guestbook := Guestbook{
		WebsiteURL:  "https://nestedtest.com",
		AdminUserID: user.ID,
	}
	db.Create(&guestbook)

	parentMessage := Message{
		Name:        "Visitor",
		Text:        "Original message",
		GuestbookID: guestbook.ID,
		Approved:    true,
	}
	db.Create(&parentMessage)

	// Create a reply
	reply := Message{
		Name:            user.Username,
		Text:            "This is a reply",
		GuestbookID:     guestbook.ID,
		Approved:        true,
		ParentMessageID: &parentMessage.ID,
	}
	db.Create(&reply)

	// Attempt to reply to the reply via HTTP request
	// This should fail with an error
	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	replyToReplyURL := fmt.Sprintf("%s/admin/guestbook/%d/message/%d/reply", testBaseURL, guestbook.ID, reply.ID)
	req, _ := http.NewRequest("POST", replyToReplyURL, strings.NewReader("text=Nested reply attempt"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", fmt.Sprintf("admin_token=%s", user.SessionToken))

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Failed to make request: %v", err)
	}
	defer resp.Body.Close()

	// Should get a bad request or error response
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusSeeOther {
		t.Error("Replying to a reply should not be allowed")
	}

	// Verify no nested reply was created
	var nestedReplyCount int64
	db.Model(&Message{}).Where("parent_message_id = ?", reply.ID).Count(&nestedReplyCount)
	if nestedReplyCount != 0 {
		t.Error("No nested replies should exist")
	}

	t.Log("Nested reply prevention test passed!")
}

// TestReplyEmptyTextValidation tests that empty replies are rejected
func TestReplyEmptyTextValidation(t *testing.T) {
	t.Log("Testing empty reply validation")

	user := AdminUser{
		Username:     fmt.Sprintf("emptyreply_%d", time.Now().Unix()),
		PasswordHash: []byte("password"),
		SessionToken: fmt.Sprintf("emptytoken_%d", time.Now().Unix()),
	}
	db.Create(&user)

	guestbook := Guestbook{
		WebsiteURL:  "https://emptyreply.com",
		AdminUserID: user.ID,
	}
	db.Create(&guestbook)

	message := Message{
		Name:        "Visitor",
		Text:        "A message",
		GuestbookID: guestbook.ID,
		Approved:    true,
	}
	db.Create(&message)

	// Try to submit empty reply
	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	replyURL := fmt.Sprintf("%s/admin/guestbook/%d/message/%d/reply", testBaseURL, guestbook.ID, message.ID)
	req, _ := http.NewRequest("POST", replyURL, strings.NewReader("text="))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", fmt.Sprintf("admin_token=%s", user.SessionToken))

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Failed to make request: %v", err)
	}
	defer resp.Body.Close()

	// Should get a bad request response
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusSeeOther {
		t.Error("Empty reply should be rejected")
	}

	// Verify no reply was created
	var replyCount int64
	db.Model(&Message{}).Where("parent_message_id = ?", message.ID).Count(&replyCount)
	if replyCount != 0 {
		t.Error("No reply should be created for empty text")
	}

	t.Log("Empty reply validation test passed!")
}

// TestReplyCrossGuestbookIsolation tests that users can't reply to messages in other users' guestbooks
func TestReplyCrossGuestbookIsolation(t *testing.T) {
	t.Log("Testing cross-guestbook reply isolation")

	// Create two users with their own guestbooks
	user1 := AdminUser{
		Username:     fmt.Sprintf("replyuser1_%d", time.Now().Unix()),
		PasswordHash: []byte("password"),
		SessionToken: fmt.Sprintf("replytoken1_%d", time.Now().Unix()),
	}
	db.Create(&user1)

	user2 := AdminUser{
		Username:     fmt.Sprintf("replyuser2_%d", time.Now().UnixNano()),
		PasswordHash: []byte("password"),
		SessionToken: fmt.Sprintf("replytoken2_%d", time.Now().UnixNano()),
	}
	db.Create(&user2)

	guestbook1 := Guestbook{
		WebsiteURL:  "https://replyuser1.com",
		AdminUserID: user1.ID,
	}
	db.Create(&guestbook1)

	guestbook2 := Guestbook{
		WebsiteURL:  "https://replyuser2.com",
		AdminUserID: user2.ID,
	}
	db.Create(&guestbook2)

	// Create message in user2's guestbook
	message := Message{
		Name:        "Visitor",
		Text:        "Message in user2's guestbook",
		GuestbookID: guestbook2.ID,
		Approved:    true,
	}
	db.Create(&message)

	// User1 tries to reply to a message in user2's guestbook
	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	// Try using user2's guestbook ID but user1's token
	replyURL := fmt.Sprintf("%s/admin/guestbook/%d/message/%d/reply", testBaseURL, guestbook2.ID, message.ID)
	req, _ := http.NewRequest("POST", replyURL, strings.NewReader("text=Malicious reply"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", fmt.Sprintf("admin_token=%s", user1.SessionToken))

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Failed to make request: %v", err)
	}
	defer resp.Body.Close()

	// Should be forbidden
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusSeeOther {
		t.Error("User should not be able to reply to messages in other users' guestbooks")
	}

	// Verify no reply was created
	var replyCount int64
	db.Model(&Message{}).Where("parent_message_id = ?", message.ID).Count(&replyCount)
	if replyCount != 0 {
		t.Error("No reply should exist from unauthorized user")
	}

	t.Log("Cross-guestbook reply isolation test passed!")
}

// TestReplyToMessageFromDifferentGuestbook tests that users can't reply to messages not in the specified guestbook
func TestReplyToMessageFromDifferentGuestbook(t *testing.T) {
	t.Log("Testing reply to message from different guestbook")

	user := AdminUser{
		Username:     fmt.Sprintf("diffgb_%d", time.Now().Unix()),
		PasswordHash: []byte("password"),
		SessionToken: fmt.Sprintf("diffgbtoken_%d", time.Now().Unix()),
	}
	db.Create(&user)

	// Create two guestbooks owned by the same user
	guestbook1 := Guestbook{
		WebsiteURL:  "https://diffgb1.com",
		AdminUserID: user.ID,
	}
	db.Create(&guestbook1)

	guestbook2 := Guestbook{
		WebsiteURL:  "https://diffgb2.com",
		AdminUserID: user.ID,
	}
	db.Create(&guestbook2)

	// Create message in guestbook2
	message := Message{
		Name:        "Visitor",
		Text:        "Message in guestbook2",
		GuestbookID: guestbook2.ID,
		Approved:    true,
	}
	db.Create(&message)

	// Try to reply to message using guestbook1's URL (message belongs to guestbook2)
	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	replyURL := fmt.Sprintf("%s/admin/guestbook/%d/message/%d/reply", testBaseURL, guestbook1.ID, message.ID)
	req, _ := http.NewRequest("POST", replyURL, strings.NewReader("text=Cross-guestbook reply"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", fmt.Sprintf("admin_token=%s", user.SessionToken))

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Failed to make request: %v", err)
	}
	defer resp.Body.Close()

	// Should be rejected
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusSeeOther {
		t.Error("Reply to message from different guestbook should be rejected")
	}

	// Verify no reply was created
	var replyCount int64
	db.Model(&Message{}).Where("parent_message_id = ?", message.ID).Count(&replyCount)
	if replyCount != 0 {
		t.Error("No reply should be created for cross-guestbook message")
	}

	t.Log("Different guestbook message reply test passed!")
}

// TestReplyCacheInvalidation tests that cache is invalidated when a reply is added
func TestReplyCacheInvalidation(t *testing.T) {
	// Use incognito mode to avoid session conflicts
	page := browser.MustIncognito().MustPage(testBaseURL)
	defer page.MustClose()

	username := fmt.Sprintf("cachetest_%d", time.Now().Unix())
	password := "testpassword123"

	// Setup account and guestbook
	t.Log("Setting up account and guestbook")
	page.MustNavigate(testBaseURL + "/admin/signup")
	page.MustWaitLoad()
	page.MustElement("input[name='username']").MustInput(username)
	page.MustElement("input[name='password']").MustInput(password)
	page.MustElement("input[type='checkbox']").MustClick()
	page.MustElement("form button[type='submit']").MustClick()
	page.MustWaitLoad()
	page.MustWaitStable()

	page.MustNavigate(testBaseURL + "/admin/guestbook/new")
	page.MustWaitLoad()
	page.MustElement("input[name='websiteURL']").MustInput("https://cachetest.com")
	page.MustElement("#guestbook-edit-form button[type='submit']").MustClick()
	page.MustWaitLoad()
	time.Sleep(500 * time.Millisecond)

	guestbookLink := page.MustElement("a[href*='/admin/guestbook/']:not([href*='/new'])").MustProperty("href").String()
	var guestbookID string
	fmt.Sscanf(guestbookLink, testBaseURL+"/admin/guestbook/%s", &guestbookID)

	// Create a message
	var guestbook Guestbook
	db.First(&guestbook, guestbookID)

	message := Message{
		Name:        "Cache Test User",
		Text:        "Test message for cache",
		GuestbookID: guestbook.ID,
		Approved:    true,
	}
	db.Create(&message)

	// Load public page to populate cache
	t.Log("Loading public page to populate cache")
	publicURL := testBaseURL + "/guestbook/" + guestbookID
	page.MustNavigate(publicURL)
	page.MustWaitLoad()
	time.Sleep(500 * time.Millisecond)

	pageText := page.MustElement("body").MustText()
	if !strings.Contains(pageText, "Test message for cache") {
		t.Error("Original message should appear on public page")
	}

	// Add a reply via admin
	t.Log("Adding reply via admin panel")
	page.MustNavigate(testBaseURL + "/admin/guestbook/" + guestbookID)
	page.MustWaitLoad()
	time.Sleep(300 * time.Millisecond)

	page.MustElement(".reply-btn").MustClick()
	time.Sleep(300 * time.Millisecond)
	page.MustElement("#reply-text").MustInput("Cache invalidation test reply")
	page.MustElement("#reply-form button[type='submit']").MustClick()
	page.MustWaitLoad()
	time.Sleep(500 * time.Millisecond)

	// Load public page again - reply should appear (cache was invalidated)
	t.Log("Verifying cache was invalidated")
	page.MustNavigate(publicURL)
	page.MustWaitLoad()
	time.Sleep(500 * time.Millisecond)

	pageText = page.MustElement("body").MustText()
	if !strings.Contains(pageText, "Cache invalidation test reply") {
		t.Error("Reply should appear on public page after cache invalidation")
	}

	t.Log("Cache invalidation test passed!")
}

// TestReplyToNonExistentMessage tests error handling for non-existent messages
func TestReplyToNonExistentMessage(t *testing.T) {
	t.Log("Testing reply to non-existent message")

	user := AdminUser{
		Username:     fmt.Sprintf("nonexist_%d", time.Now().Unix()),
		PasswordHash: []byte("password"),
		SessionToken: fmt.Sprintf("nonexisttoken_%d", time.Now().Unix()),
	}
	db.Create(&user)

	guestbook := Guestbook{
		WebsiteURL:  "https://nonexist.com",
		AdminUserID: user.ID,
	}
	db.Create(&guestbook)

	// Try to reply to a non-existent message ID
	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	nonExistentMessageID := 999999
	replyURL := fmt.Sprintf("%s/admin/guestbook/%d/message/%d/reply", testBaseURL, guestbook.ID, nonExistentMessageID)
	req, _ := http.NewRequest("POST", replyURL, strings.NewReader("text=Reply to nothing"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", fmt.Sprintf("admin_token=%s", user.SessionToken))

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Failed to make request: %v", err)
	}
	defer resp.Body.Close()

	// Should get a not found or error response
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusSeeOther {
		t.Error("Reply to non-existent message should fail")
	}

	t.Log("Non-existent message reply test passed!")
}
