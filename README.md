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

### `logging`

Builds the zap logger: coloured console at debug level in development, zap's
JSON production logger otherwise. `Writer` adapts it to `io.Writer` for
libraries that insist on writing to one, such as gin's default output.

No gin dependency. The gin middleware that two of the three copies carried was
called from nowhere and was not brought along, which leaves zap as this module's
only requirement.

## Development

```
make test    # go test ./...
make check   # go vet ./...
make tidy    # go mod tidy
```
