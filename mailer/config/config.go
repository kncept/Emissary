package config

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/kncept/Emissary/mailer/awsutil"
	"github.com/kncept/Emissary/mailer/envutil"
	"github.com/kncept/Emissary/mailer/fsutil"
)

// DefaultMaxFileSize is the default cap for a single JMAP request body or
// MIME message (10 MiB). MAX_FILESIZE overrides it in bytes.
const DefaultMaxFileSize int64 = 10 << 20

// ServerConfig holds HTTP server and authentication configuration.
type ServerConfig struct {
	ListenAddr string

	Username     string
	Password     string
	JWTSecret    string
	JWTGenerated bool
	JWTExpiry    time.Duration
	JWTAudience  string
	JWTIssuer    string

	// JMAPPublicURL is the absolute HTTPS base URL advertised in the JMAP
	// Session object (RFC 8620 section 2). The apiUrl, downloadUrl, uploadUrl
	// and eventSourceUrl templates are resolved against it. It must not have
	// a trailing slash. When empty (local development only), URLs fall back
	// to the incoming request's origin, so they may use http://.
	JMAPPublicURL string
	// JMAPEventSourceURL is the absolute base for the EventSource endpoint.
	// When empty it defaults to JMAPPublicURL. Deployments with a dedicated
	// streaming Function URL set this to that endpoint (RESOLVED-1).
	JMAPEventSourceURL string
	// JMAPRESTProxy marks the API Gateway REST Lambda. API Gateway caps
	// integrations at 29 seconds, so this deployment must never serve a
	// closeafter=no stream; clients use the advertised streaming origin.
	JMAPRESTProxy bool
}

// Validate checks that the server configuration is complete and consistent.
// A blank username or password is valid: it disables the built-in administrator
// login without preventing other user managers from working.
func (c *ServerConfig) Validate() error {
	var errs []error
	// Username and Password are optional - if either is missing, the admin
	// user is simply not created. Other user managers continue to work.
	return errors.Join(errs...)
}

// StorageConfig holds email storage configuration.
type StorageConfig struct {
	Type     string // "s3", "filesystem" or "memory"
	S3Bucket string
	// FilesystemDir is the shared root from FILESYSTEM_DIR. Mail is stored
	// under its "mail" subdirectory.
	FilesystemDir string
}

// Validate checks that the storage configuration is complete and consistent.
func (c *StorageConfig) Validate() error {
	var errs []error
	switch c.Type {
	case "s3", "filesystem", "memory":
	default:
		errs = append(errs, fmt.Errorf("unknown storage type %q (want s3, filesystem or memory)", c.Type))
	}
	if c.Type == "s3" && c.S3Bucket == "" {
		errs = append(errs, errors.New("storage=s3 requires a bucket (set S3_BUCKET)"))
	}
	if c.Type == "filesystem" && c.FilesystemDir == "" {
		errs = append(errs, errors.New("storage=filesystem requires a directory (set FILESYSTEM_DIR)"))
	}
	return errors.Join(errs...)
}

// SenderConfig holds outbound email sender configuration. The SES region is
// resolved from the AWS SDK configuration, not from the environment. There is
// no configured From address: each message carries the submitting user's own
// address (or the masquerade target's address).
type SenderConfig struct {
	Type    string // "aws-ses", "mta" or "log"
	MTAPort string // SMTP port for the MTA sender (default 25)
}

// IsSES reports whether outbound mail goes through SES, in which case From
// addresses must be checked against the account's verified identities.
func (c SenderConfig) IsSES() bool {
	switch strings.ToLower(strings.TrimSpace(c.Type)) {
	case "aws-ses", "ses":
		return true
	}
	return false
}

// Validate checks that the sender configuration is complete and consistent.
func (c *SenderConfig) Validate() error {
	var errs []error
	switch c.Type {
	case "aws-ses", "ses", "mta", "log":
	default:
		errs = append(errs, fmt.Errorf("unknown sender type %q (want aws-ses, mta or log)", c.Type))
	}
	return errors.Join(errs...)
}

// ReceiverConfig selects the backend holding inbound recipient routing rules.
// S3Bucket and FilesystemDir default to the shared S3_BUCKET and FILESYSTEM_DIR
// values, but can be set independently with RECEIVER_BUCKET and
// RECEIVER_FILESYSTEM_DIR.
type ReceiverConfig struct {
	Type          string // "s3", "filesystem", "memory", "ddb" or "dynamodb"
	S3Bucket      string
	FilesystemDir string
	DdbTable      string
	Region        string // AWS region for the "ddb" backend
}

// Validate checks that the receiver config backend is known and has a target.
func (c *ReceiverConfig) Validate() error {
	switch c.Type {
	case "s3":
		if strings.TrimSpace(c.S3Bucket) == "" {
			return errors.New("receiver s3 requires a bucket (set RECEIVER_BUCKET or S3_BUCKET)")
		}
	case "filesystem":
		if strings.TrimSpace(c.FilesystemDir) == "" {
			return errors.New("receiver filesystem requires a directory (set RECEIVER_FILESYSTEM_DIR or FILESYSTEM_DIR)")
		}
	case "memory", "ddb", "dynamodb":
	default:
		return fmt.Errorf("unknown receiver type %q (want s3, filesystem, memory or ddb)", c.Type)
	}
	return nil
}

// MetadataConfig holds email metadata storage configuration.
type MetadataConfig struct {
	Type     string // "memory", "file", "ddb", "dynamodb"
	DdbTable string
	// FilePath is the metadata file/directory. When METADATA_FILE is unset it
	// defaults to <FILESYSTEM_DIR>/metadata.
	FilePath string
	Region   string // AWS region for DDB
}

// SessionConfig holds session storage configuration for refresh tokens.
type SessionConfig struct {
	Type     string // "memory", "ddb", "dynamodb"
	DdbTable string
	Region   string // AWS region for DDB
}

// Validate checks that the metadata configuration is complete and consistent.
func (c *MetadataConfig) Validate() error {
	var errs []error
	switch c.Type {
	case "memory", "file", "ddb", "dynamodb":
	default:
		errs = append(errs, fmt.Errorf("unknown metadata type %q (want memory, file or ddb)", c.Type))
	}
	return errors.Join(errs...)
}

// Validate checks that the session configuration is complete and consistent.
func (c *SessionConfig) Validate() error {
	var errs []error
	switch c.Type {
	case "memory", "ddb", "dynamodb":
	default:
		errs = append(errs, fmt.Errorf("unknown session type %q (want memory, ddb or dynamodb)", c.Type))
	}
	return errors.Join(errs...)
}

// UserManagerConfig selects the authentication backend for ordinary users.
// The USERNAME/PASSWORD administrator is configured separately and
// is available with every backend.
type UserManagerConfig struct {
	Type   string // "none", "ddb", "filesystem", "ldap" or "s3"
	Domain string // receiving domain for mail delivery and reporting
	Region string // AWS region for the "ddb" and "s3" backends

	DdbTable string
	// FilesystemDir is the shared root from FILESYSTEM_DIR. Users are stored
	// under its "users" subdirectory.
	FilesystemDir string

	LDAPURL          string
	LDAPBindDN       string
	LDAPBindPassword string
	LDAPBaseDN       string
	LDAPUserFilter   string
	LDAPCacheTTL     string
}

// Validate checks that the user manager configuration is complete.
func (c *UserManagerConfig) Validate() error {
	var errs []error
	switch c.Type {
	case "none", "ddb", "dynamodb", "filesystem", "ldap", "s3":
	default:
		errs = append(errs, fmt.Errorf("unknown user manager type %q (want none, ddb, filesystem, ldap or s3); set it explicitly to opt into a local/no-op backend", c.Type))
	}
	if c.Type == "ldap" && c.LDAPURL == "" {
		errs = append(errs, errors.New("user manager ldap requires a server URL (set LDAP_URL)"))
	}
	if c.Type == "filesystem" && c.FilesystemDir == "" {
		errs = append(errs, errors.New("user manager filesystem requires a directory (set FILESYSTEM_DIR)"))
	}
	return errors.Join(errs...)
}

// Config is the complete runtime configuration for cmd/frontend.
type Config struct {
	Server      ServerConfig
	Storage     StorageConfig
	Sender      SenderConfig
	Receiver    ReceiverConfig
	Metadata    MetadataConfig
	Session     SessionConfig
	UserManager UserManagerConfig

	// AWSRegion is the default AWS region used when a specific service
	// region is not configured.
	AWSRegion string

	// FilesystemDir is the shared root for the filesystem-backed stores,
	// read from FILESYSTEM_DIR and defaulting to ~/.emissary. Storage uses
	// <root>/mail, the user manager <root>/users and metadata <root>/metadata.
	FilesystemDir string

	// DDBTablePrefix is prepended to logical DynamoDB table names when
	// building table names (see TABLE_PREFIX). It defaults to "emissary".
	DDBTablePrefix string

	// MaxFileSize caps a single JMAP request body and a single parsed MIME
	// message, in bytes. It comes from MAX_FILESIZE and defaults to 10 MiB.
	MaxFileSize int64
}

// Validate checks the entire configuration for consistency. It returns a
// composite error containing all validation failures from the sub-configs.
func (c *Config) Validate() error {
	errs := []error{
		c.Server.Validate(),
		c.Storage.Validate(),
		c.Sender.Validate(),
		c.Receiver.Validate(),
		c.Metadata.Validate(),
		c.Session.Validate(),
		c.UserManager.Validate(),
	}
	// The s3 user manager stores users in the mail storage bucket. The
	// storage validation already enforces S3_BUCKET when STORAGE=s3 (which
	// also covers the auto-selected backend); this catches an explicit
	// USER_MANAGER=s3 with no bucket at all.
	if c.UserManager.Type == "s3" && c.Storage.S3Bucket == "" {
		errs = append(errs, errors.New("user manager s3 requires a bucket (set S3_BUCKET)"))
	}
	return errors.Join(errs...)
}

// FilesystemMailDir returns the directory holding filesystem-backed mail.
func (c *Config) FilesystemMailDir() string {
	return filepath.Join(c.FilesystemDir, "mail")
}

// FilesystemUsersDir returns the directory holding filesystem-backed users.
func (c *Config) FilesystemUsersDir() string {
	return filepath.Join(c.FilesystemDir, "users")
}

// FilesystemMetadataDir returns the directory holding filesystem-backed
// metadata records.
func (c *Config) FilesystemMetadataDir() string {
	return filepath.Join(c.FilesystemDir, "metadata")
}

// ApplyDDBTablePrefix names tables the same way as the household project
// (TABLE_PREFIX): prefix joined with a separator and the logical table name.
// If prefix is empty, "emissary-" is used. If prefix does not already end with
// "-" or "_", "-" is inserted.
func ApplyDDBTablePrefix(prefix, name string) string {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		prefix = "emissary"
	}
	if strings.HasSuffix(prefix, "-") || strings.HasSuffix(prefix, "_") {
		return prefix + name
	}
	return prefix + "-" + name
}

func resolveDDBTable(explicit, prefix, logical string) string {
	if strings.TrimSpace(explicit) != "" {
		return strings.TrimSpace(explicit)
	}
	return ApplyDDBTablePrefix(prefix, logical)
}

// MetadataTableName returns the DynamoDB table name for message metadata.
func (c *Config) MetadataTableName() string {
	return resolveDDBTable(c.Metadata.DdbTable, c.DDBTablePrefix, "metadata")
}

// SessionTableName returns the DynamoDB table name for sessions.
func (c *Config) SessionTableName() string {
	return resolveDDBTable(c.Session.DdbTable, c.DDBTablePrefix, "sessions")
}

// UserTableName returns the DynamoDB table name for users.
func (c *Config) UserTableName() string {
	return resolveDDBTable(c.UserManager.DdbTable, c.DDBTablePrefix, "users")
}

// ReceiverTableName returns the DynamoDB table name for inbound routing
// rules. It follows the same TABLE_PREFIX derivation as the other tables.
func (c *Config) ReceiverTableName() string {
	return resolveDDBTable(c.Receiver.DdbTable, c.DDBTablePrefix, "receiver")
}

// backend records whether one implementation of a pluggable component has
// configuration present in the environment.
type backend struct {
	name       string
	configured bool
}

// backendResolver implements the single selection rule shared by every
// pluggable component:
//
//  1. An explicitly configured selector (STORAGE, SENDER, ...) always wins.
//  2. Otherwise the backend is chosen only when exactly one has configuration.
//  3. No configuration, or configuration for several backends, is an error.
//
// Step 3 is deliberate: a deployment that forgets to configure a component
// must fail loudly instead of silently losing mail with an in-memory store or
// failing to deliver it with the log sender. Those local implementations are
// still available, but only as an explicit opt-in.
type backendResolver struct {
	hasCreds       bool
	tablePrefixSet bool // TABLE_PREFIX/DDB_TABLE_PREFIX set explicitly in the environment
}

// s3 is configured when AWS credentials are available and a bucket is set.
// Every value it needs comes from static environment configuration.
func (r backendResolver) s3(buckets ...string) backend {
	configured := r.hasCreds
	if configured {
		configured = false
		for _, bucket := range buckets {
			if strings.TrimSpace(bucket) != "" {
				configured = true
				break
			}
		}
	}
	return backend{name: "s3", configured: configured}
}

// ddb is configured when AWS credentials are available and either this
// component's table name or a table prefix was set explicitly in the
// environment. The table name is passed in because each component has its own
// override (DDB_TABLE, SESSION_TABLE, RECEIVER_TABLE, USER_TABLE).
func (r backendResolver) ddb(table string) backend {
	return backend{
		name:       "ddb",
		configured: r.hasCreds && (strings.TrimSpace(table) != "" || r.tablePrefixSet),
	}
}

// filesystem is configured when one of the given root environment variables
// is set. The shared FILESYSTEM_DIR default (~/.emissary) never counts.
func (r backendResolver) filesystem(dirs ...string) backend {
	return r.filesystemNamed("filesystem", dirs...)
}

// filesystemNamed is filesystem with the selector name used by components
// (for example "file" for metadata) that call their filesystem backend "file".
func (r backendResolver) filesystemNamed(name string, dirs ...string) backend {
	configured := false
	for _, dir := range dirs {
		if strings.TrimSpace(dir) != "" {
			configured = true
			break
		}
	}
	return backend{name: name, configured: configured}
}

// ses is configured when the SDK can supply AWS credentials (including IAM
// roles that have no AWS_* environment variables).
func (r backendResolver) ses() backend {
	return backend{name: "aws-ses", configured: r.hasCreds}
}

// mta is configured when its port was set explicitly; MTA_PORT's default of
// 25 does not count as configuration.
func (r backendResolver) mta(port string) backend {
	return backend{name: "mta", configured: strings.TrimSpace(port) != ""}
}

// ldap is configured when a server URL is set explicitly.
func (r backendResolver) ldap(url string) backend {
	return backend{name: "ldap", configured: strings.TrimSpace(url) != ""}
}

// selectType applies the shared rule to one component. noop names the explicit
// local/no-op value (memory, log or none) shown in the error; hint lists the
// environment configurations that count for this component.
func (r backendResolver) selectType(component, selector, explicit, noop, hint string, candidates ...backend) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	var configured []string
	for _, candidate := range candidates {
		if candidate.configured {
			configured = append(configured, candidate.name)
		}
	}
	switch len(configured) {
	case 1:
		return configured[0], nil
	case 0:
		return "", fmt.Errorf("%s backend is not configured: set %s=%s to opt into the local/no-op implementation, or configure exactly one backend (%s)", component, selector, noop, hint)
	default:
		return "", fmt.Errorf("%s has configuration for multiple backends (%s): set %s explicitly to choose one", component, strings.Join(configured, ", "), selector)
	}
}

// Load reads configuration from the environment. Values that name a Secrets
// Manager secret are resolved; a failed resolution aborts startup instead of
// leaking the ARN into a credential field. Pass a context for secret
// resolution and IAM-role probing.
func Load(ctx context.Context) (*Config, error) {
	// Collect the first environment error so one bad ARN cannot be reported as
	// a confusing "unknown backend" validation failure later on.
	var envErr error
	get := func(names ...string) string {
		if envErr != nil {
			return ""
		}
		value, err := firstEnv(ctx, names...)
		if err != nil {
			envErr = err
		}
		return value
	}
	getDefault := func(fallback string, names ...string) string {
		value := get(names...)
		if envErr != nil || value == "" {
			return fallback
		}
		return value
	}

	listen, err := listenAddr(ctx)
	if err != nil {
		return nil, err
	}
	awsRegion := get("AWS_REGION", "AWS_DEFAULT_REGION")

	restProxy := false
	if raw := strings.TrimSpace(get("JMAP_REST_PROXY")); raw != "" {
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid JMAP_REST_PROXY: %w", err)
		}
		restProxy = parsed
	}

	// Read FILESYSTEM_DIR early to check if it's explicitly set.
	// If explicitly set (even with ~), we expand it.
	// If not set, we only apply the default ~/.emissary and expand it
	// when filesystem-backed stores are actually needed.
	// This avoids requiring $HOME in environments like AWS Lambda where
	// it's not defined, unless filesystem mode is used or explicitly configured.
	explicitFilesystemDir := get("FILESYSTEM_DIR")
	explicitReceiverFilesystemDir := get("RECEIVER_FILESYSTEM_DIR")

	// Read every selector up front: backend resolution needs them all, and
	// TABLE_PREFIX must be checked before its default is applied below.
	storageType := strings.ToLower(strings.TrimSpace(get("STORAGE")))
	senderType := strings.ToLower(strings.TrimSpace(get("SENDER")))
	receiverType := strings.ToLower(strings.TrimSpace(get("RECEIVER")))
	metadataType := strings.ToLower(strings.TrimSpace(get("METADATA")))
	sessionType := strings.ToLower(strings.TrimSpace(get("SESSION")))
	userManagerType := strings.ToLower(strings.TrimSpace(get("USER_MANAGER")))
	tablePrefixEnv := get("TABLE_PREFIX", "DDB_TABLE_PREFIX")
	mtaPortEnv := get("MTA_PORT")

	needsFilesystem := storageType == "filesystem" ||
		userManagerType == "filesystem" ||
		receiverType == "filesystem" ||
		metadataType == "file"

	var filesystemDir string
	if envErr != nil {
		return nil, envErr
	}
	if explicitFilesystemDir != "" {
		// Explicitly set: expand it (handles ~)
		filesystemDir, err = fsutil.ExpandDir(explicitFilesystemDir)
		if err != nil {
			return nil, fmt.Errorf("invalid FILESYSTEM_DIR: %w", err)
		}
	} else if needsFilesystem {
		// Not explicitly set but filesystem mode needed: apply default and expand
		filesystemDir, err = fsutil.ExpandDir(getDefault("~/.emissary", "FILESYSTEM_DIR"))
		if err != nil {
			return nil, fmt.Errorf("invalid FILESYSTEM_DIR: %w", err)
		}
	}
	// If not explicitly set and filesystem not needed, filesystemDir stays empty

	// The receiver filesystem store can point at a dedicated directory. It is
	// expanded independently so "~" works even when the shared FILESYSTEM_DIR
	// is not used by any other backend.
	var receiverFilesystemDir string
	if explicitReceiverFilesystemDir != "" {
		receiverFilesystemDir, err = fsutil.ExpandDir(explicitReceiverFilesystemDir)
		if err != nil {
			return nil, fmt.Errorf("invalid RECEIVER_FILESYSTEM_DIR: %w", err)
		}
	}

	cfg := &Config{
		Server: ServerConfig{
			ListenAddr:         listen,
			Username:           get("USERNAME"),
			Password:           get("PASSWORD"),
			JWTSecret:          get("JWT_SECRET"),
			JWTAudience:        get("JWT_AUDIENCE"),
			JWTIssuer:          get("JWT_ISSUER"),
			JMAPPublicURL:      strings.TrimRight(get("JMAP_PUBLIC_URL"), "/"),
			JMAPEventSourceURL: strings.TrimRight(get("JMAP_EVENTSOURCE_URL"), "/"),
			JMAPRESTProxy:      restProxy,
		},
		Storage: StorageConfig{
			Type:          storageType,
			S3Bucket:      get("S3_BUCKET"),
			FilesystemDir: filesystemDir,
		},
		Sender: SenderConfig{
			Type:    senderType,
			MTAPort: getDefault("25", "MTA_PORT"),
		},
		Receiver: ReceiverConfig{
			Type:          receiverType,
			S3Bucket:      get("RECEIVER_BUCKET"),
			FilesystemDir: receiverFilesystemDir,
			DdbTable:      get("RECEIVER_TABLE"),
			Region:        get("RECEIVER_REGION"),
		},
		Metadata: MetadataConfig{
			Type:     metadataType,
			DdbTable: get("DDB_TABLE"),
			FilePath: get("METADATA_FILE"),
			Region:   get("METADATA_REGION"),
		},
		Session: SessionConfig{
			Type:     sessionType,
			DdbTable: get("SESSION_TABLE"),
			Region:   get("SESSION_REGION"),
		},
		UserManager: UserManagerConfig{
			Type:             strings.ToLower(strings.TrimSpace(get("USER_MANAGER"))),
			Domain:           strings.ToLower(strings.TrimSpace(get("DOMAIN"))),
			DdbTable:         get("USER_TABLE"),
			FilesystemDir:    filesystemDir,
			Region:           get("USER_REGION"),
			LDAPURL:          get("LDAP_URL"),
			LDAPBindDN:       get("LDAP_BIND_DN"),
			LDAPBindPassword: get("LDAP_BIND_PASSWORD"),
			LDAPBaseDN:       get("LDAP_BASE_DN"),
			LDAPUserFilter:   get("LDAP_USER_FILTER"),
			LDAPCacheTTL:     get("LDAP_CACHE_TTL"),
		},
		AWSRegion:      awsRegion,
		FilesystemDir:  filesystemDir,
		DDBTablePrefix: strings.TrimSpace(getDefault("emissary", "TABLE_PREFIX", "DDB_TABLE_PREFIX")),
	}
	if envErr != nil {
		return nil, envErr
	}

	// MAX_FILESIZE is a byte count, not a duration or a secret ARN. Reject a
	// malformed value at startup so a typo cannot silently disable the cap.
	cfg.MaxFileSize = DefaultMaxFileSize
	if raw := strings.TrimSpace(get("MAX_FILESIZE")); raw != "" {
		maxFileSize, parseErr := strconv.ParseInt(raw, 10, 64)
		if parseErr != nil || maxFileSize <= 0 || maxFileSize >= 1<<40 {
			return nil, fmt.Errorf("invalid MAX_FILESIZE %q: want a byte count between 1 and %d", raw, int64(1<<40)-1)
		}
		cfg.MaxFileSize = maxFileSize
	}

	ttl, err := time.ParseDuration(getDefault("24h", "JWT_TTL"))
	if err != nil {
		return nil, fmt.Errorf("invalid JWT_TTL: %w", err)
	}
	if envErr != nil {
		return nil, envErr
	}
	cfg.Server.JWTExpiry = ttl

	if cfg.Server.JWTSecret == "" {
		secret := make([]byte, 32)
		if _, err := rand.Read(secret); err != nil {
			return nil, fmt.Errorf("generate JWT secret: %w", err)
		}
		cfg.Server.JWTSecret = base64.RawURLEncoding.EncodeToString(secret)
		cfg.Server.JWTGenerated = true
	}

	// Only probe the SDK credential chain when a selector is unset and a
	// backend might therefore need the answer; explicit selectors must not add
	// startup latency.
	var hasCreds bool
	if storageType == "" || senderType == "" || receiverType == "" ||
		metadataType == "" || sessionType == "" || userManagerType == "" {
		hasCreds, err = hasAWSCredentials(ctx)
		if err != nil {
			return nil, err
		}
	}

	// Resolve every pluggable backend with the same rule: an explicit selector
	// wins; otherwise exactly one configured backend is required. Missing or
	// ambiguous configuration is an error instead of a silent memory/log
	// fallback. The no-op implementations remain available as explicit choices.
	resolver := backendResolver{hasCreds: hasCreds, tablePrefixSet: strings.TrimSpace(tablePrefixEnv) != ""}

	if cfg.Storage.Type, err = resolver.selectType(
		"storage", "STORAGE", cfg.Storage.Type, "memory",
		"AWS credentials + S3_BUCKET (s3), FILESYSTEM_DIR (filesystem)",
		resolver.s3(cfg.Storage.S3Bucket),
		resolver.filesystem(explicitFilesystemDir),
	); err != nil {
		return nil, err
	}

	if cfg.Sender.Type, err = resolver.selectType(
		"sender", "SENDER", cfg.Sender.Type, "log",
		"AWS credentials (aws-ses), MTA_PORT (mta)",
		resolver.ses(),
		resolver.mta(mtaPortEnv),
	); err != nil {
		return nil, err
	}

	if cfg.Receiver.Type, err = resolver.selectType(
		"receiver", "RECEIVER", cfg.Receiver.Type, "memory",
		"AWS credentials + RECEIVER_BUCKET/S3_BUCKET (s3), RECEIVER_FILESYSTEM_DIR/FILESYSTEM_DIR (filesystem), AWS credentials + RECEIVER_TABLE/TABLE_PREFIX (ddb)",
		resolver.s3(cfg.Receiver.S3Bucket, cfg.Storage.S3Bucket),
		resolver.filesystem(explicitReceiverFilesystemDir, explicitFilesystemDir),
		resolver.ddb(cfg.Receiver.DdbTable),
	); err != nil {
		return nil, err
	}

	if cfg.Metadata.Type, err = resolver.selectType(
		"metadata", "METADATA", cfg.Metadata.Type, "memory",
		"AWS credentials + DDB_TABLE/TABLE_PREFIX (ddb), METADATA_FILE/FILESYSTEM_DIR (file)",
		resolver.ddb(cfg.Metadata.DdbTable),
		resolver.filesystemNamed("file", cfg.Metadata.FilePath, explicitFilesystemDir),
	); err != nil {
		return nil, err
	}

	if cfg.Session.Type, err = resolver.selectType(
		"session", "SESSION", cfg.Session.Type, "memory",
		"AWS credentials + SESSION_TABLE/TABLE_PREFIX (ddb)",
		resolver.ddb(cfg.Session.DdbTable),
	); err != nil {
		return nil, err
	}

	if cfg.UserManager.Type, err = resolver.selectType(
		"user manager", "USER_MANAGER", cfg.UserManager.Type, "none",
		"AWS credentials + S3_BUCKET (s3), FILESYSTEM_DIR (filesystem), AWS credentials + USER_TABLE/TABLE_PREFIX (ddb), LDAP_URL (ldap)",
		resolver.s3(cfg.Storage.S3Bucket),
		resolver.filesystem(explicitFilesystemDir),
		resolver.ddb(cfg.UserManager.DdbTable),
		resolver.ldap(cfg.UserManager.LDAPURL),
	); err != nil {
		return nil, err
	}

	// Component-specific targets default to the shared storage/filesystem
	// settings. RECEIVER_BUCKET and RECEIVER_FILESYSTEM_DIR allow a dedicated
	// rules store, as documented in docs/CONFIGURATION.md.
	if cfg.Receiver.S3Bucket == "" {
		cfg.Receiver.S3Bucket = cfg.Storage.S3Bucket
	}
	if cfg.Receiver.FilesystemDir == "" {
		cfg.Receiver.FilesystemDir = filesystemDir
	}
	if cfg.Metadata.Type == "file" && cfg.Metadata.FilePath == "" {
		// METADATA_FILE still overrides this; by default the file metadata
		// store uses one JSON file per email under FILESYSTEM_DIR/metadata.
		cfg.Metadata.FilePath = cfg.FilesystemMetadataDir()
	}
	if cfg.Metadata.Region == "" {
		cfg.Metadata.Region = cfg.AWSRegion
	}
	if cfg.UserManager.Region == "" {
		cfg.UserManager.Region = cfg.AWSRegion
	}
	if cfg.Receiver.Region == "" {
		cfg.Receiver.Region = cfg.AWSRegion
	}
	if cfg.Session.Region == "" {
		cfg.Session.Region = cfg.AWSRegion
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	// Log only the selected backend types: the configuration also holds
	// credentials and must never be dumped wholesale.
	slog.Debug("Configuration loaded",
		"storage", cfg.Storage.Type,
		"sender", cfg.Sender.Type,
		"receiver", cfg.Receiver.Type,
		"metadata", cfg.Metadata.Type,
		"session", cfg.Session.Type,
		"user_manager", cfg.UserManager.Type,
		"jwt_secret_generated", cfg.Server.JWTGenerated)
	return cfg, nil
}

func listenAddr(ctx context.Context) (string, error) {
	addr, err := firstEnv(ctx, "LISTEN_ADDR")
	if err != nil {
		return "", err
	}
	if addr != "" {
		return addr, nil
	}
	port, err := firstEnvDefault(ctx, "8080", "PORT")
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(port, ":") {
		return port, nil
	}
	return ":" + port, nil
}

func firstEnv(ctx context.Context, names ...string) (string, error) {
	for _, name := range names {
		value, err := envutil.GetCachedEnv(ctx, name)
		if err != nil {
			return "", fmt.Errorf("environment variable %s: %w", name, err)
		}
		if value != "" {
			return value, nil
		}
	}
	return "", nil
}

func firstEnvDefault(ctx context.Context, fallback string, names ...string) (string, error) {
	value, err := firstEnv(ctx, names...)
	if err != nil {
		return "", err
	}
	if value != "" {
		return value, nil
	}
	return fallback, nil
}

// credentialProbe is a variable so tests can simulate an IAM role without
// contacting the AWS credential chain.
var credentialProbe = awsutil.HasCredentials

// hasAWSCredentials reports whether AWS credentials are available. Environment
// variables cover static keys, shared profiles, web identity and ECS task
// roles; when none are present the SDK chain is probed so EC2 instance profiles
// and other IAM roles are recognised as configuration instead of leaving an
// AWS-backed component looking unconfigured.
func hasAWSCredentials(ctx context.Context) (bool, error) {
	accessKey, err := firstEnv(ctx, "AWS_ACCESS_KEY_ID")
	if err != nil {
		return false, err
	}
	secretKey, err := firstEnv(ctx, "AWS_SECRET_ACCESS_KEY")
	if err != nil {
		return false, err
	}
	if accessKey != "" && secretKey != "" {
		return true, nil
	}
	profile, err := firstEnv(ctx, "AWS_PROFILE", "AWS_DEFAULT_PROFILE")
	if err != nil {
		return false, err
	}
	if profile != "" {
		return true, nil
	}
	tokenFile, err := firstEnv(ctx, "AWS_WEB_IDENTITY_TOKEN_FILE")
	if err != nil {
		return false, err
	}
	roleARN, err := firstEnv(ctx, "AWS_ROLE_ARN")
	if err != nil {
		return false, err
	}
	if tokenFile != "" && roleARN != "" {
		return true, nil
	}
	containerURI, err := firstEnv(ctx, "AWS_CONTAINER_CREDENTIALS_RELATIVE_URI", "AWS_CONTAINER_CREDENTIALS_FULL_URI")
	if err != nil {
		return false, err
	}
	if containerURI != "" {
		return true, nil
	}
	return credentialProbe(ctx), nil
}
