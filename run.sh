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

# Get the version for ldflags (from git tag or "dev")
get_version() {
	git describe --tags --exact-match 2>/dev/null | sed 's/^v//' || echo "dev"
}

COMMIT_HASH=$(get_commit_hash)
BUILD_TIME=$(get_build_time)
VERSION=$(get_version)
LDFLAGS="-X github.com/kncept/Emissary/mailer/envutil.CommitHash=${COMMIT_HASH} -X github.com/kncept/Emissary/mailer/envutil.BuildTime=${BUILD_TIME} -X github.com/kncept/Emissary/mailer/envutil.Version=${VERSION}"

usage() {
	cat <<EOF
Usage: $0 <command>

Commands:
  build            Build the frontend, compile the Go packages and build the Lambda binary
  build:lambda     Build only the Lambda binary (bootstrap), for linux/amd64
  generate:api     Regenerate Go and TypeScript API code from the OpenAPI specs
  check:generated  Fail if committed generated API code is stale
  test             Run the test suite (Go and TypeScript)
  spectest         Run the external JMAP conformance suite against a local server
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

# External JMAP conformance suite. The suite is pinned to a commit, patched
# with this repository's advertised-profile adaptations, and run against a
# throwaway local server. All credentials are supplied through the environment
# or generated per run; none are embedded here.
SPECTEST_SUITE_URL="https://github.com/jmapio/jmap-test-suite.git"
SPECTEST_SUITE_COMMIT="0f2c117dc5f3621bfa50cb0b8b4d4ac87a4154be"

spectest_login() {
	local username="$1"
	local password="$2"
	local base_url="$3"
	local body
	body=$(python3 -c 'import json,sys; print(json.dumps({"username":sys.argv[1],"password":sys.argv[2]}))' "$username" "$password")
	curl -fsS -X POST "$base_url/api/auth/login" \
		-H 'Content-Type: application/json' --data-binary "$body" |
		python3 -c 'import json,sys; print(json.load(sys.stdin)["accessToken"])'
}

spectest_create_user() {
	local email="$1"
	local password="$2"
	local manager="$3"
	local admin="$4"
	local base_url="$5"
	local body
	body=$(python3 -c 'import json,sys; print(json.dumps({"email":sys.argv[1],"password":sys.argv[2],"enabled":True,"userManager":sys.argv[3]=="true","systemManager":sys.argv[4]=="true"}))' \
		"$email" "$password" "$manager" "$manager")
	curl -fsS -X POST "$base_url/api/user/" \
		-H "Authorization: Bearer $admin" -H 'Content-Type: application/json' \
		--data-binary "$body" >/dev/null
}

spectest() {
	(
		set -euo pipefail

		local work_root="${SPECTEST_WORK_DIR:-${TMPDIR:-/tmp}}"
		work_root="${work_root%/}/emissary-spectest"
		local suite_dir="$work_root/jmap-test-suite"
		local users_dir="$work_root/users"
		local config_file="$work_root/config.json"
		local report_file="${SPECTEST_REPORT:-$work_root/report.json}"
		local patch_file="$ROOT_DIR/scripts/spectest/jmap-test-suite.patch"
		local port="${SPECTEST_PORT:-8099}"
		local base_url="http://127.0.0.1:$port"
		local server_pid=""

		cleanup() {
			if [[ -n "$server_pid" ]]; then
				kill "$server_pid" 2>/dev/null || true
				wait "$server_pid" 2>/dev/null || true
			fi
			rm -rf "$users_dir"
		}
		trap cleanup EXIT

		if ! command -v python3 >/dev/null 2>&1 || ! command -v curl >/dev/null 2>&1; then
			echo "spectest requires python3 and curl" >&2
			return 1
		fi
		if [[ ! -f "$patch_file" ]]; then
			echo "Missing suite profile patch: $patch_file" >&2
			return 1
		fi

		local username="${SPECTEST_USERNAME:-spectest-$(generate_guid)@example.com}"
		local password="${SPECTEST_PASSWORD:-$(generate_guid)}"
		local jwt_secret="${SPECTEST_JWT_SECRET:-$(generate_guid)}"
		local secondary_username="${SPECTEST_SECONDARY_USERNAME:-spectest-secondary-$(generate_guid)@example.com}"
		local secondary_password="${SPECTEST_SECONDARY_PASSWORD:-$(generate_guid)}"

		echo "=== Building local server ==="
		build_frontend
		build_backend

		echo ""
		echo "=== Preparing pinned conformance suite ==="
		mkdir -p "$work_root" "$users_dir"
		if [[ ! -d "$suite_dir/.git" ]]; then
			mkdir -p "$suite_dir"
			git -C "$suite_dir" init -q
			git -C "$suite_dir" remote add origin "$SPECTEST_SUITE_URL"
		fi
		if ! git -C "$suite_dir" cat-file -e "$SPECTEST_SUITE_COMMIT^{commit}" 2>/dev/null; then
			git -C "$suite_dir" fetch -q --depth 1 origin "$SPECTEST_SUITE_COMMIT"
		fi
		git -C "$suite_dir" checkout -q "$SPECTEST_SUITE_COMMIT"
		git -C "$suite_dir" checkout -q -- src
		git -C "$suite_dir" apply "$patch_file"
		# The pinned suite does not commit a lockfile, so a plain install is
		# the documented setup (see its README).
		npm install --prefix "$suite_dir"
		npm run build --prefix "$suite_dir"

		echo ""
		echo "=== Starting local server on port $port ==="
		env USERNAME="$username" PASSWORD="$password" JWT_SECRET="$jwt_secret" \
			STORAGE=memory SENDER=log RECEIVER=memory METADATA=memory SESSION=memory \
			USER_MANAGER=filesystem FILESYSTEM_DIR="$users_dir" \
			PORT="$port" LOG_LEVEL="${SPECTEST_LOG_LEVEL:-ERROR}" \
			MAX_FILESIZE=10485760 \
			nohup .build/frontend >"$work_root/server.log" 2>&1 &
		server_pid=$!

		local ready=0
		for _ in {1..90}; do
			local status
			status=$(curl -s -o /dev/null -w '%{http_code}' "$base_url/.well-known/jmap" 2>/dev/null || true)
			if [[ "$status" =~ ^[0-9]{3}$ && "$status" != "000" ]]; then
				ready=1
				break
			fi
			if ! kill -0 "$server_pid" 2>/dev/null; then
				break
			fi
			sleep 0.25
		done
		if [[ "$ready" -ne 1 ]]; then
			echo "Local server did not become ready; see $work_root/server.log" >&2
			return 1
		fi

		# The bootstrap administrator can create the two mail users used by
		# the suite; creating the primary as a real user keeps singleton
		# methods (VacationResponse) working with the filesystem manager.
		local admin_token primary_token secondary_token
		admin_token=$(spectest_login "$username" "$password" "$base_url")
		spectest_create_user "$username" "$password" true "$admin_token" "$base_url"
		spectest_create_user "$secondary_username" "$secondary_password" false "$admin_token" "$base_url"
		primary_token=$(spectest_login "$username" "$password" "$base_url")
		secondary_token=$(spectest_login "$secondary_username" "$secondary_password" "$base_url")

		python3 - "$config_file" "$base_url" "$username" "$primary_token" \
			"$secondary_username" "$secondary_token" <<'PY'
import json, sys
path, base, username, token, secondary_username, secondary_token = sys.argv[1:]
config = {
    "sessionUrl": base + "/.well-known/jmap",
    "serverInfo": "Emissary local conformance",
    "users": {
        "primary": {"username": username, "password": token},
        "secondary": {"username": secondary_username, "password": secondary_token},
    },
    "authMethod": "bearer",
    "timeout": 30000,
    "verbose": False,
}
with open(path, "w") as out:
    json.dump(config, out, indent=2)
PY

		echo ""
		echo "=== Running conformance suite ==="
		node "$suite_dir/dist/cli.js" -c "$config_file" -f -o "$report_file" --fail-only

		echo ""
		python3 - "$report_file" <<'PY'
import json, sys
summary = json.load(open(sys.argv[1]))["summary"]
print(
    f"JMAP conformance: {summary['passed']} passed, {summary['failed']} failed, "
    f"{summary['skipped']} skipped "
    f"(required: {summary['requiredPassed']} passed, {summary['requiredFailed']} failed)"
)
PY
		echo "Report: $report_file"
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
	echo "TABLE_PREFIX: ${TABLE_PREFIX:-${DDB_TABLE_PREFIX:-<not set (required for DDB backends)>}}"
	echo "SESSION: ${SESSION:-<not set (CDK passes memory unless overridden)>}"
	echo "USER_MANAGER: ${USER_MANAGER:-<not set (CDK passes s3 unless overridden)>}"
	echo "USERNAME: ${USERNAME:-<not set (no admin user)>}"
	if [[ -n "${PASSWORD+x}" ]]; then
		echo "PASSWORD: (defined)"
	else
		echo "PASSWORD: (not defined)"
	fi
	echo "JWT_SECRET: $(if [[ -n "${JWT_SECRET:-}" ]]; then echo "(defined)"; else echo "<not set (will generate)>"; fi)"
	echo "MAX_FILESIZE: ${MAX_FILESIZE:-<not set (defaults to 10485760 bytes)>}"
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

	# config.Load never guesses a backend: it uses an explicit selector or the
	# one backend with configuration, and errors otherwise. This local runner
	# opts into the no-op/local implementations when nothing is configured, so
	# `STORAGE=memory ./run.sh run` remains a complete AWS-free run. Anything
	# the user selected explicitly (or configured through a companion variable)
	# is left for config.Load to resolve.
	if [[ -z "${STORAGE:-}" ]] && [[ -z "${S3_BUCKET:-}" ]] && [[ -z "${FILESYSTEM_DIR:-}" ]]; then
		export STORAGE=memory
	fi
	if [[ -z "${SENDER:-}" ]] && [[ -z "${MTA_PORT:-}" ]] \
		&& [[ -z "${AWS_ACCESS_KEY_ID:-}" ]] && [[ -z "${AWS_PROFILE:-}" ]] \
		&& [[ -z "${AWS_WEB_IDENTITY_TOKEN_FILE:-}" ]] \
		&& [[ -z "${AWS_CONTAINER_CREDENTIALS_RELATIVE_URI:-}" ]] \
		&& [[ -z "${AWS_CONTAINER_CREDENTIALS_FULL_URI:-}" ]]; then
		export SENDER=log
	fi
	if [[ -z "${RECEIVER:-}" ]] && [[ -z "${RECEIVER_BUCKET:-}" ]] \
		&& [[ -z "${RECEIVER_FILESYSTEM_DIR:-}" ]] \
		&& [[ -z "${S3_BUCKET:-}" ]] && [[ -z "${FILESYSTEM_DIR:-}" ]] \
		&& [[ -z "${TABLE_PREFIX:-}" ]] && [[ -z "${DDB_TABLE_PREFIX:-}" ]]; then
		export RECEIVER=memory
	fi
	if [[ -z "${METADATA:-}" ]] && [[ -z "${METADATA_FILE:-}" ]] \
		&& [[ -z "${FILESYSTEM_DIR:-}" ]] \
		&& [[ -z "${TABLE_PREFIX:-}" ]] && [[ -z "${DDB_TABLE_PREFIX:-}" ]]; then
		export METADATA=memory
	fi
	if [[ -z "${SESSION:-}" ]] \
		&& [[ -z "${TABLE_PREFIX:-}" ]] && [[ -z "${DDB_TABLE_PREFIX:-}" ]]; then
		export SESSION=memory
	fi
	if [[ -z "${USER_MANAGER:-}" ]] && [[ -z "${S3_BUCKET:-}" ]] \
		&& [[ -z "${FILESYSTEM_DIR:-}" ]] \
		&& [[ -z "${LDAP_URL:-}" ]] && [[ -z "${TABLE_PREFIX:-}" ]] \
		&& [[ -z "${DDB_TABLE_PREFIX:-}" ]]; then
		export USER_MANAGER=none
	fi

	# S3 is only needed when it is the selected mail store; the memory and
	# filesystem backends run entirely locally.
	if [[ -z "${S3_BUCKET:-}" && "${STORAGE:-}" == "s3" ]]; then
		echo "Error: S3_BUCKET environment variable must be set when STORAGE=s3" >&2
		return 1
	fi

	export USERNAME="$USERNAME"
	export PASSWORD="$PASSWORD"
	export JWT_SECRET="$JWT_SECRET"
	if [[ -n "${S3_BUCKET:-}" ]]; then
		export S3_BUCKET="$S3_BUCKET"
	fi
	export PORT="${PORT:-8080}"
	# Local runs are verbose by default; deployment sets ERROR explicitly.
	export LOG_LEVEL="${LOG_LEVEL:-DEBUG}"
	export MAX_FILESIZE="${MAX_FILESIZE:-10485760}"

	echo "Starting Emissary frontend..."
	echo "  Username: $USERNAME"
	if [[ -z "$PASSWORD" ]]; then
		echo "  Password: (admin login disabled)"
	else
		echo "  Password: $PASSWORD"
	fi
	echo "  JWT Secret: $JWT_SECRET"
	echo "  Storage: ${STORAGE:-auto (inferred from configuration)}"
	echo "  Sender: ${SENDER:-auto (inferred from configuration)}"
	echo "  Receiver: ${RECEIVER:-auto (inferred from configuration)}"
	echo "  Metadata: ${METADATA:-auto (inferred from configuration)}"
	echo "  Session: ${SESSION:-auto (inferred from configuration)}"
	echo "  User Manager: ${USER_MANAGER:-auto (inferred from configuration)}"
	if [[ -n "${S3_BUCKET:-}" ]]; then
		echo "  S3 Bucket: $S3_BUCKET"
	fi
	if [[ "${STORAGE:-}" == "filesystem" ]]; then
		echo "  Filesystem Dir: ${FILESYSTEM_DIR:-~/.emissary}"
	fi
	echo "  Port: $PORT"
	echo "  Log Level: $LOG_LEVEL"
	echo "  Max File Size: $MAX_FILESIZE bytes"
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
	spectest)
		spectest
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