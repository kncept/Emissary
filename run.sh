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

# Build the frontend and place the output where the //go:embed
# directives expect it. Vite builds to mailer/gui/frontend/dist (see
# frontend/vite.config.ts); go:embed does not follow symlinks, so the
# output is copied into the package directories (gitignored)
build_frontend() {
	(
		cd frontend
		npm ci
		npm run build
	)

	# The running server (cmd/frontend) embeds the assets from its own package
	# directory, so sync the Vite output there. The gui package already
	# receives it directly at its embed path.
	rm -rf mailer/cmd/frontend/frontend/dist
	mkdir -p mailer/cmd/frontend/frontend
	cp -R mailer/gui/frontend/dist mailer/cmd/frontend/frontend/dist
}

build_backend() {
	(
		mkdir -p .build
		cd mailer

		go build -o ../.build/frontend ./cmd/frontend
		go build -o ../.build/sns-receiver ./cmd/sns-receiver
	)
}

build() {
	build_frontend
	build_backend
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

run() {
	# Build first
	build

	# Generate credentials
	USERNAME="Emissary"
	PASSWORD=$(generate_guid)
	JWT_SECRET=$(generate_guid)

	export AUTH_USERNAME="$USERNAME"
	export AUTH_PASSWORD="$PASSWORD"
	export JWT_SECRET="$JWT_SECRET"
	export S3_BUCKET="${S3_BUCKET:-emissary-emails}"
	export S3_PREFIX="${S3_PREFIX:-emails}"
	export S3_REGION="${S3_REGION:-us-east-1}"
	export METADATA_TYPE="${METADATA_TYPE:-memory}"
	export PORT="${PORT:-8080}"

	echo "Starting Emissary frontend..."
	echo "  Username: $USERNAME"
	echo "  Password: $PASSWORD"
	echo "  JWT Secret: $JWT_SECRET"
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