# Authentication

Once the [configuration](configuration.md) is resolved, a
[`Connector`](connector.md) resolves the authentication context at best effort.
The result is a signed JWT token, which the connector attaches to every gRPC call
as the metadata `authorization`, as `Bearer JWT-TOKEN`.

Nothing else has to be done for a call to be authenticated: the clients created
from a connector carry the context, and the caller never passes metadata.

## Resolution order

The sources are tried in this order:

1. [`api.WithLoginToken`](connector.md#options) →
   [service account login](#service-account-login);
2. the environment variable `CRAFTING_SANDBOX_AUTH_TOKEN`, whose value is a login
   token → [service account login](#service-account-login);
3. the environment variable `CRAFTING_SANDBOX_AUTH_TOKEN_FILE`, whose value is the
   path of a file holding a login token →
   [service account login](#service-account-login);
4. the [CLI authentication context](#cli-authentication-context);
5. otherwise, the connector is
   [unauthenticated](connector.md#unauthenticated-connectors).

**The first source which has the information available decides the outcome.** If
its procedure fails, the whole construction fails; the resolution does not fall
through to the remaining sources, and does not leave the connector
unauthenticated either. So a wrong login token is an error even when the CLI is
logged in on the same machine:

```go
// Fails, rather than silently using the CLI token instead.
_, err := api.NewConnector(ctx, "myorg", api.WithLoginToken("expired-token"))
```

Only step 4 is allowed to come up empty without being an error, because a missing
tokens file is the normal state of a machine where the CLI never logged in.

## Service account login

A _login token_ is exchanged for a JWT token:

- `GET $server_url/auth/token/$login_token?json` with the header
  `Accept: application/json`;
- the response is a JSON object whose field `token` is the signed JWT token.

The login token never appears in the error message of a failed login, so it is
safe to log the error.

An explicit login is also available at any time on an existing connector, see
[`Connector.Login`](connector.md#login):

```go
err := connector.Login(ctx, loginToken)
```

## CLI authentication context

When no explicit login information is available, the connector uses the JWT token
the CLI saved:

- the file `$configFolder/tokens` is a JSON object keyed by server URL;
- the entry for `$serverUrl` has the JWT token in its field `Token`.

An entry saved with a trailing slash on the URL is matched too.

A missing tokens file, or one with no entry for the target server, means no CLI
authentication context is available, and the resolution continues. A tokens file
which exists but cannot be decoded is an error: the information is there, but
unusable.

## Token renewal

A JWT token carries an expiration time in its `exp` claim. The connector decodes
it — without verifying the signature, which only the server can do — and renews
the token before it expires:

- `GET $server_url/auth/token?json` with the headers
  `Accept: application/json` and `Authorization: Bearer OLD-TOKEN`;
- the response has the same shape as a login response.

The renewal happens lazily, when a call is about to be made with a token which
expires within the next minute, and concurrent calls share one renewal request.
So a long-running program does not have to do anything to keep its authentication
valid, and a token with no expiration time is never renewed.

When the renewal fails, the call which triggered it fails with the gRPC status
code `UNAUTHENTICATED`, and so does `NewConnector` when the token it resolved was
already expiring at that point. Recover by logging in again with
[`Connector.Login`](connector.md#login).

The renewal uses the context of the call which triggered it, so a call with a
tight deadline can be the one which pays for the renewal.
