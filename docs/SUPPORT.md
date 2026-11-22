# Support tools

Emissary includes an administrative support surface for inspecting runtime configuration, configuring inbound receiver rules and working with the inbound mail staging area. It is available to System Managers from **Settings → Support** in the web client, or directly under `/api/support/`. The receiver rules editor is reached from the `receiver` configuration details (**Edit**). The wire contract is [`api/support.yaml`](../api/support.yaml); [API.md](API.md#conventions) covers shared error conventions and [API.md](API.md#support-api) lists the paths.

## Authorization

Every support operation requires a Bearer token from `POST /api/auth/login` whose `systemManager` claim is true. The built-in `USERNAME`/`PASSWORD` administrator always has that claim; an Emissary-managed user gets it from the System Manager role. Ordinary users receive `403 insufficient permissions`. The obsolete `admin` claim is no longer issued or checked.

- Missing or invalid tokens receive `401 authentication required` and a `WWW-Authenticate: Bearer realm="emissary"` header.
- Support endpoints ignore ordinary per-user visibility rules: a System Manager can see server configuration and every staging object.
- Error bodies use the shared `{"error": "<message>"}` convention ([API.md](API.md#conventions)).

See [AUTH.md](AUTH.md#the-token-pair) for the token pair and its claims.

## Endpoints

| Method | Path | Purpose |
| --- | --- | --- |
| `GET` | `/api/support/configurations` | List configuration sections and their implementations. |
| `GET` | `/api/support/configurations/{name}` | Get the full settings for one section. |
| `GET` | `/api/support/bucket/root` | List the immediate contents of the mail bucket. |
| `POST` | `/api/support/process-email` | Run the standard inbound pipeline on a staging object. |
| `GET` | `/api/support/receiver/identities` | List verified SES identities and whether each has rules. |
| `GET` | `/api/support/receiver/rules` | List receiver rules (identity → ordered rules). |
| `PUT` | `/api/support/receiver/rules` | Add or replace receiver rules entries. |
| `DELETE` | `/api/support/receiver/rules/{identity}` | Remove one identity's rules; `_` is rejected. |

## Configuration sections

`GET /api/support/configurations` reports every runtime section with the name of the implementation currently backing it:

| Section | Typical implementations | What it describes |
| --- | --- | --- |
| `server` | `http` | HTTP listener, JWT and administrator settings. |
| `storage` | `s3`, `filesystem`, `memory` | Email storage backend. |
| `sender` | `aws-ses`, `mta`, `log` | Outbound mail sender. |
| `receiver` | `memory`, `filesystem`, `s3`, `ddb` | Inbound rules (one config per identity; `_` is the fallback). |
| `metadata` | `memory`, `file`, `ddb` | Keywords and server-owned `receivedAt`. |
| `session` | `memory`, `ddb` | Refresh-token session store. |
| `user-manager` | `none`, `ddb`, `filesystem`, `s3`, `ldap` | Ordinary-user authentication. |

Every value is an explicit implementation, not a fallback. If a selector and its component are both absent, the API reports `unconfigured` rather than inventing a default; see [CONFIGURATION.md](CONFIGURATION.md#backend-selection-all-pluggable-components) for the selection rules and each variable.

Example response (`200`):

```json
{
  "configurations": [
    {
      "name": "storage",
      "implementation": "s3",
      "description": "Email storage backend"
    }
  ]
}
```

## Configuration details

`GET /api/support/configurations/{name}` returns the complete settings for one section. Names are case-insensitive; `_` and `-` are interchangeable; `user`, `users` and `usermanager` resolve to `user-manager`, and `receiver-config`, `receiver-configuration` and `routing` resolve to `receiver`.

Example response (`200`) for `server`:

```json
{
  "name": "server",
  "implementation": "http",
  "description": "HTTP server and authentication",
  "settings": {
    "adminPasswordConfigured": "true",
    "adminUsername": "admin",
    "jwtAudience": "emissary",
    "jwtIssuer": "emissary",
    "jwtSecretConfigured": "true",
    "jwtTTL": "24h0m0s",
    "listenAddr": ":8080"
  }
}
```

The `receiver` section adds `identityCount` and one `rules.<identity>` entry per loaded identity, holding a JSON array of rule names:

```json
{
  "name": "receiver",
  "implementation": "memory",
  "description": "Inbound recipient routing rules",
  "settings": {
    "identityCount": "1",
    "rules._": "[\"DeliverToUser\",\"Bounce\"]",
    "type": "memory"
  }
}
```

The flattened view is for inspection; the editor below uses the dedicated operations because it needs the ordered lists. An unknown section returns `404 configuration section not found`.

### Secret handling

Secret values are never returned; the response only reports whether a secret is configured:

| Section | Redaction fields |
| --- | --- |
| `server` | `adminPasswordConfigured`, `jwtSecretConfigured` |
| `user-manager` | `ldapBindPasswordConfigured` |

AWS credentials are never part of the response. The `settings` object holds only values a System Manager can read: passwords, signing secrets, LDAP bind passwords and access keys are omitted. Settings are string-valued, so booleans appear as `"true"` or `"false"`; empty and unconfigured values are omitted. See [CONFIGURATION.md](CONFIGURATION.md#server-and-authentication) for the variables behind the `server` section.

## Receiver rules editor

`GET /api/support/receiver/identities` lists the verified SES identities (domains and individual addresses, sorted by name). `hasRules` is true when the receiver has a dedicated rules entry; false means the `_` fallback routes that mail. The frontend shows a tick for true and a plus for false. Each identity is the `domain` key of its rules entry — there is no separate mailbox field.

```json
{
  "identities": [
    { "identity": "example.com", "hasRules": true },
    { "identity": "news.example.com", "hasRules": false }
  ]
}
```

SES identities are account-wide. The `ses:ListIdentities` permission is granted on `*` because that action does not support resource-level permissions; a failed query returns `502 failed to list SES identities`.

`GET /api/support/receiver/rules` reads the active store on every call, which is also the GUI's **Reload** action. The `_` fallback comes first, then other identities in name order:

```json
{
  "rules": [
    { "domain": "_", "rules": ["DeliverToUser", "Bounce"] },
    { "domain": "news.example.com", "rules": ["DeliverToMailIdentity", "Bounce"] },
    { "domain": "blocked.example.com", "rules": ["Drop"] }
  ]
}
```

Rules are first-match-wins: `DeliverToUser` (known users only), `DeliverToMailIdentity` (the identity's own mailbox, no account required), `Bounce` (standard bounce) and `Drop` (silent discard). `Bounce` and `Drop` are terminal, so order them after rules that should take precedence; no match bounces. The `_` fallback and a `*` key both mean the recipient's own mailbox. See [CONFIGURATION.md](CONFIGURATION.md#receiver-inbound-routing-rules) for storage and [ARCHITECTURE.md](ARCHITECTURE.md#inbound-mail-flow) for routing.

`PUT /api/support/receiver/rules` adds or replaces only the supplied identities; all others, including `_`, are left untouched, so it never deletes an entry:

```json
{
  "rules": [
    { "domain": "news.example.com", "rules": ["DeliverToMailIdentity", "Bounce"] }
  ]
}
```

Each identity needs at least one known rule and a valid config key, and the resulting config must keep a valid `_` fallback. Errors are `400 invalid receiver rules`, `400 invalid handler rules`, `400 invalid receiving domain` and `400 no rules supplied`; store failures are `500 failed to save receiver rules`. The response is the full saved list in the same shape as `GET`.

`DELETE /api/support/receiver/rules/{identity}` removes one entry and returns the remaining list. The identity then shows a plus and is routed by `_` until rules are added again. Unknown identities give `404 receiver rules not found`, invalid keys `400 invalid receiving identity`, and `_` gives `400 the fallback receiving identity cannot be removed`. The config stores enforce that rule independently of the API.

Saved rules go to the active store (`memory`, `filesystem`, `s3` or `ddb`) and to the in-process receiver used by `ProcessEmail`. With `RECEIVER=s3` the bucket is `RECEIVER_BUCKET` (default `S3_BUCKET`); with `filesystem` it is `RECEIVER_FILESYSTEM_DIR` (default `FILESYSTEM_DIR`). Loading is read-only, so startup and **Reload** never write rules. The `s3-receiver` Lambda reloads on every invocation, so persistent backends apply rules to the next message; `memory` keeps rules per process.

## Bucket root

`GET /api/support/bucket/root` lists the immediate contents of the configured mail bucket with one `ListObjectsV2` call using `Delimiter: "/"`:

- `objects` are unprocessed raw messages SES wrote at the bucket root, keyed by SES message id.
- `prefixes` are top-level virtual directories, normally `users/` and the internal `_processed/` idempotency-marker prefix. See [S3.md](S3.md#bucket-layout) for the layout.

Example response (`200`):

```json
{
  "bucket": "emissary-mail-example-com",
  "objects": [
    {
      "key": "01000192a3b4c5d6-12345678-9abc-def0-1234-56789abcdef0",
      "size": 4812,
      "lastModified": "2026-11-17T10:30:45Z",
      "etag": "\"5d41402abc4b2a76b9719d911017c592\""
    }
  ],
  "prefixes": ["_processed/", "users/"]
}
```

This operation is available only with `STORAGE=s3`; otherwise it returns `501 bucket operations require s3 storage`. A listing failure returns `502 failed to list bucket root`.

## Processing a staging email

`POST /api/support/process-email` runs the standard inbound pipeline on one object from the bucket root:

```json
{ "id": "01000192a3b4c5d6-12345678-9abc-def0-1234-56789abcdef0" }
```

The `id` is a `key` from `GET /api/support/bucket/root`. Processing:

1. Parses the raw message and extracts SMTP envelope recipients from the `for <address>` clauses of its topmost `Received` header (the addresses SES accepted via `RCPT TO`), not from `To`, `Cc` or `Bcc`; a staged message with no envelope recipient is skipped.
2. Validates each recipient with the configured user manager.
3. Copies the message into each valid recipient's `inbox` at `users/<recipient>/inbox/<timestamp>_<ses-message-id>.eml`, where the timestamp is ISO 8601 basic UTC (`YYYYMMDDTHHMMSS`).
4. Sends bounces for recipients that do not exist.
5. Records a per-recipient marker, then deletes the staging object only when every recipient has been handled.

Success returns:

```json
{ "success": true, "id": "01000192a3b4c5d6-...", "message": "message processed" }
```

Processing is irreversible once staging is deleted; a retry then returns `404`. The error statuses are:

| Status | Meaning |
| --- | --- |
| `400` | The `id` is missing or blank. |
| `404` | The staging object does not exist (already processed). |
| `501` | Storage is not S3. |
| `502` | Read, parse, delivery, bounce or staging delete failed; details are logged. |

A `502` leaves staging in place for a retry, which skips recipients already marked as handled so it does not deliver or bounce them twice. See [S3.md](S3.md#inbound-flow) for the pipeline and [CONFIGURATION.md](CONFIGURATION.md#receiver-inbound-routing-rules) for the rules it applies.

## Frontend

The **Support** screen appears under Settings only for System Managers. It shows:

- The configuration section list with implementations; selecting a section shows its settings. `receiver` has an **Edit** action next to **Close** that opens `/support/receiver`.
- The bucket root with unprocessed staging messages and a **Process** action per object, with a busy state.
- No native dialogs: status messages and confirmations are in-app ([DESIGN.md](DESIGN.md#frontend-ui-conventions)).

The receiver screen lists identities with tick/plus indicators, then each configured identity with its ordered rules. **+** adds an entry seeded from the fallback, ↑/↓ reorder, **Remove** deletes after in-app confirmation (disabled for `_`), **Reload** discards unsaved edits, and **OK** saves via `PUT`. Users without the System Manager claim do not see the Settings entry and are redirected away from `/support` and `/support/receiver`; the API enforces the claim regardless of the client.

See [API.md](API.md#support-api), [S3.md](S3.md), [`frontend/src/components/Support.tsx`](../frontend/src/components/Support.tsx) and [`ReceiverSupport.tsx`](../frontend/src/components/ReceiverSupport.tsx).

## Related documentation

- [API.md](API.md) — HTTP API, path ownership and status conventions.
- [CONFIGURATION.md](CONFIGURATION.md) — environment variables behind each section.
- [S3.md](S3.md) — bucket layout and the inbound staging flow.
- [DESIGN.md](DESIGN.md) — frontend conventions and component overview.
