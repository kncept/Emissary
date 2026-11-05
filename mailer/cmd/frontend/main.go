package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/kncept/Emissary/mailer/auth"
	"github.com/kncept/Emissary/mailer/awsutil"
	"github.com/kncept/Emissary/mailer/config"
	"github.com/kncept/Emissary/mailer/gui"
	"github.com/kncept/Emissary/mailer/metadata"
	"github.com/kncept/Emissary/mailer/sender"
	"github.com/kncept/Emissary/mailer/server"
	"github.com/kncept/Emissary/mailer/storage"
)

func main() {
	if awsutil.IsAwsLambda() {
		runAsAwsLambda()
		return
	}

	runAsHttpService()
}

func runAsAwsLambda() {}

func runAsHttpService() {
	logger := log.New(os.Stdout, "", log.LstdFlags)

	cfg, err := config.Load()
	if err != nil {
		logger.Fatalf("configuration error: %v", err)
	}
	if cfg.JWTGenerated {
		logger.Print("warning: EMISSARY_JWT_SECRET is not set; using an ephemeral secret (tokens will not survive a restart)")
	}

	ctx := context.Background()
	store, err := buildStorage(ctx, cfg)
	if err != nil {
		logger.Fatalf("storage: %v", err)
	}
	mailSender, err := buildSender(ctx, cfg)
	if err != nil {
		logger.Fatalf("sender: %v", err)
	}
	mailMetadata, err := buildMetadata(ctx, cfg)
	if err != nil {
		logger.Fatalf("metadata: %v", err)
	}

	authenticator := auth.New(cfg.Username, cfg.Password, cfg.JWTSecret, cfg.JWTExpiry)
	handler := server.New(server.Config{
		Auth:     authenticator,
		Storage:  store,
		Sender:   mailSender,
		Metadata: mailMetadata,
		GUI:      gui.Files,
		Logger:   logger,
	})

	httpServer := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	shutdownContext, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-shutdownContext.Done()
		timeoutContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(timeoutContext); err != nil {
			logger.Printf("shutdown: %v", err)
		}
	}()

	logger.Printf("Emissary listening on %s (storage=%s sender=%s metadata=%s)",
		cfg.ListenAddr, store.Name(), mailSender.Name(), mailMetadata.Name())

	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Fatalf("http server: %v", err)
	}
}

func buildStorage(ctx context.Context, cfg *config.Config) (storage.Storage, error) {
	switch cfg.Storage {
	case "memory":
		return storage.NewMemoryStorage(), nil
	case "s3":
		return storage.NewS3Storage(ctx, cfg.AWSRegion, cfg.S3Bucket, cfg.S3Prefix, cfg.S3Endpoint)
	default:
		return nil, fmt.Errorf("unknown storage %q", cfg.Storage)
	}
}

func buildSender(ctx context.Context, cfg *config.Config) (sender.Sender, error) {
	switch cfg.Sender {
	case "log":
		return sender.NewLogSender(cfg.SesFrom), nil
	case "aws-ses", "ses":
		return sender.NewAwsSesSender(ctx, cfg.AWSRegion, cfg.SesFrom)
	default:
		return nil, fmt.Errorf("unknown sender %q", cfg.Sender)
	}
}

func buildMetadata(ctx context.Context, cfg *config.Config) (metadata.Metadata, error) {
	switch cfg.Metadata {
	case "memory":
		return metadata.NewMemoryMetadata(), nil
	case "file":
		return metadata.NewFileMetadata(cfg.MetadataFile)
	case "ddb", "dynamodb":
		return metadata.NewDdbMetadata(ctx, cfg.AWSRegion, cfg.DdbTable)
	default:
		return nil, fmt.Errorf("unknown metadata provider %q", cfg.Metadata)
	}
}
