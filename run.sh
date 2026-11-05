#!/bin/bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$ROOT_DIR"

usage() {
	cat <<EOF
Usage: $0 <command>

Commands:
  build    Build the frontend and compile the Go packages
  test     Run the test suite (Go and TypeScript)
  run      Build and run the frontend server
EOF
}

build() {
	(
		cd frontend
		npm ci
		npm run build
	)

	(
		mkdir -p .build
		cd mailer

		go build -o ../.build/frontend ./cmd/frontend

		go build -o ../.build/sns-receiver ./cmd/sns-receiver
	)
}

test() {
	echo "=== Running Go tests ==="
	(
		cd mailer
		go test -buildvcs=false -v ./...
	)

	echo ""
	echo "=== Running TypeScript tests ==="
	(
		cd frontend
		npm ci >/dev/null 2>&1
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

run() {
	# Build first
	build

	# Generate credentials
	USERNAME="Emissary"
	PASSWORD=$(generate_guid)
	SESSION_SECRET=$(generate_guid)

	export AUTH_USERNAME="$USERNAME"
	export AUTH_PASSWORD="$PASSWORD"
	export SESSION_SECRET="$SESSION_SECRET"
	export S3_BUCKET="${S3_BUCKET:-emissary-emails}"
	export S3_PREFIX="${S3_PREFIX:-emails}"
	export S3_REGION="${S3_REGION:-us-east-1}"
	export METADATA_TYPE="${METADATA_TYPE:-memory}"
	export PORT="${PORT:-8080}"

	echo "Starting Emissary frontend..."
	echo "  Username: $USERNAME"
	echo "  Password: $PASSWORD"
	echo "  Session Secret: $SESSION_SECRET"
	echo "  S3 Bucket: $S3_BUCKET"
	echo "  S3 Prefix: $S3_PREFIX"
	echo "  S3 Region: $S3_REGION"
	echo "  Metadata Type: $METADATA_TYPE"
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
	test)
		test
		;;
	run)
		run "${@:2}"
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