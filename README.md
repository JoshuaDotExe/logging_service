## Logging service

 - Nginx
 - Account Manager
 - Log Ingest
 - Log Key Manager
 - Authoriser
 - Token Handler

## Production deployment

Deployment to an OCI Always Free VM uses Terraform and GitHub Actions. See
[docs/deployment.md](docs/deployment.md) for setup, the GitHub secrets and
variables to configure, and operations.

## Development deployment

With Docker Compose v2 and Make installed, configure `.env`, then run:

```sh
make deploy
```

Nginx is the only host-facing application service. It terminates TLS and routes
`/account`, `/login`, `/logkey/*`, `/logs`, `/log`, and `/ping` to their
respective backends. Authoriser, Token Handler, PostgreSQL, and Redis are not
published to the host.

Provide a TLS certificate and matching private key outside the repository:

```dotenv
NGINX_TLS_CERT_FILE=/absolute/external/path/fullchain.pem
NGINX_TLS_KEY_FILE=/absolute/external/path/privkey.pem
NGINX_SERVER_NAME=logging.example.test
NGINX_HTTP_PORT=80
NGINX_HTTPS_PORT=443
```

The HTTP endpoint only redirects to HTTPS. Keep certificate permissions
restricted and use a certificate whose names match the hostname clients use.
For development, these files may point to a local certificate trusted by your
machine. Do not use client-side certificate verification bypasses in load tests
or automation; install a development CA in the client trust store instead.

The ingress applies per-client-IP rate limits, request-size limits, and proxy
timeouts. Application-level account and API-key quotas are still required:
Nginx limits are an edge-abuse control, not a complete authorization or quota
system.

**Destructive: every `make deploy` deletes all PostgreSQL accounts, logs and API keys,
as well as Redis cache and rate-limit state. Do not use this target in production.**

This rebuilds the app from the current local source, checking for updated build
base images while retaining valid build cache. After a successful build, it stops
the app and migration container, removes PostgreSQL and Redis containers and
their anonymous volumes, starts fresh databases, applies the initial schema,
and recreates the app container using the new image. A failed step stops
deployment, but cannot restore data already deleted.

Deployment does not automatically pull updated PostgreSQL or Redis images.
Pin their versions and update them deliberately, particularly for PostgreSQL
major upgrades. The reset relies on the current Compose configuration using
anonymous volumes and no database bind mounts. Named volumes and bind mounts
are not removed by this command. Before adding persistent storage or deploying
to production, replace this reset workflow with a non-destructive deployment
and configure backups.

## PostgreSQL store

All PostgreSQL operations accept a caller-provided context. `WriteLog` implements
the ingestion service's `LogWriter` interface; `StoreLog` retains the row-oriented
insert API.
`StoreAPIKey` populates the entry's generated database ID after a successful
insert, allowing keymanager to return a metadata ID without exposing the hash.

The initial migration includes API-key deactivation fields. Lookups
return only non-deactivated keys whose `exp` Unix timestamp (seconds) is in the
future. Missing, expired, and deactivated keys return an error matching
`pgx.ErrNoRows` via `errors.Is`. Deactivation preserves the first deactivation
timestamp and also returns `pgx.ErrNoRows` for an unknown key.

Deactivation fields are nullable in PostgreSQL and represented by pointers in
the Go row type. The store does not synchronize Redis or hash raw API keys:
callers must supply hashes and coordinate cache updates separately.

Run focused tests with
`go test ./internal/postgresstore ./internal/authoriser/... ./internal/logservice/...`.
Set `POSTGRESSTORE_TEST_URL` to an isolated test database to also exercise
migrations, log inserts, and active-key filtering. Integration tests create and
remove their own schema and verify the initial migration up/down/up cycle.
The base development migration was consolidated; databases using the older
migration history must be recreated. `api_keys.scopes` is a non-null `text[]`
column with an empty-array default, so keys inserted without scopes receive no
permissions.

## Account login and token handler

Account manager owns credential verification. The internal token handler issues
15-minute Ed25519-signed JWT access tokens only to callers with
`X-Token-Service-Key`. It also checks that the account exists, is active,
is not deleted, and is not currently locked. It does not expose a public login
endpoint or accept caller-defined JWT claims. Compose does not publish its port.
Treat its service credential as privileged: possession permits impersonating
any eligible account.

Generate keys **outside this workspace**, so they never enter build context or
source control. For example, with OpenSSL:

```sh
mkdir -p "$HOME/.config/logging-service"
chmod 700 "$HOME/.config/logging-service"
openssl genpkey -algorithm ED25519 -out "$HOME/.config/logging-service/jwt-private.pem"
openssl pkey -in "$HOME/.config/logging-service/jwt-private.pem" \
  -pubout -out "$HOME/.config/logging-service/jwt-public.pem"
```

Set these additional `.env` values before using Compose:

```dotenv
JWT_PRIVATE_KEY_FILE=/absolute/external/path/jwt-private.pem
JWT_PUBLIC_KEY_FILE=/absolute/external/path/jwt-public.pem
JWT_ISSUER=logging-service
JWT_AUDIENCE=account-management
TOKENHANDLER_API_PORT=8084
TOKEN_SERVICE_KEY=<at-least-32-random-characters>
```

Generate the service credential with `openssl rand -hex 32`. Never commit its
value. Compose mounts the private key only into tokenhandler and the public key
into accountmanager and logkeymanager. File-backed Compose secrets retain host
file permissions; ensure the container's non-root user can read the mounted
file. For local development, a readable key file inside the owner-only host
directory above is one option. Do not loosen permissions on a shared directory.
Use managed secrets and authenticated TLS between services outside a trusted
local network.

For direct Go execution, tokenhandler requires `POSTGRES_URL`,
`TOKENHANDLER_API_PORT`, `TOKEN_SERVICE_KEY`, `JWT_PRIVATE_KEY_PATH`, `JWT_ISSUER`,
and `JWT_AUDIENCE`. Account manager requires `POSTGRES_URL`,
`ACCOUNTMANAGER_API_PORT`, `TOKENHANDLER_URL`, `TOKEN_SERVICE_KEY`,
`JWT_PUBLIC_KEY_PATH`, `JWT_ISSUER`, and `JWT_AUDIENCE`.
Run `go run ./cmd/tokenhandler` and `go run ./cmd/accountmanager`.

Public account endpoints are `POST /account`, `POST /login`, and `GET /health`.
Login accepts `{"username":"example","password":"your-password"}` and returns:

```json
{"access_token":"<signed-jwt>","token_type":"Bearer","expires_in":900}
```

Passwords are checked with bcrypt. Unknown users, incorrect passwords, inactive
or deleted accounts, locked accounts, and MFA-enabled accounts cannot log in.
MFA login is deliberately rejected until an MFA challenge flow exists.
Five failed password attempts lock an account for 15 minutes; a successful
login resets the counter. Add gateway/IP rate limiting before internet exposure:
account lockouts alone do not prevent distributed guessing or registration abuse.

**New accounts remain `pending_verification` and cannot log in.** This change
does not implement email verification or an activation endpoint. An account must
be activated through a trusted administrative process first. In a disposable
development database only, an administrator can use:

```sql
UPDATE accounts SET status = 'active', updated_at = now()
WHERE username = 'example' AND deleted_at IS NULL;
```

`GET /account` and `DELETE /account` require
`Authorization: Bearer <access_token>` and operate only on the token's account.
They no longer accept a username to select another account. Deletion returns
204. Each management request verifies signature, EdDSA algorithm, issuer,
audience, expiry, issued-at, and UUID subject locally using the configured public
key, then checks current account eligibility in PostgreSQL. Database failures
fail closed. Public health endpoints report only process liveness.

There are no refresh tokens or logout/revocation endpoint: log in again after
expiry. Suspension, deletion, or an active account lock blocks management
requests even with an unexpired JWT. These checks occur before each operation,
not as one distributed transaction with account changes. Existing logging API
keys retain their previous behavior: account suspension/deletion does not yet
revoke them.

This initial version supports one configured signing key, not JWKS or overlapping
key rotation. Replacing the pair requires coordinated service restarts and
invalidates tokens signed by the old key. Account JWTs never authorize log access.

## Log key manager

Configure `POSTGRES_URL`, `LOGKEYMANAGER_API_PORT`, `JWT_PUBLIC_KEY_PATH`,
`JWT_ISSUER`, and `JWT_AUDIENCE`, then run `go run ./cmd/logkeymanager`.
All key-management routes require an account JWT in
`Authorization: Bearer <access_token>`. The old `X-Management-Key` credential is
no longer accepted; an ordinary logging API key does not grant management access.
Use HTTPS outside a trusted local network. `GET /health` is public and reports
process liveness.

`POST /logkey` derives the account UUID from the authenticated JWT:

```json
{}
```

An optional `scopes` array may contain `["write"]`, `["read"]`, or both:

```json
{"scopes":["read","write"]}
```

Omitted scopes default to `["write"]`. Empty arrays, null, duplicate scopes, and
unknown values are rejected. An optional `account_id` is accepted for compatible
clients only if it matches the authenticated account. All active eligible
accounts may create their own read/write logging keys; no additional role policy
is introduced. Account identity is attached to the key and consequently to logs
written with it.

Creation returns HTTP 201 with `api_key`, the key's database `id`, account ID,
scopes, creation/expiry timestamps, and status metadata. Keys contain 32 random
bytes, have the `log_` prefix, and expire after 30 days. Only their SHA-256 hashes
are stored in `api_keys`; the raw key is returned in the creation response only
and cannot be retrieved later. Existing rate-limit metadata defaults to a
60-second window and 100 requests; keymanager does not enforce rate limits.

The remaining management routes are:

| Method | Route | Result |
|---|---|---|
| GET | `/logkey/account/:accountID` | `{"keys":[...]}` metadata for every key belonging to that account |
| GET | `/logkey/:id` | Metadata for a database key UUID |
| DELETE | `/logkey/:id` | Soft-delete the key; return HTTP 204 |

Listing is restricted to the authenticated account; foreign account IDs return
403. Inspecting/deleting another account's key returns 404 without exposing its
metadata. Ownership is checked before deletion.

Metadata queries include expired and soft-deleted keys for inspection, but
never return raw keys or hashes. Soft deletion retains the row, sets
`soft_deleted`, and preserves the first `deactivated_at` timestamp on repeat
requests. It does not delete existing logs. The authoriser rejects a deleted
key on subsequent lookups. Missing key IDs return 404; invalid requests return
400, unauthorized management requests return 401, and database failures return
500. Requests are limited to 4 KiB, have a 10-second deadline, and use
`Cache-Control: no-store`.

## Authoriser

The authoriser follows the service/store/HTTP-handler structure used by the
other microservices. Configure `POSTGRES_URL` and `AUTHORISER_API_PORT`, then
run `go run ./cmd/authoriser`. Database sessions are read-only; use a database
role with only the required `SELECT` privilege on `api_keys` in production.

`POST /authorise` accepts:

```json
{"api_key":"log_example"}
```

For an active key, it returns HTTP 200:

```json
{"api_key":"log_example","account_id":"00000000-0000-4000-8000-000000000001","scopes":["read","write"]}
```

The supplied key is SHA-256 hashed for lookup against `api_keys.key`; the
response echoes the supplied raw key, not the database hash. Scopes are returned
exactly as stored, including an empty array. The authoriser does not enforce
scopes, issue keys, change credentials, or consume rate-limit quotas.
Missing, expired, soft-deleted, and deactivated keys return HTTP 401; malformed
requests return 400 and database failures return 500. Requests are limited to
4 KiB and have a 10-second deadline. Credential responses use
`Cache-Control: no-store`; keys must not be put in URLs or logs. Use HTTPS
outside a trusted local network. `GET /health` reports process liveness
without performing credential lookup.

## Log ingestion

The application uses the log service and HTTP router. Configure
`LOGSERVICE_API_PORT` and `POSTGRES_URL`. For local development,
`POSTGRES_MIGRATION_URL` is used if `POSTGRES_URL` is unset; use a separate
limited-permission application role in production.

`POST /logs` accepts `{"message":"example"}` with an `X-API-Key` header.
Logservice delegates key validation to its authoriser contract and requests the
`write` scope for ingestion. The account ID returned by the authoriser is passed
through middleware; caller-supplied account IDs are ignored. Other log routes
still require an authorized key but do not request a specific scope. Messages
remain strings. Requests are limited to 1 MiB and database work has a 10-second
request deadline.

Logservice uses the real authoriser HTTP client by default
(`LOGSERVICE_AUTH_MODE=authoriser`, or leave the mode unset). Configure
`AUTHORISER_URL` with the authoriser base URL; Docker Compose defaults it to the
authoriser service's internal hostname and configured port. The client verifies
the credential response and checks the requested scope before middleware
passes the account ID to the log operation. Authoriser failures do not bypass
authentication.

For local or test use only, set `LOGSERVICE_ENV` to `local` or `test` and
`LOGSERVICE_AUTH_MODE=mock`. This mock accepts any non-empty API key and assigns
the fixed account ID `00000000-0000-4000-8000-000000000001`; logservice refuses
to start with mock authorization in other environments. Do not deploy this
mode. Other environments require the real authoriser configuration.

API-key creation and rate limiting are no longer part of logservice. The
`POST /createAPIKey` endpoint has been removed; key-management operations belong
in logkeymanager.

### Local load testing

With `LOG_API_KEY` and the HTTPS ingress URL in `LOGSERVICE_URL`, run the Go
load generator:

```sh
LOGSERVICE_URL=https://logging.example.test/logs \
  go run load_test_logs.go -requests=100000 -rate=10000 -concurrency=200
```

The request cap is 1,000,000 and the configured rate cap is 100,000,000 per
second, matching the Python script's 10-nanosecond minimum interval. Concurrency
can be configured up to 1,000,000; defaults remain 10 requests, 5 per second,
and 5 concurrent requests. These are configuration limits, not guaranteed
throughput. Actual throughput depends on scheduling, concurrency, and service
latency. Use a disposable local database: successful requests persist logs.
Completed-request RPS refreshes once per second on a single terminal line.
The final line reports average RPS for the entire run, HTTP status counts, and
transport errors; request failures do not print separate lines.

Run the account-manager functional test through a running ingress deployment:

```sh
ACCOUNTMANAGER_URL=https://logging.example.test make functional-test
```

The test creates an account that remains in the configured database. Use a
disposable test deployment, never production. Without `ACCOUNTMANAGER_URL`, the
test is skipped.

The Redis store's `SoftDeleteByAPIKeyHash` writes a permanent tombstone and
removes configuration atomically. Call it after PostgreSQL deactivation.
Tombstones intentionally have no TTL, consume memory until explicitly cleaned
up, and can still be lost through eviction or Redis data loss. PostgreSQL-only
deactivation may take up to the cache TTL to propagate. This is not an
immediate distributed revocation guarantee.

The previous `utils` middleware is no longer used by the executable.
Run `go test ./...` for unit tests; set `REDISSTORE_TEST_ADDR` to an isolated
Redis instance for Redis integration tests.
Set `POSTGRESSTORE_TEST_URL` to a disposable PostgreSQL database to run the
keymanager and authoriser database/HTTP integration tests. They create isolated
schemas and exercise key creation, scoped ingestion through logservice,
metadata retrieval, account isolation, and idempotent soft deletion. The
authoriser test checks
the scopes migration up/down/up cycle and empty legacy scopes, verifies scoped
credentials over HTTP through a read-only connection, and rejects inactive
keys. Client tests also exercise logservice's middleware through the authoriser
HTTP endpoint. Never point test variables at production services.