#!/bin/bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$ROOT_DIR"

# Get the git commit hash for ldflags
get_commit_hash() {
	git rev-parse HEAD 2>/dev/null || echo "unknown"
}

# Get the build time for ldflags (ISO 8601 format)
get_build_time() {
	date -u +"%Y-%m-%dT%H:%M:%SZ" 2>/dev/null || echo "unknown"
}

COMMIT_HASH=$(get_commit_hash)
BUILD_TIME=$(get_build_time)
LDFLAGS="-X github.com/kncept/Emissary/mailer/envutil.CommitHash=${COMMIT_HASH} -X github.com/kncept/Emissary/mailer/envutil.BuildTime=${BUILD_TIME}"

usage() {
	cat <<EOF
Usage: $0 <command>

Commands:
  build            Build the frontend, compile the Go packages and build the Lambda binary
  build:lambda     Build only the Lambda binary (bootstrap), for linux/amd64
  generate:api     Regenerate Go and TypeScript API code from the OpenAPI specs
  check:generated  Fail if committed generated API code is stale
  test             Run the test suite (Go and TypeScript)
  run              Build and run the frontend server
  deploy           Build app assets and deploy all stacks (id, in, client)
  deploy:id        Deploy the email identity stack (SES verification, DKIM, MX, SPF, DMARC)
  deploy:in        Deploy the email identity and inbound stacks (id + in)
  deploy:client    Build app assets and deploy the application stack (Lambda, API Gateway, DynamoDB)
  deploy:init      Initialize the deploy directory (run npm ci)
  deploy:list      List CDK stacks (runs deploy:init then cdk ls)
EOF
}

# Generate Go types and stubs from the OpenAPI specs using ogen. All specs
# are always generated together so the generated trees stay in sync.
generate_api() {
	(
		cd mailer
		# Ensure ogen is available
		go tool ogen -target ../api/jmap/generated ../api/jmap.yaml
		go tool ogen -target ../api/auth/generated ../api/auth.yaml
		go tool ogen -target ../api/support/generated ../api/support.yaml
		go tool ogen -target ../api/users/generated ../api/users.yaml
	)

	# Generate TypeScript API client for the frontend. Install the declared
	# dev dependencies first so the local `orval` binary is available; we
	# never use npx (see "No npx" in docs/DEVELOPMENT.md).
	(
		cd frontend
		npm ci
		npm run orval
	)
}

# Generated trees that are committed to git, so a fresh generation must not
# leave any changes in them.
GENERATED_PATHS=(
	api/jmap/generated
	api/auth/generated
	api/support/generated
	api/users/generated
	frontend/src/api/generated
)

# Regenerate the API code and fail if the committed generated trees differ.
# This is the entry point for the generated-code-freshness CI check.
check_generated() {
	generate_api

	local changes
	changes=$(git status --porcelain -- "${GENERATED_PATHS[@]}")
	if [[ -n "$changes" ]]; then
		echo "Generated code is stale. Run './run.sh generate:api' and commit the result:" >&2
		echo "$changes" >&2
		return 1
	fi

	echo "Generated code is up to date."
}

# Build the frontend and place the output where the //go:embed
# directive expects it. Vite builds to mailer/gui/frontend/dist (see
# frontend/vite.config.ts); the mailer/gui package embeds it from there.
build_frontend() {
	(
		cd frontend
		npm ci
		npm run build
	)
}

build_backend() {
	(
		mkdir -p .build
		cd mailer

		go build -ldflags="${LDFLAGS}" -o ../.build/frontend ./cmd/frontend
		go build -ldflags="${LDFLAGS}" -o ../.build/s3-receiver ./cmd/s3-receiver
	)
}

# Build the frontend application as the Lambda custom-runtime binary. The CDK
# app deploys mailer/cmd/frontend/bootstrap (see emailClientStack in
# deploy/emissary.ts), so the binary must live next to the command package.
build_lambda() {
	(
		cd mailer/cmd/frontend
		CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="${LDFLAGS}" -o bootstrap .
	)
	(
		cd mailer/cmd/s3-receiver
		CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="${LDFLAGS}" -o bootstrap .
	)
}

build() {
	generate_api
	build_frontend
	build_backend
	build_lambda
}

test() {
	# The //go:embed directives require the built frontend to be present
	generate_api
	build_frontend

	# The spec tests enforce the path-ownership rule between auth.yaml
	# and jmap.yaml (see "Specs and path ownership" in docs/API.md).
	echo "=== Running API spec tests ==="
	(
		cd api
		go test -buildvcs=false ./...
	)

	echo ""
	echo "=== Running Go tests ==="
	(
		cd mailer
		go test -buildvcs=false -v ./...
	)

	echo ""
	echo "=== Linting frontend ==="
	(
		cd frontend
		npm run lint
	)

	echo ""
	echo "=== Running TypeScript tests ==="
	(
		cd frontend
		npm run test
	)
}

# Generate a GUID (UUID v4)
generate_guid() {
	if command -v uuidgen >/dev/null 2>&1; then
		uuidgen | tr '[:upper:]' '[:lower:]'
	elif [[ -r /proc/sys/kernel/random/uuid ]]; then
		cat /proc/sys/kernel/random/uuid
	else
		# Fallback: generate using random bytes
		od -x /dev/urandom | head -1 | awk '{OFS="-"; print $2$3, $4, $5, $6, $7$8$9}'
	fi
}

# Print the non-private environment variables that affect deployment
deploy_show_env() {
	echo "=== Emissary Deployment Environment ==="
	echo "DOMAIN: ${DOMAIN:-<not set>}"
	echo "AWS_REGION: ${AWS_REGION:-<not set>}"
	echo "SUBDOMAINS: ${SUBDOMAINS:-<not set (defaults to DOMAIN only)>}"
	echo "S3_BUCKET: ${S3_BUCKET:-<not set (will create new bucket)>}"
	echo "TABLE_PREFIX: ${TABLE_PREFIX:-${DDB_TABLE_PREFIX:-<not set (defaults to emissary-)>}}"
	echo "SESSION: ${SESSION:-<not set (defaults to none)>}"
	echo "SESSION_TABLE: ${SESSION_TABLE:-<not set>}"
	echo "USER_MANAGER: ${USER_MANAGER:-<not set (defaults to s3 when STORAGE=s3, else none)>}"
	echo "USER_TABLE: ${USER_TABLE:-<not set>}"
	echo "USERNAME: ${USERNAME:-<not set (no admin user)>}"
	if [[ -n "${PASSWORD+x}" ]]; then
		echo "PASSWORD: (defined)"
	else
		echo "PASSWORD: (not defined)"
	fi
	echo "JWT_SECRET: $(if [[ -n "${JWT_SECRET:-}" ]]; then echo "(defined)"; else echo "<not set (will generate)>"; fi)"
	echo "========================================="
}

# Deploy helpers
deploy_init() {
	(
		cd deploy
		npm install
	)
}

deploy_list() {
	deploy_init
	(
		cd deploy
		npm run cdk -- ls
	)
}

# Build the binaries used by the client stack's Lambda assets.
build_deploy_assets() {
	build_frontend
	build_lambda
}

# Deploy all stacks
deploy_all() {
	deploy_show_env
	deploy_init
	build_deploy_assets
	(
		cd deploy
		npm run cdk -- deploy --all
	)
}

# Deploy the email identity stack
deploy_id() {
	deploy_show_env
	deploy_init
	(
		cd deploy
		npm run cdk -- deploy emissary-id-*
	)
}

# Deploy the email identity and inbound stacks
deploy_in() {
	deploy_show_env
	deploy_init
	(
		cd deploy
		npm run cdk -- deploy emissary-id-* emissary-in-*
	)
}

# Deploy the application stack
deploy_client() {
	deploy_show_env
	deploy_init
	build_deploy_assets
	(
		cd deploy
		npm run cdk -- deploy emissary-client-*
	)
}

run() {
	# Build first
	build

	# Generate credentials only when the variable is unset. An explicitly
	# blank PASSWORD disables the administrator login.
	USERNAME="${USERNAME:-Emissary}"
	if [[ -z "${PASSWORD+x}" ]]; then
		PASSWORD=$(generate_guid)
	else
		PASSWORD="$PASSWORD"
	fi
	if [[ -z "${JWT_SECRET:-}" ]]; then
		JWT_SECRET=$(generate_guid)
	fi

	# S3 is only needed when it is the selected mail store; the memory and
	# filesystem backends run entirely locally.
	if [[ -z "${S3_BUCKET:-}" && "${STORAGE:-s3}" == "s3" ]]; then
		echo "Error: S3_BUCKET environment variable must be set when STORAGE=s3" >&2
		return 1
	fi

	export USERNAME="$USERNAME"
	export PASSWORD="$PASSWORD"
	export JWT_SECRET="$JWT_SECRET"
	if [[ -n "${S3_BUCKET:-}" ]]; then
		export S3_BUCKET="$S3_BUCKET"
	fi
	export METADATA="${METADATA:-memory}"
	export PORT="${PORT:-8080}"

	echo "Starting Emissary frontend..."
	echo "  Username: $USERNAME"
	if [[ -z "$PASSWORD" ]]; then
		echo "  Password: (admin login disabled)"
	else
		echo "  Password: $PASSWORD"
	fi
	echo "  JWT Secret: $JWT_SECRET"
	echo "  Storage Type: ${STORAGE:-s3}"
	if [[ -n "${S3_BUCKET:-}" ]]; then
		echo "  S3 Bucket: $S3_BUCKET"
	fi
	if [[ "${STORAGE:-}" == "filesystem" ]]; then
		echo "  Filesystem Dir: ${FILESYSTEM_DIR:-~/.emissary}"
	fi
	echo "  Metadata Type: $METADATA"
	echo "  Port: $PORT"
	echo ""

	# Run the frontend binary, passing through any additional arguments
	echo "+ .build/frontend $*"
	exec .build/frontend "$@"
}

main() {
	local command="${1:-}"

	case "$command" in
	build)
		build
		;;
	build:lambda)
		build_lambda
		;;
	generate:api)
		generate_api
		;;
	check:generated)
		check_generated
		;;
	test)
		test
		;;
	run)
		run "${@:2}"
		;;
	deploy)
		deploy_all
		;;
	deploy:id)
		deploy_id
		;;
	deploy:in)
		deploy_in
		;;
	deploy:client)
		deploy_client
		;;
	deploy:init)
		deploy_init
		;;
	deploy:list)
		deploy_list
		;;
	"" | -h | --help | help)
		usage
		;;
	*)
		printf 'Unknown command: %s\n\n' "$command" >&2
		usage >&2
		return 1
		;;
	esac
}

main "$@"