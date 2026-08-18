# Configuration

The configuration provides the target server URL and the local configuration
folder. It is resolved during the construction of a
[`Connector`](connector.md), and is readable from it afterwards:

```go
connector.ServerURL()    // https://myorg.sandboxes.site
connector.ConfigFolder() // /home/me/.config/crafting/sandbox
```

## Configuration folder

The configuration folder holds the config file and the authentication context
saved by the CLI. It is resolved from the environment variable
`SANDBOX_CONFIG_DIR` (`api.EnvConfigDir`), and when that is unset it defaults per
platform:

| Platform | Default |
| --- | --- |
| Mac | `~/.crafting/sandbox` |
| Linux, including WSL | `~/.config/crafting/sandbox` |

The folder does not have to exist. When it does not, there is no config file and
no CLI authentication context, which are both non-fatal.

## Server URL

The server URL is resolved from the first of these which is available:

1. the environment variable `CRAFTING_SANDBOX_SERVER_URL` (`api.EnvServerURL`);
2. [`api.WithServerURL`](connector.md#options);
3. the field `server_url` in `$configFolder/config.toml`;
4. the default `https://sandboxes.cloud` (`api.DefaultServerURL`).

Trailing slashes are removed, so the value is always safe to concatenate with a
path. The URL must use the `http` or `https` scheme and name a host; anything
else makes `NewConnector` return an error.

The scheme also decides how the gRPC connection is made: `https` uses TLS,
`http` connects in cleartext. The gRPC target is the host of the URL with its
port, defaulting to 443 for `https` and 80 for `http`.

A cleartext server URL also stops the authentication token from being withheld:
the gRPC per-RPC credentials require transport security exactly when the server
URL asked for it, so `http` is an explicit choice to send the token in the clear.

### config.toml

Only the top-level `server_url` key is read:

```toml
# The server this machine talks to.
server_url = "https://myorg.sandboxes.site"
```

A key of the same name inside a `[table]` is not the one being read, and is
ignored. An unreadable or malformed config file is not an error: the resolution
falls through to the default.

## Environment variables

The complete set of environment variables the library reads:

| Variable | Constant | Description |
| --- | --- | --- |
| `SANDBOX_CONFIG_DIR` | `api.EnvConfigDir` | The configuration folder. |
| `CRAFTING_SANDBOX_SERVER_URL` | `api.EnvServerURL` | The server URL, overriding every other source. |
| `CRAFTING_SANDBOX_AUTH_TOKEN` | `api.EnvAuthToken` | A login token, see [Authentication](authentication.md). |
| `CRAFTING_SANDBOX_AUTH_TOKEN_FILE` | `api.EnvAuthTokenFile` | The path of a file holding a login token, see [Authentication](authentication.md). |
