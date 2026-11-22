package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/awslabs/aws-lambda-go-api-proxy/httpadapter"
	"github.com/kncept/Emissary/mailer/awsutil"
	"github.com/kncept/Emissary/mailer/config"
	configvendor "github.com/kncept/Emissary/mailer/config-vendor"
	"github.com/kncept/Emissary/mailer/email"
	"github.com/kncept/Emissary/mailer/envutil"
	"github.com/kncept/Emissary/mailer/handler"
	"github.com/kncept/Emissary/mailer/logging"
)

const (
	readHeaderTimeout = 5 * time.Second
	readTimeout       = 15 * time.Second
	writeTimeout      = 30 * time.Second
	idleTimeout       = 60 * time.Second
	shutdownTimeout   = 15 * time.Second
)

func main() {
	logging.Configure()
	slog.Info(envutil.StartupMessage("frontend"))
	if err := run(); err != nil {
		slog.Error("frontend stopped", "err", err)
		os.Exit(1)
	}
}

func run() error {
	// SIGINT/SIGTERM stop accepting new connections and give in-flight
	// requests a bounded window to finish instead of being cut off mid-reply.
	// The context is created here (rather than just before ListenAndServe) so
	// components that own long-lived connections can be released on shutdown;
	// otherwise http.Server.Shutdown waits for open EventSource streams until
	// the graceful timeout expires.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load(ctx)
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}

	// Cap MIME parsing at the same size advertised for JMAP uploads/requests.
	// This is process-wide (storage and receiver reads use it too), so it is
	// done here before component construction. handler.New applies the same
	// limit only if a caller has not already set one.
	email.SetMaxPartSize(cfg.MaxFileSize)

	// Vend every runtime component from its config subtype. The same factory
	// is used by cmd/s3-receiver, so backends are registered in one place.
	components, err := configvendor.FromConfig(ctx, cfg)
	if err != nil {
		return fmt.Errorf("create components from configuration: %w", err)
	}

	isLambda := awsutil.IsAwsLambda()
	h, err := handler.New(ctx, cfg, components, handler.Options{IsLambda: isLambda})
	if err != nil {
		return fmt.Errorf("create handler: %w", err)
	}

	// The application http.Handler is fully built at this point. In Lambda,
	// hand it to the runtime instead of starting a listener of our own; the
	// adapter translates API Gateway events into requests.
	if isLambda {
		slog.Info("Lambda listener loaded")
		lambda.Start(httpadapter.New(h).ProxyWithContext)
		return nil
	}

	port := cfg.Server.ListenAddr
	if !strings.HasPrefix(port, ":") {
		port = ":" + port
	}
	server := newHTTPServer(port, h)

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- server.ListenAndServe()
	}()

	slog.Info("Server starting", "addr", port)
	select {
	case err := <-serveErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve on %s: %w", port, err)
	case <-ctx.Done():
		slog.Info("Shutdown signal received", "addr", port)
		// Cancel the component context first so long-lived subscribers such as
		// EventSource streams are closed before we wait for connections to drain.
		stop()
		gracefulCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := server.Shutdown(gracefulCtx); err != nil {
			return fmt.Errorf("graceful shutdown: %w", err)
		}
		if err := <-serveErr; err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("serve on %s: %w", port, err)
		}
		slog.Info("Server stopped", "addr", port)
		return nil
	}
}

// newHTTPServer applies bounded read/write/idle times so a slow or abandoned
// client cannot hold a connection open indefinitely.
func newHTTPServer(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
	}
}
