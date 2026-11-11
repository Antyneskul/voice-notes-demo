package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
)

// Each upstream call gets its own deadline, on top of the request's context.
const (
	readTimeout   = 60 * time.Second // time for a phone to upload up to ~11 MiB
	openAITimeout = 60 * time.Second // audio models can take a while on longer notes
	notionTimeout = 15 * time.Second
)

// buildServer creates the real OpenAI and Notion clients from config.
func buildServer(cfg Config, log *slog.Logger) *Server {
	httpClient := &http.Client{} // deadlines come from per-call contexts
	return &Server{
		Extractor: &OpenAIExtractor{
			BaseURL: cfg.OpenAIBaseURL, APIKey: cfg.OpenAIKey, Model: cfg.OpenAIModel,
			HTTP: httpClient, Timeout: openAITimeout,
		},
		Exporter: &NotionExporter{
			BaseURL: cfg.NotionBaseURL, Token: cfg.NotionKey, ParentID: cfg.NotionParentID,
			HTTP: httpClient, Timeout: notionTimeout,
		},
		Log: log,
	}
}

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	// Optional: load ./.env. Variables already set in the shell win over it.
	_ = godotenv.Load()

	cfg, err := loadConfig(os.Getenv)
	if err != nil {
		log.Error("invalid configuration", "error", err.Error())
		os.Exit(1)
	}

	gin.SetMode(gin.ReleaseMode)
	srv := &http.Server{
		Addr:              ":" + cfg.Port, // all interfaces, so a phone on the same Wi-Fi can connect
		Handler:           buildServer(cfg, log).Router(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       readTimeout,
		// net/http starts this clock when the request headers are read, so it must cover the
		// upload as well as both upstream calls, plus some margin.
		WriteTimeout: readTimeout + openAITimeout + notionTimeout + 15*time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Run the server in a goroutine; the main goroutine waits for Ctrl+C or a failure.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	log.Info("listening", "addr", srv.Addr, "openai_model", cfg.OpenAIModel)

	select {
	case err := <-errCh:
		log.Error("server stopped", "error", err.Error())
		os.Exit(1)
	case <-ctx.Done():
		log.Info("shutting down")
	}

	// Let in-flight requests finish (up to 15s) before exiting.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error("shutdown incomplete", "error", err.Error())
	}
}
