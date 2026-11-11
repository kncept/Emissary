# Deployment

Infrastructure is defined in `deploy/` as an AWS CDK v2 (TypeScript)
app and driven through `run.sh`. Everything lives in a single AWS
region (`AWS_REGION`); both the SES inbound endpoint and the receipt
rules are region-specific.

## Prerequisites

- A **Route 53 public hosted zone** for `DOMAIN` (or a parent of it).
  The app scans the account's hosted zones and picks the longest name
  that `DOMAIN` ends with.
- AWS credentials with permission to create the resources below.
- Node.js/npm (for CDK) and, to deploy the application stack, the
  Lambda binary `mailer/cmd/frontend/bootstrap` (build it with
  `./run.sh build` or `./run.sh build:lambda`).

## Commands

| Command | Effect |
| --- | --- |
| `./run.sh deploy` | Deploy **all** stacks (`cdk deploy --all`). |
| `./run.sh deploy:id` | Deploy only the identity stack. |
| `./run.sh deploy:in` | Deploy the identity + inbound stacks. |
| `./run.sh deploy:client` | Deploy only the application stack. |
| `./run.sh deploy:init` | Install the CDK dependencies (`npm install` in `deploy/`). |
| `./run.sh deploy:list` | Install dependencies and list the stacks (`cdk ls`). |

Stacks are named with the domain, dots replaced by dashes — for
`example.com`: `emissary-id-example-com`, `emissary-in-example-com`,
`emissary-client-example-com`. The per-stack targets deploy by glob,
so `deploy:id` selects `emissary-id-*`, `deploy:in` selects
`emissary-id-* emissary-in-*`, and `deploy:client` selects
`emissary-client-*`.

## Deploy-time variables

These are read by the CDK app at synthesis/deploy time (they configure
AWS, not the application directly):

| Variable | Required | Description |
| --- | --- | --- |
| `DOMAIN` | yes | The receiving domain, e.g. `example.com`. |
| `AWS_REGION` | yes | Region for SES, S3, DynamoDB, API Gateway. |
| `SUBDOMAINS` | no | Recipients accepted by the receipt rule: unset → only `DOMAIN`; `*` → `DOMAIN` plus every subdomain; a comma list → `DOMAIN` plus each entry (entries without a dot become `<entry>.<DOMAIN>`). |
| `S3_BUCKET` | no | Use this existing bucket for inbound mail instead of the created one. |
| `USERNAME` | no | Administrator username baked into the Lambda env. |
| `PASSWORD` | no | Administrator password baked into the Lambda env (a warning is printed when unset). |
| `USER_MANAGER` | no | User-manager backend for the deployed app (default `none`). |
| `SES_FROM` | no | Default From address (default `no-reply@<DOMAIN>`). |
| `JWT_SECRET` | no | Signing secret for the app. Unset → CDK generates a 64-character secret in Secrets Manager. |

## The three stacks

### `emissary-id-<domain>` — email identity

Makes `DOMAIN` a verified SES identity for both directions:

- `ses.EmailIdentity` for `DOMAIN` with MAIL FROM subdomain
  `mail.<DOMAIN>`.
- Easy DKIM CNAME records (one per SES-generated token) in the hosted
  zone — these complete the domain verification.
- MX for `mail.<DOMAIN>` → `feedback-smtp.<region>.amazonses.com`
  (bounce feedback loop).
- SPF TXT for `mail.<DOMAIN>`: `v=spf1 include:amazonses.com ~all`.
- DMARC TXT for `_dmarc.<DOMAIN>`:
  `v=DMARC1;p=quarantine;rua=mailto:dmarc@<DOMAIN>` — reports are
  mailed into the domain and therefore arrive through the inbound
  stack below.

### `emissary-in-<domain>` — inbound mail

- MX for `DOMAIN` → `inbound-smtp.<region>.amazonaws.com` (this is what
  makes the domain receive mail).
- S3 bucket for raw EML messages: public access blocked, SSE-S3,
  SSL-only, objects auto-deleted on teardown. SES writes messages at
  the bucket root, keyed by message id.
- The bucket name is published to SSM at
  `/emissary/<domain>/bucketname` (and as a stack output) so the
  application stack can find it without a cross-stack import.
- A SES receipt rule set named `emissary-<domain>` containing one rule
  for the `SUBDOMAINS`-derived recipients whose S3 action drops each
  message into the bucket (SES gets `PutObject` via the bucket policy —
  no topic or consumer Lambda).
- A custom resource that calls `SetActiveReceiptRuleSet` after create
  (and clears it on delete). **SES only applies rules in the active
  rule set, and there is exactly one active rule set per account per
  region**: deploying Emissary makes this rule set active for the
  region.

### `emissary-client-<domain>` — the application

Skipped (with a warning) unless `mailer/cmd/frontend/bootstrap` exists.

- Lambda function `emissary-<domain>`: custom runtime
  `provided.al2023`, x86_64, 512 MB, 30s timeout, handler `bootstrap`.
  Its environment pins `STORAGE=s3`, `SENDER=aws-ses`, `METADATA=ddb`
  and passes the bucket, table, domain, admin credentials (password
  only if provided), `JWT_SECRET` as a Secrets Manager ARN, and the JWT
  audience/issuer.
- IAM role `emissary-<domain>-app`: Lambda basic execution (logs to
  `/logs/emissary/<domain>`, 3-month retention), read-only access to
  the inbound bucket, `ses:SendEmail`/`ses:SendRawEmail` on the
  account's identities, read/write on the metadata table, and read on
  the JWT secret.
- DynamoDB table `emissary-<domain>-metadata` (partition key `id`,
  pay-per-request) for read/flag state — state that must survive cold
  starts.
- Secrets Manager secret `emissary/<domain>/jwt-secret` — generated
  when `JWT_SECRET` is not supplied, otherwise seeded with the given
  value via a custom resource. The app resolves the ARN at runtime.
- API Gateway REST API (regional, lambda proxy, compression enabled)
  with an ACM certificate for `mail.<DOMAIN>` (DNS validated) and a
  Route 53 **alias A record** (a CNAME is impossible at that name
  because the MAIL FROM MX/SPF records already live there).

After deployment the application is reachable at
`https://mail.<DOMAIN>` (also exported as the stack output
`EmissaryAppUrl-<domain>`).

## Teardown

`cd deploy && npx cdk destroy --all` removes the stacks. Note that:

- the inbound bucket is destroyed **with its contents**
  (`autoDeleteObjects` + `DESTROY` removal policy), as are the
  DynamoDB table, log group and JWT secret;
- deleting the inbound stack deactivates the account's active receipt
  rule set for the region.

If `S3_BUCKET` was used to point at a pre-existing bucket, that bucket
is not managed (or deleted) by the CDK app.
