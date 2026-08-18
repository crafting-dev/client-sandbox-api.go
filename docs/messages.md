# Messages

The protobuf messages, enums and service clients of `sandboxes.api.v1` are the
generated code in `pkg/proto/sandboxes/api/v1`. The package is named `v1`, so
import it under an explicit alias:

```go
import v1 "github.com/crafting-dev/client-sandbox-api.go/pkg/proto/sandboxes/api/v1"
```

This is ordinary [protoc-gen-go](https://protobuf.dev/reference/go/go-generated/)
output, with no wrappers of its own. What follows is the part of it which comes up
when calling this API; the protobuf Go reference covers the rest.

## Messages

A message is a struct, always used as a pointer, with fields named in Go style
after the protobuf field names:

```go
request := &v1.ListSandboxesRequest{
    OrgId:         connector.Org().ID,
    FilterByNames: []string{"my-sandbox"},
}
```

Never copy a message by value: it carries internal state, and `go vet` reports it
as copying a lock. Pass and store `*v1.ListSandboxesRequest`.

## Fields

Read a field through its getter rather than the struct field. Every getter is
nil-safe on the receiver, which is what makes a chain through nested messages
safe without checking each level:

```go
// Safe even when the sandbox, its meta, or the whole response is nil.
name := response.GetSandbox().GetMeta().GetName()
```

Reading the struct field directly panics on a nil message, so the getters are
worth the habit:

```go
name := response.Sandbox.Meta.Name // panics when Sandbox is nil
```

Set fields on the struct literal, as above: there are no setters.

## Presence

A scalar field with no presence — the proto3 default — is the zero value when it
was not set, and there is no way to tell "unset" from "set to zero":

```go
sandbox.GetMeta().GetName() // "" when unset or set to ""
```

A message field does have presence, and is `nil` when it was not set. So a
timestamp which was never set reads as nil rather than as the epoch:

```go
if createdAt := sandbox.GetMeta().GetCreatedAt(); createdAt != nil {
    log.Println(createdAt.AsTime())
}
```

## Repeated fields

A repeated field is a slice, and its getter returns nil when it is empty, which
ranges zero times:

```go
for _, sandbox := range response.GetSandboxes() {
    log.Println(sandbox.GetMeta().GetName())
}

request := &v1.ListOrgsRequest{FilterByNames: []string{"myorg"}}
```

## Map fields

A map field is a Go map. Its getter returns nil when it is empty, which reads as
missing for every key:

```go
labels := map[string]string{"team": "platform"}
request := &v1.CreateSandboxRequest{OrgId: orgID, Name: "my-sandbox", Labels: labels}

value := org.GetFeatures()["some-feature"] // "" when absent
```

## Enums

An enum is a named integer type with one constant per value, prefixed by the
enum's Go type path:

```go
mode := v1.Composer_Exclusion_FULL
log.Println(mode.String())     // "FULL"
log.Println(int32(mode))       // 1
```

The zero value is the `UNSPECIFIED` member, so an unset enum field reads as
unspecified rather than as the first real value.

## Oneof fields

A oneof becomes one interface-typed field, plus a generated wrapper struct per
member. Set it by assigning the wrapper of the member being used:

```go
composer := &v1.Composer{
    Method: &v1.Composer_FromAppDefinition_{
        FromAppDefinition: &v1.Composer_FromAppDefinition{
            AppDefinition: &v1.AppDefinition{},
        },
    },
}
```

Note the two similar names: `Composer_FromAppDefinition_`, with the trailing
underscore, is the oneof wrapper, and `Composer_FromAppDefinition` is the message
it holds. The wrapper name is the one which gets the underscore, because the
message already took the plain name.

Read it with a type switch on the oneof field, or with the getter of the member,
which returns nil when a different member is set:

```go
switch method := composer.GetMethod().(type) {
case *v1.Composer_FromApp_:
    log.Println(method.FromApp.GetAppId())
case *v1.Composer_FromAppDefinition_:
    log.Println(method.FromAppDefinition.GetAppDefinition())
}

if fromAppDefinition := composer.GetFromAppDefinition(); fromAppDefinition != nil {
    log.Println(fromAppDefinition.GetAppDefinition())
}
```

## Nested types

A type nested inside a message is flattened into the package, with the names
joined by an underscore. So `Composer.FromAppDefinition` is
`v1.Composer_FromAppDefinition`, and `AppDefinition.Workspace` is
`v1.AppDefinition_Workspace`:

```go
workspaces := []*v1.AppDefinition_Workspace{{Name: "dev"}}
```

## Well-known types

`google.protobuf.Timestamp` and `google.protobuf.Duration` come from the protobuf
runtime, which converts them to and from the Go types:

```go
import (
    "google.golang.org/protobuf/types/known/durationpb"
    "google.golang.org/protobuf/types/known/timestamppb"
)

createdAt := sandbox.GetMeta().GetCreatedAt().AsTime()   // time.Time
retention := policy.GetRetention().AsDuration()          // time.Duration

request := &v1.CreateLoginTokenRequest{
    OrgId:    connector.Org().ID,
    ExpireAt: timestamppb.New(time.Now().Add(time.Hour)),
}
policy.Retention = durationpb.New(24 * time.Hour)
```

`AsTime` and `AsDuration` are nil-safe, but a nil timestamp becomes the zero
`time.Time` rather than a zero-value `time.Time` you can distinguish — check for
nil first when the difference matters, see [Presence](#presence).

`google.protobuf.Any` holds a packed message, unpacked with
[`anypb`](https://pkg.go.dev/google.golang.org/protobuf/types/known/anypb):

```go
var result v1.ValidationResult
if err := apiError.GetDetails().UnmarshalTo(&result); err != nil {
    return err
}
```

## The wire format and JSON

The messages are ordinary protobuf messages, so the standard packages work on
them. Use `proto.Clone` rather than assignment to copy one, `proto.Equal` rather
than `==` to compare, and `protojson` rather than `encoding/json` to serialize:

```go
import (
    "google.golang.org/protobuf/encoding/protojson"
    "google.golang.org/protobuf/proto"
)

encoded, err := proto.Marshal(sandbox)      // the binary wire format
asJSON, err := protojson.Marshal(sandbox)   // the protobuf JSON mapping
```

`encoding/json` does not produce the protobuf JSON mapping — it misses the field
name conventions, the enum names and the well-known types — so it should not be
used on these types.
