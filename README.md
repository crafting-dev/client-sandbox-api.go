# Crafting Sandbox API for Go

A thin wrapper over the gRPC sandbox APIs (protobuf package `sandboxes.api.v1`).

```sh
go get github.com/crafting-dev/client-sandbox-api.go
```

## Quick Start

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

    connector, err := api.NewConnector(ctx, "myorg")
    if err != nil {
        log.Fatalf("Create connector: %v", err)
    }
    defer connector.Close()

    req := &v1.CreateSandboxRequest{
        OrgId: connector.Org().ID,
        Name:  "my-sandbox",
        Composer: &v1.Composer{
            Method: &v1.Composer_FromAppDefinition_{
                FromAppDefinition: &v1.Composer_FromAppDefinition{
                    AppDefinition: &v1.AppDefinition{
                        Workspaces: []*v1.AppDefinition_Workspace{{Name: "dev"}},
                    },
                },
            },
        },
    }
    resp, err := connector.ManagementServiceClient().CreateSandbox(ctx, req)
    if err != nil {
        log.Fatalf("Create sandbox: %v", err)
    }
    log.Printf("Sandbox created: %s", resp.GetSandbox().GetMeta().GetId())
}
```

Everything starts from a `Connector`, which resolves the configuration and the
authentication and creates the gRPC clients. On a machine where the Crafting CLI
is logged in, the example above needs no credentials of its own.

## Documentation

- [Connector](docs/connector.md) — construction, methods and lifetime.
- [Configuration](docs/configuration.md) — the server URL and the local configuration folder.
- [Authentication](docs/authentication.md) — login tokens, the CLI context and token renewal.
- [Services](docs/services.md) — the gRPC services, and how to call unary and streaming RPCs.
- [Messages](docs/messages.md) — working with the generated protobuf types.

See [BUILD](BUILD.md) to regenerate the protobuf code and run the tests.
