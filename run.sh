#!/bin/bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$ROOT_DIR"

usage() {
	cat <<EOF
Usage: $0 <command>

Commands:
  build         Build the frontend, compile the Go packages and build the Lambda binary
  build:lambda  Build only the Lambda binary (bootstrap), for linux/amd64
  test          Run the test suite (Go and TypeScript)
  run           Build and run the frontend server
  deploy        Deploy all stacks (id, in, client)
  deploy:id     Deploy the email identity stack (SES verification, DKIM, MX, SPF, DMARC)
  deploy:in     Deploy the email identity and inbound stacks (id + in)
  deploy:client Deploy the application stack (Lambda, API Gateway, DynamoDB)
  deploy:init   Initialize the deploy directory (run npm ci)
  deploy:list   List CDK stacks (runs deploy:init then cdk ls)
EOF
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

		go build -o ../.build/frontend ./cmd/frontend
		go build -o ../.build/sns-receiver ./cmd/sns-receiver
	)
}

# Build the frontend application as the Lambda custom-runtime binary. The CDK
# app deploys mailer/cmd/frontend/bootstrap (see emailClientStack in
# deploy/emissary.ts), so the binary must live next to the command package.
build_lambda() {
	(
		cd mailer/cmd/frontend
		CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o bootstrap .
	)
}

build() {
	build_frontend
	build_backend
	build_lambda
}

test() {
	# The //go:embed directives require the built frontend to be present
	build_frontend

	echo "=== Running Go tests ==="
	(
		cd mailer
		go test -buildvcs=false -v ./...
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

# Deploy all stacks
deploy_all() {
	deploy_init
	(
		cd deploy
		npm run cdk -- deploy --all
	)
}

# Deploy the email identity stack
deploy_id() {
	deploy_init
	(
		cd deploy
		npm run cdk -- deploy emissary-id-*
	)
}

# Deploy the email identity and inbound stacks
deploy_in() {
	deploy_init
	(
		cd deploy
		npm run cdk -- deploy emissary-id-* emissary-in-*
	)
}

# Deploy the application stack
deploy_client() {
	deploy_init
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

	if [[ -z "${S3_BUCKET:-}" ]]; then
		echo "Error: S3_BUCKET environment variable must be set" >&2
		return 1
	fi

	export USERNAME="$USERNAME"
	export PASSWORD="$PASSWORD"
	export JWT_SECRET="$JWT_SECRET"
	export S3_BUCKET="$S3_BUCKET"
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
	echo "  S3 Bucket: $S3_BUCKET"
	echo "  Metadata Type: $METADATA"
	echo "  Port: $PORT"
	echo ""

	# Run the frontend binary, passing through any additional arguments
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