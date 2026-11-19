# Support tools

Emissary includes an administrative support surface for inspecting runtime
configuration and working with the inbound mail staging area. It is available
to System Managers and is reached from **Settings → Support** in the web
client, or directly through the API under `/api/support/`.

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

## Configuration sections

`GET /api/support/configurations` reports every runtime section with the name
of the implementation currently backing it:

| Section | Typical implementations | What it describes |
| --- | --- | --- |
| `server` | `http` | HTTP listener, JWT settings and administrator configuration. |
| `storage` | `s3`, `memory` | Email storage backend. |
| `sender` | `aws-ses`, `mta`, `log` | Outbound email sender. |
| `receiver` | `memory`, `filesystem`, `s3`, `ddb` | Inbound recipient routing rules (one config per identity; `_` is the base fallback). |
| `metadata` | `memory`, `file`, `ddb` | Per-message metadata store (JMAP keywords and server-owned `receivedAt`). |
| `session` | `memory`, `ddb` | Refresh-token session store. |
| `user-manager` | `none`, `ddb`, `s3`, `ldap` | Ordinary-user authentication backend (auto-selects `s3` when storage is S3). |
| `aws` | `aws-sdk` | AWS region and DynamoDB table naming. |

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
    "prefix": "receiver",
    "rules._": "[\"DeliverToUser\",\"Bounce\"]",
    "type": "memory"
  }
}
```

The `_` entry is the base fallback used by recipients without their own rules.

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

## Bucket root

`GET /api/support/bucket/root` lists the immediate contents of the configured
mail bucket using a single `ListObjectsV2` call with `Delimiter: "/"`:

- `objects` are the raw messages SES wrote to the bucket root and that have
  not been processed yet. Their keys are the SES message IDs.
- `prefixes` are the top-level virtual directories, normally `users/`. See
  [S3.md](S3.md#bucket-layout) for the mailbox layout.

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
  "prefixes": ["users/"]
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

1. Parses the raw message and extracts recipients from `To`, `Cc` and `Bcc`.
2. Validates each recipient with the configured user manager.
3. Copies the message into each valid recipient's inbox at
   `users/<recipient>/inbox/<timestamp>_<ses-message-id>.eml`, where
   `<timestamp>` is ISO 8601 basic UTC (`YYYYMMDDTHHMMSS`, e.g. `20240308T143000`).
4. Sends bounces for recipients that do not exist.
5. Deletes the staging object.

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
parsed, a delivery failed, or a bounce failed. The staging object is only
deleted after all deliveries and bounces have been attempted.

## Frontend

The **Support** screen is reachable from Settings only when the signed-in user
has the `systemManager` claim. It shows:

- The configuration section list with its implementation; selecting a section
  shows its settings.
- The bucket root with unprocessed staging messages.
- A **Process** action for each staging object, with a `Processing…` busy
  state. It does not use a browser dialog; see
  [DESIGN.md](DESIGN.md#frontend-ui-conventions) for the no-native-dialog rule.

Users without System Manager permission do not see the Settings entry and are
redirected away from `/support`; the API also enforces the `systemManager`
claim regardless of the client.

## Related documentation

- [API.md](API.md) — the full HTTP API and path-ownership rules.
- [S3.md](S3.md) — bucket layout and the inbound staging flow.
- [CONFIGURATION.md](CONFIGURATION.md) — environment variables behind each
  configuration section.
