package main

import (
	"context"
	"log"
	"net/http"
	"strings"

	"github.com/awslabs/aws-lambda-go-api-proxy/httpadapter"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/kncept/Emissary/mailer/awsutil"
	"github.com/kncept/Emissary/mailer/config"
	"github.com/kncept/Emissary/mailer/handler"
)

func main() {
	ctx := context.Background()

	// Load configuration using the config package
	cfg, err := config.Load(ctx)
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}

	// Build handler config from loaded config
	handlerCfg := handler.HandlerConfig{
		AuthUser:       cfg.Server.Username,
		AuthPass:       cfg.Server.Password,
		JWTSecret:      cfg.Server.JWTSecret,
		JWTSecretTTL:   cfg.Server.JWTExpiry,
		S3Bucket:       cfg.Storage.S3Bucket,
		S3Region:       cfg.AWSRegion,
		MetadataType:   cfg.Metadata.Type,
		MetadataPath:   cfg.Metadata.FilePath,
		MetadataTable:  cfg.Metadata.DdbTable,
		MetadataRegion: cfg.Metadata.Region,
		JWTAudience:    cfg.Server.JWTAudience,
		JWTIssuer:      cfg.Server.JWTIssuer,
	}

	h, err := handler.New(handlerCfg)
	if err != nil {
		log.Fatalf("Failed to create handler: %v", err)
	}

	// The application http.Handler is fully built at this point. In Lambda,
	// hand it to the runtime instead of starting a listener of our own; the
	// adapter translates API Gateway events into requests.
	if awsutil.IsAwsLambda() {
		log.Printf("Lambda listener loaded")
		lambda.Start(httpadapter.New(h).ProxyWithContext)
		return
	}

	port := cfg.Server.ListenAddr
	if !strings.HasPrefix(port, ":") {
		port = ":" + port
	}
	log.Printf("Server starting on %s", port)
	if err := http.ListenAndServe(port, h); err != nil {
		log.Fatal(err)
	}
}