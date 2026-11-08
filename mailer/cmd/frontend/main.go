package main

import (
	"log"
	"net/http"
	"os"
	"time"

	"github.com/awslabs/aws-lambda-go-api-proxy/httpadapter"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/kncept/Emissary/mailer/awsutil"
	"github.com/kncept/Emissary/mailer/handler"
)

func main() {
	// Load configuration from environment
	authUser := os.Getenv("AUTH_USERNAME")
	authPass := os.Getenv("AUTH_PASSWORD")
	s3Bucket := os.Getenv("S3_BUCKET")
	s3Prefix := os.Getenv("S3_PREFIX")
	s3Region := os.Getenv("S3_REGION")
	metadataType := os.Getenv("METADATA_TYPE")
	metadataPath := os.Getenv("METADATA_PATH")
	metadataTable := os.Getenv("METADATA_TABLE")
	metadataRegion := os.Getenv("METADATA_REGION")

	// Defaults
	if s3Region == "" {
		s3Region = "us-east-1"
	}
	if metadataRegion == "" {
		metadataRegion = "us-east-1"
	}
	if metadataType == "" {
		metadataType = "memory"
	}

	// Validate required config
	if authUser == "" || authPass == "" {
		log.Fatal("AUTH_USERNAME and AUTH_PASSWORD must be set")
	}
	if s3Bucket == "" {
		log.Fatal("S3_BUCKET must be set")
	}

	// Initialize the JWT authenticator
	jwtSecret := os.Getenv("JWT_SECRET")
	if jwtSecret == "" {
		jwtSecret = os.Getenv("SESSION_SECRET") // backward compatibility
	}

	jwtTTL := 24 * time.Hour
	if ttl := os.Getenv("JWT_TTL"); ttl != "" {
		parsed, err := time.ParseDuration(ttl)
		if err != nil {
			log.Fatalf("invalid JWT_TTL %q: %v", ttl, err)
		}
		jwtTTL = parsed
	}

	cfg := handler.HandlerConfig{
		AuthUser:      authUser,
		AuthPass:      authPass,
		JWTSecret:     jwtSecret,
		JWTSecretTTL:  jwtTTL,
		S3Bucket:      s3Bucket,
		S3Prefix:      s3Prefix,
		S3Region:      s3Region,
		MetadataType:  metadataType,
		MetadataPath:  metadataPath,
		MetadataTable: metadataTable,
		MetadataRegion: metadataRegion,
	}

	h, err := handler.New(cfg)
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

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	log.Printf("Server starting on port %s", port)
	if err := http.ListenAndServe(":"+port, h); err != nil {
		log.Fatal(err)
	}
}