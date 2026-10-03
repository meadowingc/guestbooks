package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

func runApplication() error {
	if err := loadConfiguration(); err != nil {
		return err
	}
	if len(os.Args) > 1 {
		if len(os.Args) == 2 && os.Args[1] == "healthcheck" {
			return checkHealth()
		}
		return errors.New("usage: guestbooks [healthcheck]")
	}
	if err := initDatabase(); err != nil {
		return err
	}
	connection, err := db.DB()
	if err != nil {
		return err
	}
	defer func() {
		if err := connection.Close(); err != nil {
			log.Printf("Close database: %v", err)
		}
	}()
	if err := initCache(); err != nil {
		return err
	}
	powChallengeStore = NewChallengeStore()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	return serveApplication(ctx)
}

func serveApplication(ctx context.Context) error {
	listener, err := net.Listen("tcp", net.JoinHostPort(appConfig.BindHost, strconv.Itoa(appConfig.Port)))
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	defer listener.Close()
	cleanupContext, stopCleanup := context.WithCancel(context.Background())
	cleanupDone := powChallengeStore.StartCleanupLoop(cleanupContext)
	defer func() {
		stopCleanup()
		<-cleanupDone
	}()
	server := &http.Server{
		Handler:           initRouter(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      45 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    64 << 10,
	}
	serveResult := make(chan error, 1)
	go func() { serveResult <- server.Serve(listener) }()
	log.Printf("Listening on %s (public URL: %s)", listener.Addr(), PublicURL())
	var serveErr error
	select {
	case <-ctx.Done():
	case serveErr = <-serveResult:
	}
	drain, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	shutdownErr := server.Shutdown(drain)
	if shutdownErr != nil {
		log.Printf("HTTP shutdown did not drain all requests: %v", shutdownErr)
		if err := server.Close(); err != nil {
			log.Printf("Force close HTTP server: %v", err)
		}
	}
	mailErr := shutdownMail(drain)
	if errors.Is(serveErr, http.ErrServerClosed) {
		serveErr = nil
	}
	return errors.Join(serveErr, shutdownErr, mailErr)
}

func HealthHandler(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), time.Second)
	defer cancel()
	connection, err := db.DB()
	if err == nil {
		err = connection.PingContext(ctx)
	}
	if err != nil {
		log.Printf("Health database check failed: %v", err)
		http.Error(w, "Not ready", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Write([]byte("ok\n"))
}

func checkHealth() error {
	host := appConfig.BindHost
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil}}
	response, err := client.Get("http://" + net.JoinHostPort(host, strconv.Itoa(appConfig.Port)) + "/healthz")
	if err != nil {
		return fmt.Errorf("healthcheck: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("healthcheck returned HTTP %d", response.StatusCode)
	}
	return nil
}
