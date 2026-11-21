# Support tools

Emissary includes an administrative support surface for inspecting runtime
configuration, configuring inbound receiver rules and working with the inbound
mail staging area. It is available to System Managers and is reached from
**Settings → Support** in the web client, or directly through the API under
`/api/support/`. The receiver rules editor is reached from the `receiver`
configuration details (**Edit**).

The wire contract is [`api/support.yaml`](../api/support.yaml); this document
describes how the operations behave.

## Authorization

Every support operation requires a bearer token from
`POST /api/auth/login` whose `systemManager` claim is true. The built-in
`USERNAME`/`PASSWORD` administrator always receives that claim; an
Emissary-managed user receives it when their account has the System Manager
role. Ordinary users receive `403 insufficient permissions`. The obsolete
`admin` claim is no longer issued or checked.

- Missing or invalid tokens receive `401 authentication required` and a
  `WWW-Authenticate: Bearer realm="emissary"` header.
- Support endpoints ignore the ordinary per-user visibility rules. A System
  Manager can see server configuration and every staging object.
- Response bodies use the shared `{"error": "<message>"}` convention from
  [API.md](API.md#conventions).

## Endpoints

| Method | Path | Purpose |
| --- | --- | --- |
| `GET` | `/api/support/configurations` | List configuration sections and their implementations. |
| `GET` | `/api/support/configurations/{name}` | Get the full settings for one section. |
| `GET` | `/api/support/bucket/root` | List the immediate contents of the mail bucket. |
| `POST` | `/api/support/process-email` | Run the standard inbound pipeline on a staging object. |
| `GET` | `/api/support/receiver/identities` | List receiving mail identities (verified SES identities) and whether each has receiver rules. |
| `GET` | `/api/support/receiver/rules` | List the configured receiver rules (domain → ordered rules). |
| `PUT` | `/api/support/receiver/rules` | Add or replace receiver rules entries. |
| `DELETE` | `/api/support/receiver/rules/{identity}` | Remove a configured receiving identity. The `_` fallback is rejected. |

## Configuration sections

`GET /api/support/configurations` reports every runtime section with the name
of the implementation currently backing it:

| Section | Typical implementations | What it describes |
| --- | --- | --- |
| `server` | `http` | HTTP listener, JWT settings and administrator configuration. |
| `storage` | `s3`, `filesystem`, `memory` | Email storage backend. |
| `sender` | `aws-ses`, `mta`, `log` | Outbound email sender. |
| `receiver` | `memory`, `filesystem`, `s3`, `ddb` | Inbound recipient routing rules (one config per identity; `_` is the base fallback). |
| `metadata` | `memory`, `file`, `ddb` | Per-message metadata store (JMAP keywords and server-owned `receivedAt`). |
| `session` | `memory`, `ddb` | Refresh-token session store. |
| `user-manager` | `none`, `ddb`, `filesystem`, `s3`, `ldap` | Ordinary-user authentication backend. |

Every value above is an explicit implementation, not a fallback. If a selector
and its component are both absent, the support API reports `unconfigured`
instead of inventing a default; see
[Backend selection](CONFIGURATION.md#backend-selection-all-pluggable-components).

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

`GET /api/support/configurations/{name}` returns the complete settings for one
section. Names are case-insensitive; `_` and `-` are interchangeable, and
`user`, `users` and `usermanager` all resolve to `user-manager`. `receiver`,
`receiver-config`, `receiver-configuration` and `routing` all resolve to
`receiver`.

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

The `receiver` section reports the backend settings plus the rule names for
every loaded identity as `rules.<identity>` (a JSON array of rule names). For
example:

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

The `_` entry is the base fallback used by recipients without their own rules.
The editor uses the dedicated operations below instead of this flattened view,
because it needs the ordered handler groups as well as their actions.

Unknown sections return `404 configuration section not found`.

### Secret handling

Secret values are never returned. The response only reports whether a secret
is configured:

| Section | Redaction field |
| --- | --- |
| `server` | `adminPasswordConfigured`, `jwtSecretConfigured` |
| `user-manager` | `ldapBindPasswordConfigured` |

AWS credentials are never part of the configuration response. The `settings`
object contains only values that are safe for a System Manager to read;
passwords, signing secrets, LDAP bind passwords and access keys are omitted.

Because settings are string-valued, booleans are reported as `"true"` or
`"false"`. Empty or unconfigured values are omitted from the object.

## Receiver rules editor

`GET /api/support/receiver/identities` lists the receiving mail identities: the
verified identities returned by the AWS SES `ListIdentities` operation (both
domains and individual email addresses), sorted by name. `hasRules` is true
when the receiver has its own rules entry for that identity; false means mail
for it is routed by the `_` fallback. The frontend shows a tick (✓) for `true`
and a plus (+) for `false`. Each receiving identity is also the `domain` key of
the corresponding rules entry below — there is no separate mailbox field.

Example response (`200`):

```json
{
  "identities": [
    { "identity": "example.com", "hasRules": true },
    { "identity": "news.example.com", "hasRules": false }
  ]
}
```

SES identities are account-wide. The frontend role needs the
`ses:ListIdentities` IAM permission (`ListIdentities` does not support
resource-level permissions, so it is granted on `*`). A failed SES query
returns `502 failed to list SES identities`.

`GET /api/support/receiver/rules` reads the rules from the configured receiver
config store on every call, so it also serves as the GUI's **Reload** action.
Each receiving identity has exactly one ordered list of rules; the first
applicable rule wins, and `DeliverToUser` falls through when no such user
exists. The fallback `_` is returned first, followed by other identities in
name order. There are no nested handler groups or actions — the entries are
just identities and their rules.

Example response (`200`):

```json
{
  "rules": [
    { "domain": "_", "rules": ["DeliverToUser", "Bounce"] },
    { "domain": "news.example.com", "rules": ["DeliverToMailIdentity", "Bounce"] },
    { "domain": "blocked.example.com", "rules": ["Drop"] }
  ]
}
```

`domain` is the receiving identity and maps directly onto the list above. The
allowed rules are `DeliverToUser`, `DeliverToMailIdentity`, `Bounce` and
`Drop`. `DeliverToMailIdentity` delivers to the identity's own mailbox, even
when no user account exists (administrators can open it via masquerade); the
`_` fallback and a `*` key both mean the recipient's own mailbox. `Bounce`
sends a standard bounce; `Drop` discards the message for that recipient with
no delivery and no bounce. Both are terminal, so order them after any rule
that should take precedence. See
[CONFIGURATION.md](CONFIGURATION.md#receiver-inbound-routing-rules) for the
backend settings.

`PUT /api/support/receiver/rules` adds or replaces the rules entries for the
identities supplied in the request:

```json
{
  "rules": [
    { "domain": "news.example.com", "rules": ["DeliverToMailIdentity", "Bounce"] }
  ]
}
```

Identities not included in the request (in particular `_`) are left untouched,
so this operation never deletes a rules entry; use `DELETE` for that. Each
identity needs at least one known rule, identities must be valid config keys,
and the resulting config must keep a valid `_` fallback; invalid requests
return `400 invalid receiver rules`, `400 invalid handler rules`,
`400 invalid receiving domain` or `400 no rules supplied`. Store failures
return `500 failed to save receiver rules`. The response is the full saved
rules list in the same shape as `GET`.

`DELETE /api/support/receiver/rules/{identity}` removes one receiving identity's
rules entry from the config store and returns the remaining rules list. The
identity then appears with a plus in the identities list and is routed by the
`_` fallback until rules are added again. An unknown identity returns
`404 receiver rules not found`; an invalid key returns
`400 invalid receiving identity`. The `_` fallback can never be deleted —
every recipient without its own entry relies on it — so the operation returns
`400 the fallback receiving identity cannot be removed` for it. The config
store implementations enforce the same rule independently of the API.

Saved rules are written to the active receiver config store (`memory`,
`filesystem`, `s3` or `ddb`) and published to the in-process receiver used by
`ProcessEmail`. With `RECEIVER=s3` the target bucket is `RECEIVER_BUCKET`
(defaulting to `S3_BUCKET`); with `RECEIVER=filesystem` it is
`RECEIVER_FILESYSTEM_DIR` (defaulting to `FILESYSTEM_DIR`). Loading rules is
read-only, so startup and **Reload** never write to the store. A newly created support handler loads the current rules from
that store, falling back to the base rules only when no stored rules are
available. The `s3-receiver` Lambda reloads the store on every invocation, so
with a persistent backend the new rules apply to the next inbound message. With
`memory`, each process keeps its own rules.

## Bucket root

`GET /api/support/bucket/root` lists the immediate contents of the configured
mail bucket using a single `ListObjectsV2` call with `Delimiter: "/"`:

- `objects` are the raw messages SES wrote to the bucket root and that have
  not been processed yet. Their keys are the SES message IDs.
- `prefixes` are the top-level virtual directories, normally `users/` and the
  internal `_processed/` idempotency-marker prefix. See
  [S3.md](S3.md#bucket-layout) for the bucket layout.

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

This operation is available only when the configured storage is S3. With any
other backend it returns `501 bucket operations require s3 storage`. A bucket
that cannot be listed returns `502 failed to list bucket root`.

## Processing a staging email

`POST /api/support/process-email` runs the same inbound pipeline used by the
S3/SNS receivers on one object from the bucket root:

```json
{ "id": "01000192a3b4c5d6-12345678-9abc-def0-1234-56789abcdef0" }
```

The `id` is the `key` reported by `GET /api/support/bucket/root`. Processing:

1. Parses the raw message and extracts the SMTP envelope recipients from the
   `for <address>` clauses of its topmost `Received` header (the addresses SES
   accepted via `RCPT TO`), not from `To`, `Cc` or `Bcc`. A staged message with
   no envelope recipient is skipped.
2. Validates each recipient with the configured user manager.
3. Copies the message into each valid recipient's inbox at
   `users/<recipient>/inbox/<timestamp>_<ses-message-id>.eml`, where
   `<timestamp>` is ISO 8601 basic UTC (`YYYYMMDDTHHMMSS`, e.g. `20240308T143000`).
4. Sends bounces for recipients that do not exist.
5. Records a per-recipient idempotency marker, then deletes the staging object
   only when every recipient has been handled successfully.

Example response (`200`):

```json
{ "success": true, "id": "01000192a3b4c5d6-...", "message": "message processed" }
```

Processing is irreversible. Once the staging object is deleted, repeating the
call returns `404 staging object not found`. The operation returns:

| Status | Meaning |
| --- | --- |
| `400` | The `id` is missing or blank. |
| `404` | The staging object does not exist (already processed). |
| `501` | Storage is not S3. |
| `502` | The message could not be processed; details are logged server-side. |

A `502` can mean the object could not be read, the message could not be
parsed, a delivery failed, a bounce failed, or the staging delete failed. The
staging object is only deleted after all deliveries and bounces succeed. A
retry after a partial failure skips recipients that were already handled, so
it does not deliver or bounce them twice.

## Frontend

The **Support** screen is reachable from Settings only when the signed-in user
has the `systemManager` claim. It shows:

- The configuration section list with its implementation; selecting a section
  shows its settings. The `receiver` details have an **Edit** button next to
  **Close**, which opens `/support/receiver`.
- The bucket root with unprocessed staging messages.
- A **Process** action for each staging object, with a `Processing…` busy
  state. It does not use a browser dialog; see
  [DESIGN.md](DESIGN.md#frontend-ui-conventions) for the no-native-dialog rule.

The **receiver rules** screen (`/support/receiver`) shows the receiving mail
identities with tick/plus indicators, then each configured identity with its
single ordered list of rules. The identity of each entry is its `domain` key,
matching the list above; there are no mailbox identity fields, handler groups
or "actions" nesting. Clicking a **+** adds a rules entry (seeded from the
fallback rules) so it can be edited. **Remove** deletes a configured identity
via `DELETE` after an in-app confirmation; the `_` fallback button is disabled
and the backend rejects the same request. **Cancel** returns to Support,
**Reload** re-reads both lists and discards unsaved edits, and **OK** saves via
`PUT`. Rules can be reordered (↑/↓), changed or removed, and more rules can be
added with **+ Add rule**. The page uses only in-app status messages and
dialogs, never native dialogs.

Users without System Manager permission do not see the Settings entry and are
redirected away from `/support` and `/support/receiver`; the API also enforces
the `systemManager` claim regardless of the client.

## Related documentation

- [API.md](API.md) — the full HTTP API and path-ownership rules.
- [S3.md](S3.md) — bucket layout and the inbound staging flow.
- [CONFIGURATION.md](CONFIGURATION.md) — environment variables behind each
  configuration section.
