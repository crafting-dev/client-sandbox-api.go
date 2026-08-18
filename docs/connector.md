# Connector

A `Connector` handles the configuration and the authentication, and provides the
context for gRPC API calls. All the gRPC clients are created from it.

```go
connector, err := api.NewConnector(ctx, "myorg")
if err != nil {
    return err
}
defer connector.Close()

client := connector.ManagementServiceClient()
```

A `Connector` is safe for concurrent use.

## Construction

```go
func NewConnector(ctx context.Context, org string, opts ...Option) (*Connector, error)
```

| Argument | Description |
| --- | --- |
| `ctx` | Used for the login and for the call which binds to the org. |
| `org` | The org name to bind to. When it is empty, the connector binds to the only org the identity is a member of, and fails when there is more than one. |
| `opts` | Optional overrides of the defaults, see [Options](#options). |

The construction performs three tasks, all of them before it returns:

1. Resolve the [configuration](configuration.md);
2. Resolve the [authentication](authentication.md) if possible;
3. Bind to the org.

So there is nothing to wait for afterwards, and [`Org()`](#org) is usable
immediately:

```go
connector, err := api.NewConnector(ctx, "myorg")
if err != nil {
    return err
}
request := &v1.ListSandboxesRequest{OrgId: connector.Org().ID}
```

It returns an error when:

- the resolved server URL is not a usable `http` or `https` URL;
- a login was triggered and failed, see [Authentication](authentication.md);
- the org could not be resolved: the requested org is not accessible, the
  identity is a member of no org, or of more than one and no name was given.

A connector for which no authentication information was available at all does
not fail: it is constructed
[_unauthenticated_](#unauthenticated-connectors).

When the construction fails there is no connector to recover on, so a program
which supplies its own credentials should pass them with
[`WithLoginToken`](#options) rather than calling [`Login`](#login) afterwards.

### Options

```go
func WithServerURL(serverURL string) Option
func WithLoginToken(loginToken string) Option
func WithHTTPClient(client *http.Client) Option
func WithGRPCDialOptions(dialOptions ...grpc.DialOption) Option
```

| Option | Description |
| --- | --- |
| `WithServerURL` | The default server URL, for example `https://myorg.sandboxes.site`. It overrides the built-in default, but is still overridden by the environment and by the config file, see [Configuration](configuration.md). |
| `WithLoginToken` | Login as a service account with this login token. |
| `WithHTTPClient` | The HTTP client used for the [authentication endpoints](authentication.md), which are plain HTTP rather than gRPC. Defaults to `http.DefaultClient`. |
| `WithGRPCDialOptions` | Dial options appended to the ones the connector uses for its own connection. They come after, so they override the defaults. |

```go
connector, err := api.NewConnector(ctx, "myorg",
    api.WithServerURL("https://myorg.sandboxes.site"),
    api.WithLoginToken(os.Getenv("MY_SERVICE_ACCOUNT_TOKEN")))
```

The first two mirror the `server_url` and `login_token` options every language of
the SDK accepts. The other two are Go-specific escape hatches for a caller which
needs to control the transports.

## Methods

### Org

```go
func (c *Connector) Org() *OrgInfo
```

The org the connector is bound to, or `nil` when the connector is
[unauthenticated](#unauthenticated-connectors).

```go
type OrgInfo struct {
    ID   string
    Name string
}
```

The org is resolved with `ManagementService.ListOrgs`, so `Name` is the name as
the server knows it, and `ID` is the org ID most requests need:

```go
request := &v1.ListSandboxesRequest{OrgId: connector.Org().ID}
```

The returned value is a copy, so mutating it does not affect the connector.

### Authenticated

```go
func (c *Connector) Authenticated() bool
```

Whether the connector has an authentication context. It is false only when no
authentication information was available at all, because a login which was
triggered and failed fails the construction instead.

### ServerURL

```go
func (c *Connector) ServerURL() string
```

The URL of the server the connector talks to, without a trailing slash.

### ConfigFolder

```go
func (c *Connector) ConfigFolder() string
```

The local configuration folder the connector resolved.

### Login

```go
func (c *Connector) Login(ctx context.Context, loginToken string) error
```

Login as a service account with a login token and replace the current
authentication context with the result. The org is resolved again after the
login. This is how a program recovers from an unauthenticated connector:

```go
if !connector.Authenticated() {
    if err := connector.Login(ctx, loginToken); err != nil {
        return err
    }
}
```

The gRPC clients created before the login keep working: they read the
authentication context per call rather than capturing it.

A failed login leaves the connector as it was. When the login succeeds but the
org cannot be resolved, the new authentication context is kept and the org is
left unset.

### Close

```go
func (c *Connector) Close() error
```

Release the connection to the server which the clients of this connector share.
The clients cannot be used afterwards. A connector should always be closed:

```go
defer connector.Close()
```

### Client factory methods

Each method returns the gRPC client of one service, already carrying the
authentication context. See [Services](services.md) for what each service does.

| Method | Client |
| --- | --- |
| `InformationServiceClient()` | `v1.InformationServiceClient` |
| `LLMServiceClient()` | `v1.LLMServiceClient` |
| `ManagementServiceClient()` | `v1.ManagementServiceClient` |
| `SnapshotManagementServiceClient()` | `v1.SnapshotManagementServiceClient` |
| `SystemAdminServiceClient()` | `v1.SystemAdminServiceClient` |
| `TimeSeriesServiceClient()` | `v1.TimeSeriesServiceClient` |
| `TrafficServiceClient()` | `v1.TrafficServiceClient` |
| `WorkloadServiceClient()` | `v1.WorkloadServiceClient` |
| `WorkspaceServiceClient()` | `v1.WorkspaceServiceClient` |

All the clients of a connector share its one connection to the server. A
generated client is a stateless wrapper around that connection, so a factory
method is cheap and there is no reason to hold on to its result:

```go
connector.ManagementServiceClient().ListSandboxes(ctx, request)
```

## Unauthenticated connectors

When no authentication information is available at all, the construction
succeeds and leaves the connector _unauthenticated_. There is no org, and no
call can be made:

```go
connector, err := api.NewConnector(ctx, "myorg") // err is nil
connector.Authenticated()                        // false
connector.Org()                                  // nil
```

The client factory methods still hand out clients — they cannot report an error
without making every call site handle one — and the calls made on them fail with
the gRPC status code `UNAUTHENTICATED` and the message of
`api.ErrUnauthenticated`:

```go
_, err := connector.ManagementServiceClient().ListOrgs(ctx, request)
status.Code(err) // codes.Unauthenticated
```

The error crosses the gRPC transport as a status rather than as an error value,
so match on the code above rather than with `errors.Is`.

This is the state a program lands in when it runs somewhere the CLI never logged
in and no login token was provided. Recover from it with [`Login`](#login).

Note the difference with a _failed_ login: if any authentication information was
available, the connector uses it, and the failure of that procedure fails the
construction rather than falling back to this state. See
[Authentication](authentication.md).
