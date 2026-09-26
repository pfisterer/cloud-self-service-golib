# cloud-self-service-golib

The Go code that more than one service of the cloud self-service platform needs,
kept in one place instead of copied.

Used by [dynamic-zones](https://github.com/pfisterer/dynamic-zones),
[openstack-management-api](https://github.com/pfisterer/openstack-management-api)
and [role-provider-service](https://github.com/pfisterer/role-provider-service).

## What belongs in here

**Only code that at least two services need identically, and that carries no
domain logic.** That rule is the whole design. A shared library without an entry
condition becomes the place where things are put when they fit nowhere else, and
then every service depends on every other service's leftovers.

Applied to what exists today:

- `envconf` qualifies — three copies existed, and they had drifted apart.
- `logging` qualifies — three copies of the same file under two names.
- HTTP error mapping does *not*, although all three services have a file for it.
  The mappings differ deliberately: one falls back to 400 with a documented
  reason, another to 500. A shared version would have to flatten a distinction
  somebody made on purpose.
- `mcpserve` qualifies — the wiring around an MCP endpoint was written twice,
  and the read-only gate came out character-for-character identical both times.
  What did *not* come along is the error rendering: one service wraps the cause,
  the other deliberately shows only the message. That is the same kind of
  distinction as the HTTP mapping above, so it stayed put.
- Anything that mentions a zone, a project, a group or a quota does not qualify
  by definition.

Two things that look shareable and are not, so nobody has to rediscover it:

- **`internal/generated_docs/embedded.go`** is byte-identical in all three
  services and still cannot move. `//go:embed swagger.json` resolves relative to
  the package directory, so the file has to sit where it is embedded.
- **The generated role-provider client** belongs with the OpenAPI spec it is
  generated from, not here. Code generation from a published spec is the better
  mechanism, and it already exists.

## Using it

```
go get github.com/pfisterer/cloud-self-service-golib@latest
```

```go
import (
    "strings"

    "github.com/pfisterer/cloud-self-service-golib/envconf"
    "github.com/pfisterer/cloud-self-service-golib/logging"
)

cfg := Config{
    Port:    envconf.Int("API_PORT", 8080),
    DevMode: envconf.Bool("API_DEV_MODE", false),
    Origins: envconf.StringSlice("CORS_ALLOWED_ORIGINS", nil),
    Admins:  envconf.StringSet("SUPERADMIN_EMAILS", nil, strings.ToLower),
}

logger, log := logging.Init(cfg.DevMode)
defer logger.Sync()
```

The module is public, so nothing has to be configured to consume it: no
`GOPRIVATE`, no credentials in the container build, `go mod download` works as
it does for any other dependency.

## Versioning

Semver git tags, `v0.1.0` onwards. There is no VERSION file — for a library the
tag *is* the version, and a second copy of it in the tree would only be a second
thing that can be wrong.

While the major version is 0, a minor bump may break compatibility. That is
intentional for now: the API here is young and the three consumers are known.

Each service upgrades on its own schedule; nothing forces them to move together.
The price of that is the other direction — a fix here reaches production only
after three bumps and three releases, so it is worth having Dependabot or
Renovate raise those pull requests rather than remembering to.

Tags are immutable once the Go module proxy has seen them. A tag that turns out
wrong is not moved, it is superseded by the next one.

## Packages

### `envconf`

Reads configuration from environment variables. One rule, applied everywhere:
unset, empty and whitespace-only all mean "use the default", and values are
trimmed.

That rule is the reason this package exists. The three services had the same
function under the same name with different behaviour — two trimmed, one did
not — so a Helm value with a trailing blank worked in two services and broke in
the third.

A value that cannot be parsed as an integer or boolean falls back to the default
and writes one line to stderr. It does not abort: this runs before the logger
exists, and killing the process over a typo in an optional setting is worse than
continuing. But a *silent* fallback is how a typo reaches production, so the
line on stderr is not decoration.

### `authn`

`CutBearerPrefix` and `Scheme` for taking a credential out of an `Authorization`
header — both were duplicated verbatim, and `Scheme` is what makes a rejected
request loggable without writing the credential into the log.

`Claims` and `Identity` are here for a different reason. The code was not
copied; the *answer* has to be the same everywhere. A token issued for a person
by one service and a group resolved for that person by another only line up if
both mean the same string by "who this is" — and they did not.
openstack-management-api fills `PreferredUsername` from an API token and then
resolves groups by e-mail, so such a token authenticates cleanly and belongs to
no group at all.

`Identity` is deliberately not called `Email`. Which claim is canonical is
expected to be reopened: Moodle's LTI privacy settings decide whether names and
addresses are released at all, and where they are not, an opaque id or a
matriculation number is all there is. When that changes, this one function
changes — not a column in three databases.

Today `Identity` is the e-mail address, falling back to `preferred_username` and then `sub` for a provider that releases no address. It does not lowercase: the value is already stored as the owner of zones and tokens, and folding case would silently stop matching what is on disk.

### `oidcauth`

Verifies OIDC ID tokens, and does not make the identity provider a condition for
starting up. Both services used to build their verifier with
`oidc.NewProvider`, which fetches the issuer's discovery document, and treated a
failure as fatal. On 2026-09-25 the university's Keycloak went down during a
power cut: every pod that happened to restart in that window died and stayed in
CrashLoopBackOff, so the self-service was unreachable for hours after its own
cluster was healthy again.

Give `Config.JWKSURL` and the endpoint is configuration rather than a discovery
result: nothing is fetched until there is a token to check, the keys are cached
afterwards, and an outage costs only the tokens whose signing key is not yet
known. Leave it empty and discovery still happens, which is what the local and
mock setups use.

`Verify` separates two failures that look identical from the inside: a token
this service rejects, and a token it cannot judge because the provider is away
(`ErrKeysUnavailable`). Callers answer 401 for the first and 503 for the second
— a 401 tells the browser to drop its session and sign in again, which is
exactly what cannot be done while the provider is down. `KeysUnavailable` and
`LastKeyFetchError` are for a status endpoint, so the UI can say what is wrong
instead of showing a login that cannot work.

### `logging`

Builds the zap logger: coloured console at debug level in development, zap's
JSON production logger otherwise. `Writer` adapts it to `io.Writer` for
libraries that insist on writing to one, such as gin's default output.

No gin dependency in `logging` itself: `Writer` is an `io.Writer`, so it stands
in front of gin's output without importing gin. The gin *middleware* that shares
across services lives in `ginweb`, its own package — see there for why gin is
allowed into the module at all.

### `redact`

`Secret` and `ConnString` make a secret safe to put in a log line. Every service
logs its resolved configuration at startup, and that holds API keys, TSIG keys
and database passwords; each had grown its own masking helper and they had
drifted (one showed a prefix, one truncated a whole DSN to four bytes and lost
the host). `ConnString` redacts only the `password=` field of a key-value DSN,
so a broken connection is still diagnosable. No dependencies.

### `ginweb`

The HTTP wiring every gin-based service wrote the same way: `EnableCORS` (the reflected-origin-safe policy plus the OPTIONS preflight catch-all), `DisableCaching`, and the read-only-token rule (`SetReadOnly`, `IsReadOnly`, `RejectWritesForReadOnlyTokens`).

`EnableCORS` takes a `CORSOptions`: exact origins in `AllowedOrigins` (empty means no cross-origin access at all, which is right when the UI reaches the API same-origin), `DevMode` to additionally allow loopback origins for a local dev server, and `AllowMethods`/`AllowHeaders` that fall back to a common default when nil. It never reflects the request origin; an unlisted origin is refused. `RejectWritesForReadOnlyTokens` refuses anything but GET for a read-only token with a 403, which is why it belongs on the REST route group and not in the auth middleware — an MCP call is always a POST, reads included.

This is the one place the module takes gin, and the reasoning is `mcpserve`'s:
a consumer that imports no `ginweb` still does not link gin because of it, but
every consumer that *does* serve a gin router shares one implementation instead
of three that drift. The CORS policy earns the exception on its own — a
reflected-origin mistake is a credential leak, and a rule that has to be right
must not live in three files at once. What stays per service is the thin glue:
building the router, and the auth middleware that decides a token's read-only
flag and calls `SetReadOnly`.

### `token` and `tokengorm`

The API tokens people and services use in place of an interactive login.

Tokens stay **scoped to one service** rather than being one platform-wide
credential, and that is a security decision, not an oversight: a dyndns token
lives in the clear in a home router's configuration, and a credential that could
also delete OpenStack projects has no business being there. So each service
keeps its own tokens, in its own database, under its own prefix. What is shared
is the part that is easy to get wrong:

- The secret is never stored, only its SHA-256. A plain hash is enough because
  the secret is 128 bits from `crypto/rand` — there is nothing to brute-force.
- An expired token is rejected at **lookup**, not merely cleaned up on listing.
  Cleanup happens when its owner happens to open the page, which is far too rare
  to rely on.
- A token that never expires has to be asked for by name (`token.NeverExpires`,
  which is `-1`). A TTL of zero is an error, so a missing configuration value
  cannot quietly mint a permanent credential.
- Revoking requires the owner as well as the ID, so a guessed number cannot take
  away somebody else's credential.
- A request over a configured maximum lifetime is an error, not a quiet clamp: `TTLPolicy` (`Default`, `Max`, `AllowNever`) turns the hours a client asked for into a TTL, with 0 meaning the default and `token.RequestedNever` (also `-1`) asking for no expiry. The values are per deployment; the decision table is shared.

`token.Service` is created with `NewService(prefix, store)`; the prefix (e.g. `dynz_token_`) is what `Owns` checks so an auth middleware can tell a service token from an OIDC bearer token — it routes, it does not validate. `Issue` takes `IssueOptions` (TTL, read-only, and an optional description of at most `MaxDescriptionLen` characters) and returns the secret exactly once; `Lookup`, `List`, `Revoke` and `DeleteExpired` do the rest. `Lookup` also records `LastUsedAt`, at most once a minute, and ignores a failure to write it: a valid credential must not be refused over a bookkeeping column. `NewMemoryStore` is the in-memory `Store`, for a service running without a database and for tests.

A `Subject` is whatever the platform calls an identity — see `authn.Identity`.
Deliberately not "user": the reconciler that provisions course VMs will hold
tokens too, under a service identity, and nothing here should have to care.

`tokengorm` is separate so that `token` carries no database dependency — a
service running its storage in memory should not pull GORM in to get at the
credential logic. Its `Migrate` also performs the one column rename this package
inherited (`username` → `subject`), guarded and idempotent. That rename is why
the package exists rather than a bare model: `AutoMigrate` does not rename
columns, so on the old schema it would add an empty `subject` beside the
populated `username` and leave every token owned by nobody — no error, just
lookups that stop finding anything.

Use `tokengorm.NewService(prefix, db)` rather than `NewStore`: it runs `Migrate` first, so a service cannot start on an unmigrated table. `Migrate` also drops the indexes the rename left behind under their old names (on PostgreSQL and SQLite), so the table does not end up with duplicate indexes.

The tests use the cgo sqlite driver on purpose: it is the one dynamic-zones
runs, and column renaming is driver-specific enough that testing it anywhere
else would prove less than it appears to.

### `mcpserve`

Serving an MCP endpoint, minus the tools. Three things, all of which existed
twice before this package did:

- **`AddTool`** — the read-only rule for MCP. Every MCP call is a POST, reads
  included, so the HTTP method cannot stand in for "does this change
  something" the way it can for REST; the tool says what it does and the check
  runs against that. A mutating tool is left *out* for a read-only credential
  rather than offered and refused: a model picks from the tools it is shown, and
  one that always fails invites it to retry differently.
- **`Handler`** — MCP over HTTP, building a fresh server per request around the
  caller in that request's context, so a tool closes over the identity that
  called it. This is the one place the shared version is stricter than both
  originals: they took the caller with a discarded `ok`, and a request that
  somehow reached the handler unauthenticated was served with a zero-valued
  caller — no identity, `ReadOnly()` false. The most permissive caller there is,
  produced by the situation that should produce the least. Here it is a 401.
- **`ConfirmEcho`** — the "type the name back" step in front of a destructive
  tool. It is not a defence against prompt injection, and the doc comment says
  so: injected text can quote a name as easily as invent one. It catches a model
  that resolved "the old one" to the wrong thing, and it puts the real name in
  front of the person approving the call.

`Caller` is an interface with a single method, `ReadOnly() bool`. Who the caller
*is* never travels through this package — each service keeps its own caller type
with whatever identity it needs, and `WithCaller`/`CallerFrom` carry it through
the context, typed per caller type so one kind cannot be read as another.

No gin here in `mcpserve`: an MCP router is a few lines of glue that belong to
the service. The gin middleware that genuinely *did* repeat across services —
CORS, caching, the read-only rule — lives in `ginweb` instead, on the same terms
this package set: only a consumer that imports it links gin. The MCP SDK itself
does become a dependency of the module — a consumer that imports no `mcpserve`
still does not link it, but it does appear in `go.sum`.

## Development

```
make check   # go vet + go test (default target)
make test    # go test ./...
make vet     # go vet ./...
make tidy    # go mod tidy
```
