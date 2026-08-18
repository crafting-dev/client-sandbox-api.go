// The examples in this file mirror the snippets in the README and in docs/, so
// they are compiled together with the package and cannot drift from it. They
// carry no "Output:" comment, so `go test` builds them without running them:
// they would need a real server.

package api_test

import (
	"context"
	"errors"
	"io"
	"log"
	"os"

	"github.com/crafting-dev/client-sandbox-api.go/pkg/api"
	v1 "github.com/crafting-dev/client-sandbox-api.go/pkg/proto/sandboxes/api/v1"
)

// Everything starts from a Connector: it resolves where the server is, who the
// caller is, and which org the calls apply to.
func Example() {
	ctx := context.Background()

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

// A sandbox is created from an app definition.
func ExampleConnector_ManagementServiceClient() {
	ctx := context.Background()

	connector, err := api.NewConnector(ctx, "myorg")
	if err != nil {
		log.Fatalf("Create connector: %v", err)
	}
	defer connector.Close()

	response, err := connector.ManagementServiceClient().CreateSandbox(ctx, &v1.CreateSandboxRequest{
		OrgId: connector.Org().ID,
		Name:  "my-sandbox",
		Composer: &v1.Composer{
			// Composer.method is a protobuf oneof, so the value is wrapped in
			// the generated type of the field being set.
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
		log.Fatalf("Create sandbox: %v", err)
	}
	log.Printf("Sandbox created: %s", response.GetSandbox().GetMeta().GetId())
}

// The server URL and the login token can both be overridden, which is what a
// program running outside a machine where the CLI logged in does.
func ExampleNewConnector_options() {
	ctx := context.Background()

	connector, err := api.NewConnector(ctx, "myorg",
		api.WithServerURL("https://myorg.sandboxes.site"),
		api.WithLoginToken(os.Getenv("MY_SERVICE_ACCOUNT_TOKEN")))
	if err != nil {
		log.Fatalf("Create connector: %v", err)
	}
	defer connector.Close()
}

// Passing an empty org name binds to the only org the identity is a member of.
func ExampleNewConnector_singleOrg() {
	ctx := context.Background()

	connector, err := api.NewConnector(ctx, "")
	if err != nil {
		log.Fatalf("Create connector: %v", err)
	}
	defer connector.Close()

	log.Printf("Bound to org %s (%s)", connector.Org().Name, connector.Org().ID)
}

// A connector constructed where no authentication information was available is
// unauthenticated. Recover from it with Login.
func ExampleConnector_Login() {
	ctx := context.Background()

	connector, err := api.NewConnector(ctx, "myorg")
	if err != nil {
		log.Fatalf("Create connector: %v", err)
	}
	defer connector.Close()

	if !connector.Authenticated() {
		if err := connector.Login(ctx, os.Getenv("MY_SERVICE_ACCOUNT_TOKEN")); err != nil {
			log.Fatalf("Login: %v", errors.Join(err, api.ErrUnauthenticated))
		}
	}
	log.Printf("Bound to org %s", connector.Org().ID)
}

// A server streaming RPC is read until io.EOF.
func ExampleConnector_WorkloadServiceClient() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	connector, err := api.NewConnector(ctx, "myorg")
	if err != nil {
		log.Fatalf("Create connector: %v", err)
	}
	defer connector.Close()

	stream, err := connector.WorkloadServiceClient().StreamLog(ctx, &v1.StreamLogRequest{
		Workload: &v1.WorkloadRef{
			OrgId:        connector.Org().ID,
			SandboxId:    "sandbox-id",
			WorkloadName: "dev",
		},
	})
	if err != nil {
		log.Fatalf("Stream log: %v", err)
	}
	for {
		event, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			log.Fatalf("Receive: %v", err)
		}
		log.Println(event.String())
	}
}
