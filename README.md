# cloud-self-service-golib

The Go code that more than one service of the cloud self-service platform needs,
kept in one place instead of copied.

Used by [dynamic-zones](https://github.com/pfisterer/dynamic-zones),
[openstack-management-api](https://github.com/pfisterer/openstack-management-api),
[role-provider-service](https://github.com/pfisterer/role-provider-service) and the AI service's management API.

## What belongs in here

**Only code that at least two services need identically, and that carries no
domain logic.** That rule is the whole design. A shared library without an entry
condition becomes the place where things are put when they fit nowhere else, and
then every service depends on every other service's leftovers.

Applied to what exists today:

- `envconf` qualifies — three copies existed, and they had drifted apart.
- `logging` qualifies — three copies of the same file under two names.
- HTTP error mapping: **the mechanism is shared, the tables are per service.** All three services had a file for it, and the mappings differ deliberately: one falls back to 400 with the operation's message, another to 500 or 502 with a fixed text. So `apierr` shares the shape of an answer (status, code, text), the order in which a classification is made and the rendering for REST and MCP; which errors a service tells apart and what it answers otherwise stay in that service's table.
- `mcpserve` qualifies — the wiring around an MCP endpoint was written twice,
  and the read-only gate came out character-for-character identical both times.
  What did *not* come along is the error rendering: one service wraps the cause,
  the other deliberately shows only the message. That is the same kind of
  distinction as the HTTP mapping above: the rendering of an `apierr.Error` is shared, what a tool's error text says around it is the service's.
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

`SecretEqual` and `BearerIs` are for the machine-to-machine routes that check one configured static token: `SecretEqual(got, want)` compares in constant time, and an empty `want` never matches, so a token that was not configured locks the route instead of opening it; `BearerIs(header, want)` is `CutBearerPrefix` followed by `SecretEqual`. What a refusal looks like stays with each router.

Today `Identity` is the e-mail address, falling back to `preferred_username` and then `sub` for a provider that releases no address. An address is lowercased, because providers release it in whatever case the directory holds and two spellings of one address are one person; opaque fallbacks keep their case.

### `oidcauth`

Verifies OIDC ID tokens, and does not make the identity provider a condition for starting up. Both services used to build their verifier with `oidc.NewProvider`, which fetches the issuer's discovery document, and treated a failure as fatal: while the provider was unreachable, every pod that restarted crashed and kept crashing, so the service stayed down even after its own cluster was healthy again.

Give `Config.JWKSURL` and the endpoint is configuration rather than a discovery
result: nothing is fetched until there is a token to check, the keys are cached
afterwards, and an outage costs only the tokens whose signing key is not yet
known. Leave it empty and discovery still happens, which is what the local and
mock setups use.

`NewWithRetry(ctx, cfg, log, retry.Policy)` is `New`, tried again while discovery fails (by default for 60 s, each failed attempt logged): without `JWKSURL` a pod that starts asks the provider at once, and since the NetworkPolicies a new pod is let through only a moment later, so the first attempt can fail although nothing is wrong. With `JWKSURL` it is one call to `New`, which makes no network call; a missing issuer is not tried again.

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

`ConnString` reads the key-value form the way libpq does: the key in any case, blanks allowed around `=`, and a quoted value (`password='pa ss word'`, with `\'` inside) masked as a whole rather than word by word. In the URL form the password in the user info and a `password` query parameter in any case (`?Password=…`) are masked. A URL that does not parse comes out as `(unparsable DSN)`. An empty password reads as empty, as `Secret` has it.

### `retry`

`Do` calls a function again with a doubling pause until it succeeds, the context ends or a total time is up (by default 60 s, pauses from 250 ms up to 5 s); `Permanent` marks an error another attempt cannot fix, such as a wrong password, which then ends it at once. It exists for startup: since the NetworkPolicies a new pod is let through only a moment after it starts, so the first connection to Postgres or to an OIDC provider can fail although nothing is wrong. `OnRetry` is the hook for a log line. No dependencies.

Three fields for loops that are not about startup: `Attempts` caps the number of calls, the first included (0 = no limit), for a step that must run a fixed number of times however fast each call fails; `Jitter` spreads each pause by ± that fraction (0.1 = ±10 %) so pods that failed together do not retry in lockstep, and `OnRetry` is handed the pause as it will be waited; a negative `Total` sets no time limit, so only the context, `Attempts` or a `Permanent` error end it — a supervisor that reconnects for as long as the process runs.

### `httpx`

HTTP clients for calling other hosts, and one call of a JSON API. `NewClient(timeout)` is a client with a time limit for the whole request, answer included. `WithRetry(c)` makes a client repeat a request that failed on the way out, and `RetryingClient(timeout)` is both: at most three attempts, 1 s and 2 s apart, for a name that did not resolve (any method) and, for GET and HEAD only, for a connection that broke before an answer (reset, EOF, TLS handshake timeout). An answer, whatever its status, is never repeated, and neither is a request whose body cannot be sent again. A client without a transport of its own gets the default one with a 5 s TLS handshake limit, so a stuck handshake leaves room for another attempt.

`Do(ctx, client, Request, out)` sends a `Request` (method, URL, header, a body marshalled as JSON) and decodes a 2xx answer into `out`: `nil` reads and drops it, `*string` takes it as text, anything else is JSON; `MaxBody` caps what is read, and `EmptyOK` accepts an empty 2xx answer. It returns the status, 0 without an answer. An answer outside 2xx is a `*StatusError` with the status and the start of the body (`ErrBody`, 2 KiB by default); an error of the client comes back as it is, so the caller names its own service in the message. `BasicAuth(user, password)` is the value of an `Authorization` header for basic authentication.

It came from the AI service, whose clients to the cluster, the registry and the LLM gateway had each grown a loop around short DNS outages and broken connections. No dependencies beyond `retry`.

### `day`

A calendar day, the unit in which projects and budgets end. `Parse` reads `2006-01-02` and RFC 3339 timestamps (with or without fractional seconds, `Z` or an offset) and takes the day they fall on in Europe/Berlin: the portal's date picker reports a day as local midnight, so 31 December arrives as `2026-12-30T23:00:00.000Z`. `ParseUTCDate` is for values a writer stored as a UTC date at a time that is already the next day in Berlin, such as os-mgt's former term end `…T23:59:59Z`. `Normalize` gives the stored form `2006-01-02`. `Today`, `Of`, `Start` and `End` count in Europe/Berlin (the zone database is embedded), so something that ends on a day runs while `now.Before(d.End())`. `Date` compares by day and marshals as text, so it can sit in a JSON or YAML struct directly. Before this, end dates were compared as instants or strings in several places, and the same day written twice could look like an extension.

### `ginweb`

The HTTP wiring every gin-based service wrote the same way: `EnableCORS` (the reflected-origin-safe policy plus the OPTIONS preflight catch-all), `DisableCaching`, and the read-only-token rule (`SetReadOnly`, `IsReadOnly`, `RejectWritesForReadOnlyTokens`).

`EnableCORS` takes a `CORSOptions`: exact origins in `AllowedOrigins` (empty means no cross-origin access at all, which is right when the UI reaches the API same-origin), `DevMode` to additionally allow loopback origins for a local dev server, and `AllowMethods`/`AllowHeaders` that fall back to a common default when nil. It never reflects the request origin; an unlisted origin is refused. `RejectWritesForReadOnlyTokens` refuses anything but GET for a read-only token with a 403, which is why it belongs on the REST route group and not in the auth middleware — an MCP call is always a POST, reads included.

`RejectCrossOriginWrites` refuses a state-changing request a browser sent on behalf of a page from another origin, with 403: Go's `http.CrossOriginProtection` as gin middleware, judging `Sec-Fetch-Site` and, without it, `Origin` against `Host`; a request with neither header is no browser's and passes. `CrossOriginOptions.TrustedOrigins` takes the CORS allowlist, and `EnableCORS` mounts it with that list, so a group with CORS needs no second call. It exists because a cookie with `SameSite=Lax` is sent on a cross-origin POST from any host of the same site, and CORS only stops the page from reading the answer.

`LimitRequestBody(maxBytes)` caps a request body: a body announced larger by `Content-Length` is refused with 413 up front (`{"error": "Die Anfrage ist zu groß.", "code": "too_large"}`), one that turns out larger fails the handler's read (400, as for any malformed body). Not mounted by default, since an upload route needs a larger limit than the rest. `APISecurityHeaders` sets what a JSON API's responses should carry: `nosniff`, no framing, no referrer and a `Content-Security-Policy` that lets a response opened as a document load nothing; for API groups only, as the policy would break an HTML page with inline styles.

`Health` serves the probes: `GET /healthz` answers 200 while the process serves and checks nothing, since a liveness probe that fails over a dependency restarts a pod a restart cannot fix; `GET /readyz` runs the checks added with `AddCheck` concurrently (3 s for all of them by default) and answers 503 with the names of the failing ones under `failed`. A check added with `AddDegraded` is listed under `degraded` but never makes it 503: an identity provider that is away does not stop API tokens, and taking every pod out of the Service at once would turn a partial outage into a full one. `Mount(r)` registers both, before any authentication.

`AbortError(c, *apierr.Error)` writes an `apierr` answer. The refusals this package writes itself carry a German text and a `code` too: `cross_origin`, `too_large`, `read_only`, `internal`.

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

`token.Service` is created with `NewService(prefix, store)`; the prefix (e.g. `dynz_token_`) is what `Owns` checks so an auth middleware can tell a service token from an OIDC bearer token — it routes, it does not validate. `Issue` takes `IssueOptions` (TTL, read-only, an optional description of at most `MaxDescriptionLen` characters, and an optional `CreatedBy` naming whoever issued the token on its subject's behalf — a record, never a permission: the token still acts as its subject) and returns the secret exactly once; `Lookup`, `List`, `Revoke` and `DeleteExpired` do the rest. `Lookup` also records `LastUsedAt`, at most once a minute, and ignores a failure to write it: a valid credential must not be refused over a bookkeeping column. `NewMemoryStore` is the in-memory `Store`, for a service running without a database and for tests.

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
  produced by the situation that should produce the least. Here it is a 401 (`{"error":"Anmeldung erforderlich.","code":"unauthenticated"}`).
  The handler is stateless: the MCP protocol from `2026-07-28` on is served only that way, and a server per request keeps no session anyway. Clients still on the session-based protocol are served too, without a session id; a GET for a notification stream gets 405, since no tool sends one. So does an OPTIONS request, before the handler looks for a caller: it is no MCP call, MCP clients are no browsers, and it must not build a server or reach the SDK.
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

### `mcpserve/mcptest`

The test helpers each service with an MCP endpoint had written for itself, and the two drift checks that keep the tools in step with the REST API. It depends on the MCP SDK and `testing` only, no gin: routes are passed as `"METHOD /path"` strings, whatever router produced them.

- `InMemory(t, server)` connects a client over an in-memory transport; `Dial(t, endpoint, bearer)` connects over streamable HTTP with `Authorization: Bearer <bearer>` on every request (none for an empty bearer), so a test goes through the service's real authentication. Both close the session when the test ends.
- `ToolNames` lists the offered tools, sorted; `Call` calls one and fails the test only when the call itself fails, so a tool's error comes back as a result with `IsError`; `Text` joins a result's text content.
- `CheckRoutes(t, routes, tools, decisions)` holds every route against a `Route` decision: the `Tools` that do what it does, or the reason it is `Omitted` — exactly one of the two. It fails on a route with no decision, a decision for a route that does not exist, a named tool that is not offered, and a tool no route stands behind.
- `CheckInputs(t, inputs)` holds each tool's input struct against the request type it feeds: every field of `Domain` is offered under its name, `Renamed`, or `Omitted` with a reason; stale `Renamed` or `Omitted` entries fail too. Fields are compared by JSON name; an untagged exported field counts under its name in snake_case (`GPUClass` is `gpu_class`), unexported fields do not count, and embedded structs are flattened, as `encoding/json` does.

Neither check forces a tool or a field. A new route or field matches nothing and turns the test red, and what it asks for is a decision written down next to the others.

### `loop`

The periodic work of a service: `Run(ctx, Schedule, fn, Options)` runs `fn` after `Schedule.First` (0 = right away), then on every tick of `Interval` and whenever one of the `Nudges` receives, until the context ends; start it in a goroutine. A pass that returns an error is logged as a warning and the loop goes on. A pass that panics is recovered, logged with its stack (`Options.Log`), handed to `Options.OnPanic` (for a metric) and the loop goes on at its next tick: before this, two of openstack-management-api's loops had no recovery at all, so one bad pass ended them for the rest of the process's life without a sound. `DrainBeforeFirst` empties the nudge channels before the first pass, which covers whatever was announced before it. A nil nudge never receives, a closed one is dropped. `Pass` is one pass with the same recovery, for a pass an endpoint triggers. gin-free; it came from the AI service's `background` package and openstack-management-api's `tree.RunLoop`.

### `credential`

Who a request is from, by its `Authorization` header: `Resolve(ctx, header, tokens, verifier)` returns a `Principal` or one of four errors, and every service of the platform repeated exactly these steps. No bearer is `ErrMissing`; a bearer the service's token store `Owns` (its prefix) is looked up, and one that is unknown, expired or revoked is `ErrRejected` (wrapping `token.ErrNotFound`), while any other failure of the store comes back as an error of its own (the service's 500); every other bearer is verified as an OIDC ID token, where keys that cannot be fetched are `ErrKeysUnavailable` (503, not 401: a 401 sends the browser into a login that cannot work while the provider is away), any other failure `ErrRejected`, and no verifier at all `ErrNoVerifier`. A token with the service's prefix never reaches the provider. `Cause` is what a rejection was made from, for the log.

`Principal` has the verified `Claims` (for an API token only `Email`, holding its subject, so `Identity()` is the same string either way), the token's `Record` for an API token (`ViaAPIToken`), and `ReadOnly`. `Tokens` and `Verifier` are the interfaces `*token.Service` and `*oidcauth.Verifier` implement; nil, or a nil pointer, means none. What a refusal looks like — status, text, the service's caller type, a dev user, acting on behalf — stays with each service. It is security-critical, so the table test covers every branch with a real RSA-signed token against a served key set.

### `apierr`

How a service turns the errors of its operations into the answer of its API. `Error` is the answer: `Status`, a stable `Code` a client matches on, `Msg` for people (German, like every message to people on the platform), and a cause for the log that never reaches the caller (`Cause`, `WithCause`). A `Classifier` is one service's table: `Rules` in order — `Is` (a sentinel with a fixed answer), `IsWithText` (a sentinel answered with the error's own text, for operations whose messages are written for the caller), `As` (an error type) — and a `Fallback` (nil = `Internal`, 500 "Interner Fehler."). `Classify` answers an `*Error` anywhere in the chain as it is, then the first matching rule, then the fallback; `With` puts an area's rules in front of the service's. `Body` is `{"error": …, "code": …}`, which `ginweb.AbortError` writes; `MCPError` is the same text for an MCP tool, without the cause.

### `promtext`

The Prometheus text exposition format (0.0.4) by hand, for a service's `/metrics`: `Writer` (`Header`, `Sample`), `CounterVec` and `HistogramVec` kept in memory, and `Cache` for a part read from a database or a cluster, rendered again once it is older than its TTL (an error is not kept). Gauges computed per scrape, a few counters and a histogram need neither client_golang's registry nor its dependencies. It came from the AI service; openstack-management-api writes its reconciler's counts and the loop panics with it. No dependencies.

## Development

```
make check   # go vet + go test (default target)
make test    # go test ./...
make vet     # go vet ./...
make tidy    # go mod tidy
```
