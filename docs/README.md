# Crafting Sandbox API for Go

A thin wrapper over the gRPC sandbox APIs (protobuf package `sandboxes.api.v1`).

The library is two packages:

| Package | Description |
| --- | --- |
| [`pkg/api`](connector.md) | The [`Connector`](connector.md): handles configuration and authentication, and creates the gRPC clients. |
| [`pkg/proto/sandboxes/api/v1`](messages.md) | The generated protobuf messages, enums and gRPC service clients of `sandboxes.api.v1`. |

```go
import (
    "github.com/crafting-dev/client-sandbox-api.go/pkg/api"
    v1 "github.com/crafting-dev/client-sandbox-api.go/pkg/proto/sandboxes/api/v1"
)
```

The generated package is named `v1`, after the last element of its protobuf
package. Import it under an explicit `v1` alias as above: the name is not obvious
from the import path, and the alias is what makes `v1.CreateSandboxRequest` read
the way the protobuf files do.

## Quick start

```go
package main

import (
    "context"
    "log"

    "github.com/crafting-dev/client-sandbox-api.go/pkg/api"
    v1 "github.com/crafting-dev/client-sandbox-api.go/pkg/proto/sandboxes/api/v1"
)

func main() {
    ctx := context.Background()

    // Resolves the configuration, the authentication and the org.
    connector, err := api.NewConnector(ctx, "myorg")
    if err != nil {
        log.Fatalf("Create connector: %v", err)
    }
    defer connector.Close()

    response, err := connector.ManagementServiceClient().ListSandboxes(ctx,
        &v1.ListSandboxesRequest{OrgId: connector.Org().ID})
    if err != nil {
        log.Fatalf("List sandboxes: %v", err)
    }
    for _, sandbox := range response.GetSandboxes() {
        log.Println(sandbox.GetMeta().GetName())
    }
}
```

Everything the library does starts from a `Connector`: it resolves where the
server is, who the caller is, and which org the calls apply to. The gRPC clients
it hands out already carry the authentication context, so no call ever has to
pass metadata.

`NewConnector` does all of that before it returns, so `connector.Org()` is
usable on the next line. Both the login and the call which binds to the org use
the context passed to it.

## Documentation

- [Connector](connector.md) — construction, methods and lifetime.
- [Configuration](configuration.md) — the server URL and the local configuration folder.
- [Authentication](authentication.md) — login tokens, the CLI context and token renewal.
- [Services](services.md) — the gRPC services, and how to call unary and streaming RPCs.
- [Messages](messages.md) — working with the generated protobuf types.

Refer to the [original protobuf files](../../../../protos/proto/sandboxes/api/v1)
for the schema and the comments on every message and RPC. This library adds no
schema of its own: whatever the protobuf files say is what the API accepts.

## Layout

| Path | Description |
| --- | --- |
| `pkg/api/connector.go` | The `Connector`, and the package documentation. |
| `pkg/api/config.go` | Resolution of the server URL and the configuration folder. |
| `pkg/api/auth.go` | The login, renewal and CLI context procedures. |
| `pkg/api/*_test.go` | The tests, run with `go test ./...`. |
| `pkg/proto/` | The generated protobuf and gRPC code, see [BUILD](../BUILD.md). |
