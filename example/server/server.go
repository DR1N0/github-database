package server

import (
	"context"
	"io/fs"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func handleAPI(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		http.NotFound(w, r)
		return
	}
	switch parts[0] {
	case "table":
		handleTable(w, r, parts[1:])
	case "checkpoint":
		handleCheckpoint(w, r)
	default:
		http.NotFound(w, r)
	}
}

func RunServer(offline bool, baseline fs.FS) {
	loadDotenv("../.env")

	mode := "online"
	if offline {
		mode = "offline"
	}

	if err := openDB(offline, baseline); err != nil {
		logger.Fatalf("openDB: %v", err)
	}

	http.HandleFunc("/api/v1/", handleAPI)
	srv := &http.Server{Addr: ":8080"}
	logger.Printf("ghdb demo server listening on :8080 (%s mode)", mode)

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Fatalf("ListenAndServe: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	logger.Println("shutting down...")

	shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		logger.Printf("shutdown: %v", err)
	}

	closeCtx, closeCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer closeCancel()
	if err := tableDB.Close(closeCtx); err != nil {
		logger.Printf("tableDB close: %v", err)
	}
	logger.Println("server stopped")
}
