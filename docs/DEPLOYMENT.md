# Deployment

Infrastructure is defined in [`deploy/`](../deploy/) as an AWS CDK v2 (TypeScript) app and driven through [`run.sh`](../run.sh). Everything lives
in a single AWS region (`AWS_REGION`); both the SES inbound endpoint and the receipt rules are region-specific. Application-side variables and
the values injected by the client stack are described in [CONFIGURATION.md](CONFIGURATION.md#deployed-values-set-by-the-cdk-client-stack) and
[API.md](API.md#deployment-constraints).

## Prerequisites

- A **Route 53 public hosted zone** for `DOMAIN` (or a parent of it). The app scans the account's hosted zones and picks the longest name that
  `DOMAIN` ends with.
- AWS credentials with permission to create the resources below.
- Node.js/npm (for CDK) and, to deploy the application stack, the Lambda binary `mailer/cmd/frontend/bootstrap` (build it with `./run.sh build`
  or `./run.sh build:lambda`; see [DEVELOPMENT.md](DEVELOPMENT.md#build)).

## Commands

| Command | Effect |
| --- | --- |
| `./run.sh deploy` | Build the frontend and Lambda binaries, then deploy **all** stacks (`cdk deploy --all`). |
| `./run.sh deploy:id` | Deploy only the identity stack. |
| `./run.sh deploy:in` | Deploy the identity + inbound stacks. |
| `./run.sh deploy:client` | Build the frontend and Lambda binaries, then deploy only the application stack. |
| `./run.sh deploy:init` | Install the CDK dependencies (`npm install` in `deploy/`). |
| `./run.sh deploy:list` | Install dependencies and list the stacks (`cdk ls`). |

`./run.sh run` only builds and starts the local frontend; it does not deploy anything to AWS (see [DEVELOPMENT.md](DEVELOPMENT.md#run-locally)).
To update a deployed Lambda, use `./run.sh deploy:client` (or `./run.sh deploy`), which rebuilds both `bootstrap` binaries before CDK deploy.

Stacks are named with the domain, dots replaced by dashes — for `example.com`: `emissary-id-example-com`, `emissary-in-example-com`,
`emissary-client-example-com`. The per-stack targets deploy by glob, so `deploy:id` selects `emissary-id-*`, `deploy:in` selects `emissary-id-*
emissary-in-*`, and `deploy:client` selects `emissary-client-*`.

## Deploy-time variables

These are read by the CDK app at synthesis/deploy time (they configure AWS, not the application directly). Application variables set by the
client stack are summarized in [CONFIGURATION.md](CONFIGURATION.md#deployed-values-set-by-the-cdk-client-stack); the table below is the
deploy-time source of truth.

| Variable | Required | Description |
| --- | --- | --- |
| `DOMAIN` | yes | The receiving domain, e.g. `example.com`. |
| `AWS_REGION` | yes | Region for SES, S3, DynamoDB, API Gateway. |
| `TABLE_PREFIX` | only when a DDB backend is enabled | Prefix for DynamoDB table names; normalized with a trailing `-` unless it already ends in `-` or `_`. There is no default: selecting `METADATA`/`SESSION`/`USER_MANAGER`/`RECEIVER=ddb` without it fails synthesis and startup. `DDB_TABLE_PREFIX` is accepted as an alias. |
| `SUBDOMAINS` | no | Recipients accepted by the receipt rule: unset → only `DOMAIN`; `*` → `DOMAIN` plus every subdomain; a comma list → `DOMAIN` plus each entry (entries without a dot become `<entry>.<DOMAIN>`). |
| `S3_BUCKET` | no | Use this existing bucket for inbound mail instead of the created one. |
| `USERNAME` | no | Administrator username baked into the Lambda env. |
| `PASSWORD` | no | Administrator password baked into the Lambda env (a warning is printed when unset). |
| `USER_MANAGER` | no | User-manager backend for the deployed app. Unset → CDK passes `s3` (storage is always S3 in a deploy); users then live as `users/<email>/user.json` objects in the mail bucket. When set to `ddb`, a users table is created. |
| `SESSION` | no | Session backend for the deployed app. Unset → CDK passes `memory`. When set to `ddb`, a sessions table is created. |
| `RECEIVER` | no | Receiver config backend. Unset → CDK passes `s3` (routing files in the mail bucket). When set to `ddb`/`dynamodb`, a receiver table is created for the inbound routing rules. |
| `RECEIVER_BUCKET` | no | S3 bucket for receiver rules when `RECEIVER=s3` (default: the mail bucket). IAM access is granted on both buckets. |
| `JWT_SECRET` | no | Signing secret for the app. Unset → CDK generates a 64-character secret in Secrets Manager. |
| `LOG_LEVEL` | no | Minimum `log/slog` level for both application Lambdas (`DEBUG`, `INFO`, `WARN` or `ERROR`, or the equivalent numeric value). Always injected; `ERROR` is used when unset in the current environment. Unrecognised values fall back to `WARN`. |

## The three stacks

### `emissary-id-<domain>` — email identity

Makes `DOMAIN` a verified SES identity for both directions:

- `ses.EmailIdentity` for `DOMAIN` with MAIL FROM subdomain `mail.<DOMAIN>`.
- Easy DKIM CNAME records (one per SES-generated token) in the hosted zone — these complete the domain verification.
- MX for `mail.<DOMAIN>` → `feedback-smtp.<region>.amazonses.com` (bounce feedback loop).
- SPF TXT for `mail.<DOMAIN>`: `v=spf1 include:amazonses.com ~all`.
- DMARC TXT for `_dmarc.<DOMAIN>`: `v=DMARC1;p=quarantine;rua=mailto:dmarc@<DOMAIN>` — reports are mailed into the domain and therefore arrive
  through the inbound stack below.

### emissary-in-<domain> — inbound mail

- MX for `DOMAIN` → `inbound-smtp.<region>.amazonaws.com` (this is what makes the domain receive mail).
- S3 bucket for raw EML messages: public access blocked, SSE-S3, SSL-only, objects auto-deleted on teardown. SES writes messages at the bucket
  root, keyed by message id.
- The bucket name is published to SSM at `/emissary/<domain>/bucketname` (and as a stack output) so the application stack can find it without a
  cross-stack import.
- A SES receipt rule set named `emissary-<domain>` containing one rule for the `SUBDOMAINS`-derived recipients whose S3 action drops each message
  into the bucket (SES gets `PutObject` via the bucket policy — no topic or consumer Lambda).
- A custom resource that calls `SetActiveReceiptRuleSet` after create (and clears it on delete). **SES only applies rules in the active rule set,
  and there is exactly one active rule set per account per region**: deploying Emissary makes this rule set active for the region.

### `emissary-client-<domain>` — the application

Skipped (with a warning) unless `mailer/cmd/frontend/bootstrap` exists.

- Lambda function `emissary-<domain>`: custom runtime `provided.al2023`, x86_64, 512 MB, 30s timeout, handler `bootstrap`. Its environment sets
  **every** pluggable selector explicitly: `STORAGE=s3`, `SENDER=aws-ses`, `METADATA` (default `memory`), `SESSION` (default `memory`),
  `USER_MANAGER` (default `s3`) and `RECEIVER` (default `s3`). This is required because `config.Load` no longer infers a backend from
  `TABLE_PREFIX` when several backends have configuration. It also passes the bucket, `TABLE_PREFIX` (when set), the DynamoDB region variables
  for enabled DDB backends (including `RECEIVER_REGION` when `RECEIVER=ddb`), domain, admin credentials (password only if provided), `JWT_SECRET`
  as a Secrets Manager ARN, the JWT audience/issuer, and `LOG_LEVEL` (default `ERROR`).
- Lambda function `emissary-<domain>-s3-receiver`: custom runtime `provided.al2023`, x86_64, 256 MB, 30s timeout, handler `bootstrap`, triggered
  by `OBJECT_CREATED` events on the inbound bucket. It uses the same role and environment as the frontend function.
- IAM role `emissary-<domain>-app`: shared by both Lambda functions. Lambda basic execution (logs to `/logs/emissary/<domain>` and
  `/logs/emissary/<domain>-s3-receiver`, 3-month retention), read, write and delete access to the inbound bucket (including the `receiver/`
  config prefix), `ses:SendEmail`/`ses:SendRawEmail` on the account's identities, `ses:ListIdentities` (account-wide, for the support receiver
  rules editor), read/write on all DynamoDB tables created for the configured backends, and read on the JWT secret.
- DynamoDB tables for application state (pay-per-request), named as `TABLE_PREFIX` plus a fixed suffix (`metadata`, `sessions`, `users`,
  `receiver`). There is no default prefix: the deployment fails if a DDB backend is selected without one. Each table below is created only when
  its backend is selected and is granted read/write to that shared role:
  - `metadata`: per-message metadata (JMAP keywords and `receivedAt`; partition key `id`, sort key `userId`, plus the `userId-index` global
    secondary index for per-user listing; `METADATA=ddb`).
  - `sessions`: refresh tokens (partition key `id`, sort key `userEmail`; `SESSION=ddb`).
  - `users`: the `ddb` user manager (partition key `id`; `USER_MANAGER=ddb`).
  - `receiver`: inbound routing rules (partition key `id` only, with a `rules` list attribute; `RECEIVER=ddb`).
- Secrets Manager secret `emissary/<domain>/jwt-secret` — generated when `JWT_SECRET` is not supplied, otherwise seeded with the given value via
  a custom resource. The app resolves the ARN at runtime.
- API Gateway REST API (regional, lambda proxy, compression enabled) with an ACM certificate for `mail.<DOMAIN>` (DNS validated) and a Route 53
  **alias A record** (a CNAME is impossible at that name because the MAIL FROM MX/SPF records already live there).

After deployment the application is reachable at `https://mail.<DOMAIN>` (also exported as the stack output `EmissaryAppUrl-<domain>`).

## Teardown

`cd deploy && npm run cdk -- destroy --all` removes the stacks. Note that:

- the inbound bucket is destroyed **with its contents** (`autoDeleteObjects` + `DESTROY` removal policy), as are the DynamoDB table, log group
  and JWT secret;
- deleting the inbound stack deactivates the account's active receipt rule set for the region.

If `S3_BUCKET` was used to point at a pre-existing bucket, that bucket is not managed (or deleted) by the CDK app.
