package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestRuntimeChild(t *testing.T) {
	if os.Getenv("GUESTBOOKS_RUNTIME_CHILD") != "1" {
		return
	}
	// Only the controlled parent test sets this directory to a synthetic fixture.
	if err := os.Chdir(os.Getenv("GUESTBOOKS_RUNTIME_DIRECTORY")); err != nil {
		t.Fatal(err)
	}
	os.Args = []string{"guestbooks"}
	if err := runApplication(); err != nil {
		t.Log(err)
		os.Exit(2)
	}
	os.Exit(0)
}

func runtimeFixture(t *testing.T, port int) string {
	t.Helper()
	dir := t.TempDir()
	config := "server:\n  port: " + strconv.Itoa(port) + "\n  bind_host: 127.0.0.1\n  public_url: http://127.0.0.1:" + strconv.Itoa(port) + "\nadmin:\n  allow_signups: false\nmailer:\n  mailer_name: none\n"
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	// Symlink only source templates/assets; all mutable state stays in dir.
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"templates", "assets"} {
		if err := os.Symlink(filepath.Join(cwd, name), filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func startRuntimeChild(t *testing.T, directory string) (*exec.Cmd, <-chan error) {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^TestRuntimeChild$")
	command.Env = append(os.Environ(), "GUESTBOOKS_RUNTIME_CHILD=1", "GUESTBOOKS_RUNTIME_DIRECTORY="+directory)
	logFile, err := os.Create(filepath.Join(directory, "runtime.log"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { logFile.Close() })
	command.Stdout, command.Stderr = logFile, logFile
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- command.Wait()
		close(done)
	}()
	t.Cleanup(func() {
		select {
		case <-done:
		default:
			command.Process.Kill()
			<-done
		}
	})
	return command, done
}

func TestRuntimeOccupiedPortExits(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	_, done := startRuntimeChild(t, runtimeFixture(t, port))
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("occupied port exited successfully")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("occupied port left a live process")
	}
}

func TestRuntimeGracefulTermination(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	directory := runtimeFixture(t, port)
	fixtureDB, err := gorm.Open(sqlite.Open(filepath.Join(directory, "guestbook.db")), &gorm.Config{Logger: databaseLogger})
	if err != nil {
		t.Fatal(err)
	}
	if err := fixtureDB.AutoMigrate(&AdminUser{}, &Guestbook{}, &Message{}); err != nil {
		t.Fatal(err)
	}
	user := AdminUser{Username: "runtime-owner", SessionToken: "runtime-token", PasswordHash: []byte("test")}
	if err := fixtureDB.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	book := Guestbook{AdminUserID: user.ID, WebsiteURL: "https://example.test"}
	if err := fixtureDB.Create(&book).Error; err != nil {
		t.Fatal(err)
	}
	connection, err := fixtureDB.DB()
	if err != nil {
		t.Fatal(err)
	}
	connection.Close()
	command, done := startRuntimeChild(t, directory)
	base := "http://127.0.0.1:" + strconv.Itoa(port)
	client := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(10 * time.Second)
	ready := false
	for time.Now().Before(deadline) {
		response, err := client.Get(base + "/healthz")
		if err == nil {
			response.Body.Close()
			if response.StatusCode == 200 {
				ready = true
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !ready {
		t.Fatal("child did not become ready")
	}
	socket, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer socket.Close()
	socket.SetDeadline(time.Now().Add(10 * time.Second))
	body := "name=visitor&text=drained"
	fmt.Fprintf(socket, "POST /guestbook/%d/submit HTTP/1.1\r\nHost: 127.0.0.1:%d\r\nContent-Type: application/x-www-form-urlencoded\r\nAccept: application/json\r\nContent-Length: %d\r\nExpect: 100-continue\r\nConnection: close\r\n\r\n", book.ID, port, len(body))
	reader := bufio.NewReader(socket)
	continued, err := http.ReadResponse(reader, nil)
	if err != nil || continued.StatusCode != 100 {
		t.Fatalf("submission did not reach body read: response=%v error=%v", continued, err)
	}
	continued.Body.Close()
	if err := command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		t.Fatalf("server exited before draining submission: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if _, err := io.WriteString(socket, body); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 201 {
		t.Fatalf("drained submission returned %d", response.StatusCode)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("termination: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("child did not finish shutdown")
	}
}

func TestRuntimeHealthConfiguredPort(t *testing.T) {
	server := httptest.NewServer(initRouter())
	defer server.Close()
	previous := appConfig
	t.Cleanup(func() { appConfig = previous })
	host, port, err := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	appConfig.BindHost = host
	appConfig.Port, _ = strconv.Atoi(port)
	if err := checkHealth(); err != nil {
		t.Fatal(err)
	}
	router := initRouter()
	for i := 0; i < 110; i++ {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest("GET", "/healthz", nil))
		requireStatus(t, response, 200)
	}
}

func TestRuntimeProofCleanupStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := NewChallengeStore().StartCleanupLoop(ctx)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("proof cleanup did not stop")
	}
}

func TestSessionMigrationDefault(t *testing.T) {
	user, _ := featureFixture(t)
	if user.SessionExpiresAt <= time.Now().Unix() {
		t.Fatal("new test sessions must have explicit expiry")
	}
	if err := db.Model(&user).Update("session_expires_at", 0).Error; err != nil {
		t.Fatal(err)
	}
	requireStatus(t, featureRequest(initRouter(), "GET", "/admin/settings", nil, &user, true), 401)
}

func TestLegacyAccountSchemaMigration(t *testing.T) {
	migration, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "legacy.db")), &gorm.Config{Logger: databaseLogger})
	if err != nil {
		t.Fatal(err)
	}
	connection, err := migration.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	for _, query := range []string{
		"CREATE TABLE admin_users (id INTEGER PRIMARY KEY, username TEXT, session_token TEXT, password_hash TEXT)",
		"INSERT INTO admin_users VALUES (1,'legacy-one','old-session-one','old-hash-one'),(2,'legacy-two','old-session-two','old-hash-two')",
	} {
		if err := migration.Exec(query).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := migration.AutoMigrate(&Guestbook{}, &Message{}, &AdminUser{}); err != nil {
		t.Fatal(err)
	}
	var users []AdminUser
	if err := migration.Order("id").Find(&users).Error; err != nil {
		t.Fatal(err)
	}
	if len(users) != 2 {
		t.Fatal("migration lost accounts")
	}
	for _, user := range users {
		if user.SessionExpiresAt != 0 || user.VerificationAttemptAt != 0 || !strings.HasPrefix(user.SessionToken, "old-session-") {
			t.Fatal("migration must expire legacy sessions without rewriting account identity")
		}
	}
}
