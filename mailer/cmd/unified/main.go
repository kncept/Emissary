package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/awslabs/aws-lambda-go-api-proxy/httpadapter"
	awscfg "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/kncept/Emissary/mailer/awsutil"
	"github.com/kncept/Emissary/mailer/config"
	configvendor "github.com/kncept/Emissary/mailer/config-vendor"
	"github.com/kncept/Emissary/mailer/email"
	"github.com/kncept/Emissary/mailer/envutil"
	"github.com/kncept/Emissary/mailer/handler"
	"github.com/kncept/Emissary/mailer/jmap/delivery"
	"github.com/kncept/Emissary/mailer/logging"
	"github.com/kncept/Emissary/mailer/receiver"
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
	slog.Info(envutil.StartupMessage("unified"))
	if err := run(); err != nil {
		slog.Error("unified stopped", "err", err)
		os.Exit(1)
	}
}

func run() error {
	ctx := context.Background()

	cfg, err := config.Load(ctx)
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}

	email.SetMaxPartSize(cfg.MaxFileSize)

	components, err := configvendor.FromConfig(ctx, cfg)
	if err != nil {
		return fmt.Errorf("create components from configuration: %w", err)
	}

	isLambda := awsutil.IsAwsLambda()

	h, err := handler.New(ctx, cfg, components, handler.Options{IsLambda: isLambda})
	if err != nil {
		return fmt.Errorf("create handler: %w", err)
	}

	if !isLambda {
		port := cfg.Server.ListenAddr
		if !strings.HasPrefix(port, ":") {
			port = ":" + port
		}
		server := newHTTPServer(port, h)

		shutdownCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
		defer stop()
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
		case <-shutdownCtx.Done():
			slog.Info("Shutdown signal received", "addr", port)
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

	slog.Info("Lambda listener loaded")
	lambda.Start(unifiedLambdaHandler(h, cfg))
	return nil
}

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

type rawEventProcessor interface {
	ProcessEvent(ctx context.Context, event receiver.InboundRawEmailEvent) error
}

func processEvent(ctx context.Context, rcvr rawEventProcessor, s3Event events.S3Event, bucket string, s3Client receiver.S3API) error {
	var errs []error
	for _, record := range s3Event.Records {
		recordBucket := record.S3.Bucket.Name
		key := record.S3.Object.Key

		if !receiver.IsInboundEmailKey(key) {
			slog.Debug("Ignoring nested S3 object", "bucket", recordBucket, "key", key)
			continue
		}
		if recordBucket != bucket {
			slog.Warn("S3 object bucket does not match configured mail bucket",
				"key", key, "bucket", recordBucket, "expected_bucket", bucket)
			continue
		}

		slog.Debug("Processing S3 object", "bucket", recordBucket, "key", key)
		event := receiver.NewRawS3Event(s3Client, bucket, key)
		if err := rcvr.ProcessEvent(ctx, event); err != nil {
			slog.Error("Failed to process message", "key", key, "err", err)
			errs = append(errs, fmt.Errorf("process %s: %w", key, err))
		}
	}
	return errors.Join(errs...)
}

func unifiedLambdaHandler(httpHandler http.Handler, cfg *config.Config) func(context.Context, json.RawMessage) (json.RawMessage, error) {
	// Initialize S3 receiver components lazily on first S3 event
	var (
		s3Client     *s3.Client
		s3Receiver   rawEventProcessor
		initErr      error
		initDone     bool
	)

	initS3Receiver := func(ctx context.Context) error {
		if initDone {
			return initErr
		}
		initDone = true

		awsCfg, err := awscfg.LoadDefaultConfig(ctx)
		if err != nil {
			initErr = fmt.Errorf("failed to load AWS config: %w", err)
			return initErr
		}
		// Use concrete *s3.Client for both receiver and dedup
		// *s3.Client satisfies both receiver.S3API and receiver.DedupS3Client
		s3Client = s3.NewFromConfig(awsCfg)

		components, err := configvendor.FromConfig(ctx, cfg)
		if err != nil {
			initErr = fmt.Errorf("failed to create components from configuration: %w", err)
			return initErr
		}

		rcvr, err := receiver.NewReceiver(receiver.ReceiverConfig{
			UserManager:    components.UserManager,
			Sender:         components.Sender,
			Domain:         cfg.UserManager.Domain,
			Storage:        components.Storage,
			Metadata:       components.Metadata,
			Dedup:          receiver.NewS3Dedup(s3Client, cfg.Storage.S3Bucket),
			VacationStore:  components.VacationStore,
			IdentityConfig: components.ReceiverConfig,
			EmailDelivery:  delivery.New(components.Storage),
		})
		if err != nil {
			initErr = fmt.Errorf("failed to create receiver: %w", err)
			return initErr
		}
		s3Receiver = rcvr
		return nil
	}

	apiGatewayAdapter := httpadapter.New(httpHandler)

	return func(ctx context.Context, payload json.RawMessage) (json.RawMessage, error) {
		// Detect event type by checking for characteristic fields
		var rawEvent map[string]json.RawMessage
		if err := json.Unmarshal(payload, &rawEvent); err != nil {
			return nil, fmt.Errorf("failed to unmarshal event: %w", err)
		}

		// Check if it's an S3 event (has "Records" array with S3 structure)
		if _, hasRecords := rawEvent["Records"]; hasRecords {
			var s3Event events.S3Event
			if err := json.Unmarshal(payload, &s3Event); err != nil {
				return nil, fmt.Errorf("failed to unmarshal S3 event: %w", err)
			}

			if err := initS3Receiver(ctx); err != nil {
				return nil, err
			}

			if err := processEvent(ctx, s3Receiver, s3Event, cfg.Storage.S3Bucket, s3Client); err != nil {
				return nil, err
			}
			// S3 events don't return a response payload
			return json.RawMessage("{}"), nil
		}

		// Check if it's an API Gateway event (has "resource", "path", "httpMethod", etc.)
		if _, hasResource := rawEvent["resource"]; hasResource {
			var apiEvent events.APIGatewayProxyRequest
			if err := json.Unmarshal(payload, &apiEvent); err != nil {
				return nil, fmt.Errorf("failed to unmarshal API Gateway event: %w", err)
			}

			resp, err := apiGatewayAdapter.ProxyWithContext(ctx, apiEvent)
			if err != nil {
				return nil, err
			}

			responseBytes, err := json.Marshal(resp)
			if err != nil {
				return nil, fmt.Errorf("failed to marshal API Gateway response: %w", err)
			}
			return responseBytes, nil
		}

		// Check for API Gateway V2 (HTTP API) event structure
		if _, hasVersion := rawEvent["version"]; hasVersion {
			if _, hasRouteKey := rawEvent["routeKey"]; hasRouteKey {
				var apiEventV2 events.APIGatewayV2HTTPRequest
				if err := json.Unmarshal(payload, &apiEventV2); err != nil {
					return nil, fmt.Errorf("failed to unmarshal API Gateway V2 event: %w", err)
				}

				// Use V2 adapter
				v2Adapter := httpadapter.NewV2(httpHandler)
				resp, err := v2Adapter.ProxyWithContext(ctx, apiEventV2)
				if err != nil {
					return nil, err
				}

				responseBytes, err := json.Marshal(resp)
				if err != nil {
					return nil, fmt.Errorf("failed to marshal API Gateway V2 response: %w", err)
				}
				return responseBytes, nil
			}
		}

		// Check for ALB event structure
		if _, hasRequestContext := rawEvent["requestContext"]; hasRequestContext {
			var albEvent events.ALBTargetGroupRequest
			if err := json.Unmarshal(payload, &albEvent); err != nil {
				return nil, fmt.Errorf("failed to unmarshal ALB event: %w", err)
			}

			albAdapter := httpadapter.NewALB(httpHandler)
			resp, err := albAdapter.ProxyWithContext(ctx, albEvent)
			if err != nil {
				return nil, err
			}

			responseBytes, err := json.Marshal(resp)
			if err != nil {
				return nil, fmt.Errorf("failed to marshal ALB response: %w", err)
			}
			return responseBytes, nil
		}

		return nil, fmt.Errorf("unknown event type: cannot determine if APIGateway, S3, or ALB event")
	}
}