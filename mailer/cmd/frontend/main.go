package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/awslabs/aws-lambda-go-api-proxy/httpadapter"
	"github.com/kncept/Emissary/mailer/awsutil"
	"github.com/kncept/Emissary/mailer/config"
	configvendor "github.com/kncept/Emissary/mailer/config-vendor"
	"github.com/kncept/Emissary/mailer/envutil"
	"github.com/kncept/Emissary/mailer/handler"
	"github.com/kncept/Emissary/mailer/logging"
)

func main() {
	logging.Configure()
	slog.Info(envutil.StartupMessage("frontend"))

	ctx := context.Background()

	// Load configuration using the config package
	cfg, err := config.Load(ctx)
	if err != nil {
		slog.Error("Failed to load configuration", "err", err)
		os.Exit(1)
	}

	// Vend every runtime component from its config subtype. The same factory
	// is used by cmd/s3-receiver, so backends are registered in one place.
	components, err := configvendor.FromConfig(ctx, cfg)
	if err != nil {
		slog.Error("Failed to create components from configuration", "err", err)
		os.Exit(1)
	}

	// Build handler config from loaded config
	handlerCfg := handler.HandlerConfig{
		AuthUser:       cfg.Server.Username,
		AuthPass:       cfg.Server.Password,
		ListenAddr:     cfg.Server.ListenAddr,
		JWTSecret:      cfg.Server.JWTSecret,
		JWTSecretTTL:   cfg.Server.JWTExpiry,
		S3Bucket:       cfg.Storage.S3Bucket,
		FilesystemDir:  cfg.FilesystemDir,
		StorageType:    cfg.Storage.Type,
		AWSRegion:      cfg.AWSRegion,
		MetadataType:   cfg.Metadata.Type,
		MetadataPath:   cfg.Metadata.FilePath,
		MetadataTable:  cfg.Metadata.DdbTable,
		MetadataRegion: cfg.Metadata.Region,
		DDBTablePrefix: cfg.DDBTablePrefix,
		SessionType:    cfg.Session.Type,
		SessionTable:   cfg.Session.DdbTable,
		SessionRegion:  cfg.Session.Region,
		JWTAudience:    cfg.Server.JWTAudience,
		JWTIssuer:      cfg.Server.JWTIssuer,

		Storage:             components.Storage,
		Metadata:            components.Metadata,
		Session:             components.Session,
		UserManager:         components.UserManager,
		Sender:              components.Sender,
		ReceiverConfigStore: components.ReceiverConfigStore,
		ReceiverConfig:      components.ReceiverConfig,

		UserManagerType:  cfg.UserManager.Type,
		UserDomain:       cfg.UserManager.Domain,
		UserTable:        cfg.UserManager.DdbTable,
		UserRegion:       cfg.UserManager.Region,
		LDAPURL:          cfg.UserManager.LDAPURL,
		LDAPBindDN:       cfg.UserManager.LDAPBindDN,
		LDAPBindPassword: cfg.UserManager.LDAPBindPassword,
		LDAPBaseDN:       cfg.UserManager.LDAPBaseDN,
		LDAPUserFilter:   cfg.UserManager.LDAPUserFilter,
		LDAPCacheTTL:     cfg.UserManager.LDAPCacheTTL,

		SenderType:    cfg.Sender.Type,
		SenderMTAPort: cfg.Sender.MTAPort,

		ReceiverConfigType:    cfg.Receiver.Type,
		ReceiverBucket:        cfg.Receiver.S3Bucket,
		ReceiverFilesystemDir: cfg.Receiver.FilesystemDir,
		ReceiverTable:         cfg.ReceiverTableName(),
		ReceiverRegion:        cfg.Receiver.Region,

		IsLambda: awsutil.IsAwsLambda(),
	}

	h, err := handler.New(handlerCfg)
	if err != nil {
		slog.Error("Failed to create handler", "err", err)
		os.Exit(1)
	}

	// The application http.Handler is fully built at this point. In Lambda,
	// hand it to the runtime instead of starting a listener of our own; the
	// adapter translates API Gateway events into requests.
	if awsutil.IsAwsLambda() {
		slog.Info("Lambda listener loaded")
		lambda.Start(httpadapter.New(h).ProxyWithContext)
		return
	}

	port := cfg.Server.ListenAddr
	if !strings.HasPrefix(port, ":") {
		port = ":" + port
	}
	slog.Info("Server starting", "addr", port)
	if err := http.ListenAndServe(port, h); err != nil {
		slog.Error("Server stopped", "addr", port, "err", err)
		os.Exit(1)
	}
}
