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
EOF
}

build() {
	(
		cd frontend
		npm ci
		find ../mailer/gui -mindepth 1 ! -name '*.go' -exec rm -rf {} +
		npm run build -- --outDir ../mailer/gui
	)

	(
		cd mailer
		go build -buildvcs=false ./...
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

main() {
	local command="${1:-}"

	case "$command" in
	build)
		build
		;;
	test)
		test
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
