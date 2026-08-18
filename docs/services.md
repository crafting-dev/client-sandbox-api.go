# Services

Each gRPC service of `sandboxes.api.v1` has a client, created from a
[`Connector`](connector.md) so it carries the authentication context:

```go
client := connector.ManagementServiceClient()
```

The clients are the generated code in `pkg/proto/sandboxes/api/v1`. The RPCs, the
requests and the responses are whatever the
[protobuf files](../../../../protos/proto/sandboxes/api/v1) declare — refer to
them for the comments on every RPC.

## The services

| Service | Description |
| --- | --- |
| `ManagementService` | The Management API: orgs, sandboxes, snapshots, resources, secrets. The one most programs use. |
| `InformationService` | The Information API. |
| `WorkloadService` | Workload API: processes, logs and files of a running workload. |
| `WorkspaceService` | Exposed inside the user container when the workload runs as a workspace. |
| `SnapshotManagementService` | Snapshot management. Not for creating snapshots. |
| `TrafficService` | The Traffic API. |
| `TimeSeriesService` | The time series data stored for sandboxes. |
| `LLMService` | The LLM related service. For List and Delete, use the resource APIs on `ManagementService`. |
| `SystemAdminService` | System administration, served by a dedicated gRPC server with its own sub-domain and authorized users. |

## Unary calls

A unary RPC takes a context and a request, and returns the response:

```go
response, err := connector.ManagementServiceClient().ListSandboxes(ctx,
    &v1.ListSandboxesRequest{OrgId: connector.Org().ID})
if err != nil {
    return err
}
for _, sandbox := range response.GetSandboxes() {
    log.Println(sandbox.GetMeta().GetName())
}
```

Most requests need the org ID, which the connector resolved:

```go
request := &v1.ListSandboxesRequest{OrgId: connector.Org().ID}
```

### Errors

An error from a call is a gRPC status. Match on its code rather than on its text:

```go
if _, err := client.GetSandbox(ctx, request); err != nil {
    if status.Code(err) == codes.NotFound {
        // No such sandbox.
    }
    return err
}
```

The status codes the API uses come from the server. A call made on an
[unauthenticated connector](connector.md#unauthenticated-connectors), or one
whose token could not be renewed, fails with `codes.Unauthenticated` before it
reaches the server.

### Creating a sandbox

A sandbox is created from an app definition. `Composer.method` is a protobuf
oneof, so the value is wrapped in the generated type of the field being set — see
[Oneof fields](messages.md#oneof-fields):

```go
response, err := connector.ManagementServiceClient().CreateSandbox(ctx,
    &v1.CreateSandboxRequest{
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
    })
if err != nil {
    return err
}
log.Printf("Sandbox created: %s", response.GetSandbox().GetMeta().GetId())
```

## Server-streaming calls

A server-streaming RPC returns a stream which is read until `io.EOF`:

```go
stream, err := connector.WorkloadServiceClient().StreamLog(ctx, &v1.StreamLogRequest{
    Workload: &v1.WorkloadRef{
        OrgId:        connector.Org().ID,
        SandboxId:    sandboxID,
        WorkloadName: "dev",
    },
})
if err != nil {
    return err
}
for {
    event, err := stream.Recv()
    if errors.Is(err, io.EOF) {
        break
    }
    if err != nil {
        return err
    }
    log.Println(event.String())
}
```

`io.EOF` means the server ended the stream normally; any other error is a
failure. To stop reading early, cancel the context the call was made with:

```go
ctx, cancel := context.WithCancel(ctx)
defer cancel()
```

Without that cancel, abandoning a stream leaks it until the connector is closed.

## Bidirectional-streaming calls

A bidirectional RPC takes no request: both directions are streams.
`WorkspaceService.SyncStream` is the one the API has.

```go
stream, err := connector.WorkspaceServiceClient().SyncStream(ctx)
if err != nil {
    return err
}
if err := stream.Send(&v1.SyncStreamCommand{}); err != nil {
    return err
}
if err := stream.CloseSend(); err != nil {
    return err
}
for {
    event, err := stream.Recv()
    if errors.Is(err, io.EOF) {
        break
    }
    if err != nil {
        return err
    }
    log.Println(event.String())
}
```

Send and receive may run concurrently on separate goroutines, but `Send` itself
is not safe to call from more than one goroutine at a time, and neither is
`Recv`.

## Deadlines, metadata and call options

The context of a call carries its deadline:

```go
ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
defer cancel()

response, err := client.ListSandboxes(ctx, request)
```

Extra metadata is attached to the context. The connector sets `authorization`
itself, so leave that one alone:

```go
ctx = metadata.AppendToOutgoingContext(ctx, "x-my-header", "value")
```

Every RPC accepts `grpc.CallOption` after the request, so the standard options
work as they do anywhere else:

```go
response, err := client.ListSandboxes(ctx, request, grpc.WaitForReady(true))
```

To apply something to every call instead — an interceptor, a keepalive policy, a
retry policy — pass the dial option to the connector with
[`api.WithGRPCDialOptions`](connector.md#options).
