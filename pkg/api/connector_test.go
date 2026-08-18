package api

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	v1 "github.com/crafting-dev/client-sandbox-api.go/pkg/proto/sandboxes/api/v1"
)

// useCliToken points the connector at the fake server and gives it a CLI token
// to use, which is what a machine where the CLI logged in looks like.
func useCliToken(t *testing.T, dir string, server *fakeServer, jwt string) {
	t.Helper()
	writeConfig(t, dir, "server_url = \""+server.url+"\"\n")
	writeTokens(t, dir, map[string]any{server.url: map[string]string{"Token": jwt}})
}

// newTestConnector constructs a connector against the fake server and closes it
// when the test ends.
func newTestConnector(t *testing.T, server *fakeServer, org string, opts ...Option) *Connector {
	t.Helper()
	connector, err := NewConnector(
		context.Background(), org, append([]Option{WithServerURL(server.url)}, opts...)...)
	if err != nil {
		t.Fatalf("NewConnector: %v", err)
	}
	t.Cleanup(func() { _ = connector.Close() })
	return connector
}

func TestConnectorWithCliToken(t *testing.T) {
	dir := configDir(t)
	server := startFakeServer(t)
	jwt := fakeJWT(time.Now().Add(time.Hour))
	useCliToken(t, dir, server, jwt)

	connector := newTestConnector(t, server, "myorg")

	if !connector.Authenticated() {
		t.Error("Authenticated() = false, want true")
	}
	org := connector.Org()
	if org == nil {
		t.Fatal("Org() = nil, want the org the connector bound to")
	}
	if org.ID != "org-id-1" || org.Name != "myorg" {
		t.Errorf("Org() = %+v, want {ID: org-id-1, Name: myorg}", *org)
	}
	if connector.ServerURL() != server.url {
		t.Errorf("ServerURL() = %q, want %q", connector.ServerURL(), server.url)
	}
	if connector.ConfigFolder() != dir {
		t.Errorf("ConfigFolder() = %q, want %q", connector.ConfigFolder(), dir)
	}

	// Binding to the org is a call, and it carried the CLI token.
	if got := server.recordedGRPC(); len(got) != 1 || got[0] != "Bearer "+jwt {
		t.Errorf("gRPC authorization = %v, want one call with Bearer %s", got, jwt)
	}
}

func TestConnectorOrgIsACopy(t *testing.T) {
	dir := configDir(t)
	server := startFakeServer(t)
	useCliToken(t, dir, server, fakeJWT(time.Now().Add(time.Hour)))

	connector := newTestConnector(t, server, "myorg")
	connector.Org().ID = "mutated"
	if got := connector.Org().ID; got != "org-id-1" {
		t.Errorf("Org().ID = %q, want the connector's org to be unaffected", got)
	}
}

func TestConnectorWithLoginTokenOption(t *testing.T) {
	configDir(t)
	server := startFakeServer(t)
	jwt := fakeJWT(time.Now().Add(time.Hour))
	server.acceptLoginToken("login-token", jwt)

	connector := newTestConnector(t, server, "myorg", WithLoginToken("login-token"))

	if !connector.Authenticated() {
		t.Error("Authenticated() = false, want true")
	}
	if got := server.recordedGRPC(); len(got) != 1 || got[0] != "Bearer "+jwt {
		t.Errorf("gRPC authorization = %v, want the JWT from the login", got)
	}
}

func TestConnectorWithAuthTokenEnv(t *testing.T) {
	configDir(t)
	server := startFakeServer(t)
	jwt := fakeJWT(time.Now().Add(time.Hour))
	server.acceptLoginToken("env-token", jwt)
	t.Setenv(EnvAuthToken, "env-token")

	connector := newTestConnector(t, server, "myorg")
	if !connector.Authenticated() {
		t.Error("Authenticated() = false, want true")
	}
}

func TestConnectorWithAuthTokenFileEnv(t *testing.T) {
	configDir(t)
	server := startFakeServer(t)
	jwt := fakeJWT(time.Now().Add(time.Hour))
	server.acceptLoginToken("file-token", jwt)

	file := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(file, []byte("file-token"), 0o600); err != nil {
		t.Fatalf("write token file: %v", err)
	}
	t.Setenv(EnvAuthTokenFile, file)

	connector := newTestConnector(t, server, "myorg")
	if !connector.Authenticated() {
		t.Error("Authenticated() = false, want true")
	}
}

func TestConnectorAuthResolutionOrder(t *testing.T) {
	dir := configDir(t)
	server := startFakeServer(t)

	optionJWT := fakeJWT(time.Now().Add(time.Hour))
	envJWT := fakeJWT(time.Now().Add(2 * time.Hour))
	server.acceptLoginToken("option-token", optionJWT)
	server.acceptLoginToken("env-token", envJWT)
	useCliToken(t, dir, server, fakeJWT(time.Now().Add(3*time.Hour)))
	t.Setenv(EnvAuthToken, "env-token")

	// The option wins over the environment, which wins over the CLI context.
	connector := newTestConnector(t, server, "myorg", WithLoginToken("option-token"))
	if got := server.recordedGRPC(); len(got) != 1 || got[0] != "Bearer "+optionJWT {
		t.Errorf("gRPC authorization = %v, want the JWT from the option", got)
	}
	_ = connector
}

func TestConnectorFailedLoginDoesNotFallThrough(t *testing.T) {
	dir := configDir(t)
	server := startFakeServer(t)
	// The CLI is logged in on this machine, and must not be used instead.
	useCliToken(t, dir, server, fakeJWT(time.Now().Add(time.Hour)))

	_, err := NewConnector(context.Background(), "myorg",
		WithServerURL(server.url), WithLoginToken("expired-token"))
	if err == nil {
		t.Fatal("NewConnector succeeded, want the failed login to fail the construction")
	}
	if len(server.recordedGRPC()) != 0 {
		t.Error("a call was made, want the construction to fail before binding to the org")
	}
}

func TestConnectorUnauthenticated(t *testing.T) {
	configDir(t)
	server := startFakeServer(t)

	// Nothing to authenticate with: no login token, no environment, no CLI
	// context. The construction succeeds, unauthenticated.
	connector := newTestConnector(t, server, "myorg")

	if connector.Authenticated() {
		t.Error("Authenticated() = true, want false")
	}
	if connector.Org() != nil {
		t.Errorf("Org() = %+v, want nil", connector.Org())
	}

	// A client is still handed out, and the call it is given fails.
	_, err := connector.ManagementServiceClient().ListOrgs(
		context.Background(), &v1.ListOrgsRequest{})
	if err == nil {
		t.Fatal("ListOrgs succeeded, want it to fail on an unauthenticated connector")
	}
	if got := statusCode(err); got != "Unauthenticated" {
		t.Errorf("status code = %s, want Unauthenticated", got)
	}
	if !strings.Contains(err.Error(), "unauthenticated") {
		t.Errorf("error = %v, want it to explain the connector is unauthenticated", err)
	}
}

func TestConnectorLoginRecoversFromUnauthenticated(t *testing.T) {
	configDir(t)
	server := startFakeServer(t)
	jwt := fakeJWT(time.Now().Add(time.Hour))
	server.acceptLoginToken("login-token", jwt)

	connector := newTestConnector(t, server, "myorg")
	// The client is created before the login, and must pick up its result.
	client := connector.ManagementServiceClient()

	if err := connector.Login(context.Background(), "login-token"); err != nil {
		t.Fatalf("Login: %v", err)
	}
	if !connector.Authenticated() {
		t.Error("Authenticated() = false, want true after the login")
	}
	if org := connector.Org(); org == nil || org.Name != "myorg" {
		t.Errorf("Org() = %v, want the org resolved again after the login", org)
	}

	if _, err := client.ListOrgs(context.Background(), &v1.ListOrgsRequest{}); err != nil {
		t.Fatalf("ListOrgs on the pre-login client: %v", err)
	}
	authorizations := server.recordedGRPC()
	if len(authorizations) == 0 {
		t.Fatal("no call reached the server")
	}
	for _, authorization := range authorizations {
		if authorization != "Bearer "+jwt {
			t.Errorf("gRPC authorization = %q, want Bearer %s", authorization, jwt)
		}
	}
}

func TestConnectorLoginFails(t *testing.T) {
	configDir(t)
	server := startFakeServer(t)

	connector := newTestConnector(t, server, "myorg")
	if err := connector.Login(context.Background(), "bad-token"); err == nil {
		t.Fatal("Login succeeded, want an error")
	}
	if connector.Authenticated() {
		t.Error("Authenticated() = true, want the failed login to leave the connector as it was")
	}
}

func TestConnectorBindsToTheOnlyOrg(t *testing.T) {
	dir := configDir(t)
	server := startFakeServer(t)
	useCliToken(t, dir, server, fakeJWT(time.Now().Add(time.Hour)))
	server.setOrgs(OrgInfo{ID: "only-id", Name: "only"})

	// No org name: bind to the only org of the identity.
	connector := newTestConnector(t, server, "")
	if org := connector.Org(); org == nil || org.ID != "only-id" {
		t.Errorf("Org() = %v, want the only org", org)
	}
}

func TestConnectorOrgResolutionFailures(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		org     string
		orgs    []OrgInfo
		wantErr string
	}{
		{
			name:    "requested org is not accessible",
			org:     "myorg",
			orgs:    []OrgInfo{{ID: "other-id", Name: "other"}},
			wantErr: `org "myorg" is not accessible`,
		},
		{
			name:    "member of no org",
			org:     "",
			orgs:    nil,
			wantErr: "not a member of any org",
		},
		{
			name:    "member of more than one org and no name given",
			org:     "",
			orgs:    []OrgInfo{{ID: "a-id", Name: "a"}, {ID: "b-id", Name: "b"}},
			wantErr: "the org name is required",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			dir := configDir(t)
			server := startFakeServer(t)
			useCliToken(t, dir, server, fakeJWT(time.Now().Add(time.Hour)))
			server.setOrgs(testCase.orgs...)

			_, err := NewConnector(context.Background(), testCase.org, WithServerURL(server.url))
			if err == nil {
				t.Fatal("NewConnector succeeded, want an error")
			}
			if !strings.Contains(err.Error(), testCase.wantErr) {
				t.Errorf("error = %v, want it to mention %q", err, testCase.wantErr)
			}
		})
	}
}

func TestConnectorRenewsAnExpiringToken(t *testing.T) {
	dir := configDir(t)
	server := startFakeServer(t)

	// The CLI token is already inside the renewal window, so binding to the
	// org renews it first.
	expiring := fakeJWT(time.Now().Add(renewSkew / 2))
	renewed := fakeJWT(time.Now().Add(time.Hour))
	useCliToken(t, dir, server, expiring)
	server.acceptRenewal(renewed)

	connector := newTestConnector(t, server, "myorg")

	authorizations := server.recordedGRPC()
	if len(authorizations) != 1 || authorizations[0] != "Bearer "+renewed {
		t.Errorf("gRPC authorization = %v, want the renewed JWT", authorizations)
	}

	// The renewed token is kept, so the next call does not renew again.
	if _, err := connector.ManagementServiceClient().ListOrgs(
		context.Background(), &v1.ListOrgsRequest{}); err != nil {
		t.Fatalf("ListOrgs: %v", err)
	}
	renewals := 0
	for _, request := range server.recordedHTTP() {
		if request.path == "/auth/token?json" {
			renewals++
		}
	}
	if renewals != 1 {
		t.Errorf("got %d renewals, want 1", renewals)
	}
}

func TestConnectorFailedRenewalFailsTheConstruction(t *testing.T) {
	dir := configDir(t)
	server := startFakeServer(t)
	// Expiring, and the renew endpoint rejects it.
	useCliToken(t, dir, server, fakeJWT(time.Now().Add(renewSkew/2)))

	if _, err := NewConnector(context.Background(), "myorg", WithServerURL(server.url)); err == nil {
		t.Fatal("NewConnector succeeded, want the failed renewal to fail the construction")
	}
}

func TestConnectorConcurrentCallsShareOneRenewal(t *testing.T) {
	dir := configDir(t)
	server := startFakeServer(t)
	useCliToken(t, dir, server, fakeJWT(time.Now().Add(time.Hour)))

	connector := newTestConnector(t, server, "myorg")

	// Put the connector's token inside the renewal window behind its back, so
	// the concurrent calls below all notice it at the same time.
	renewed := fakeJWT(time.Now().Add(2 * time.Hour))
	server.acceptRenewal(renewed)
	connector.mu.Lock()
	connector.auth = &authToken{
		token:     connector.auth.token,
		expiresAt: time.Now().Add(renewSkew / 2),
		source:    sourceCLI,
	}
	connector.mu.Unlock()

	const calls = 8
	errs := make(chan error, calls)
	for range calls {
		go func() {
			_, err := connector.ManagementServiceClient().ListOrgs(
				context.Background(), &v1.ListOrgsRequest{})
			errs <- err
		}()
	}
	for range calls {
		if err := <-errs; err != nil {
			t.Errorf("ListOrgs: %v", err)
		}
	}

	renewals := 0
	for _, request := range server.recordedHTTP() {
		if request.path == "/auth/token?json" {
			renewals++
		}
	}
	if renewals != 1 {
		t.Errorf("got %d renewals for %d concurrent calls, want 1", renewals, calls)
	}
}

func TestConnectorRejectsAnUnusableServerURL(t *testing.T) {
	configDir(t)
	if _, err := NewConnector(context.Background(), "myorg", WithServerURL("ftp://example.com")); err == nil {
		t.Error("NewConnector succeeded, want an unusable server URL to fail")
	}
}

func TestConnectorClientFactories(t *testing.T) {
	dir := configDir(t)
	server := startFakeServer(t)
	useCliToken(t, dir, server, fakeJWT(time.Now().Add(time.Hour)))

	connector := newTestConnector(t, server, "myorg")

	// Every service has a factory, and they all share the one connection.
	if connector.InformationServiceClient() == nil ||
		connector.LLMServiceClient() == nil ||
		connector.ManagementServiceClient() == nil ||
		connector.SnapshotManagementServiceClient() == nil ||
		connector.SystemAdminServiceClient() == nil ||
		connector.TimeSeriesServiceClient() == nil ||
		connector.TrafficServiceClient() == nil ||
		connector.WorkloadServiceClient() == nil ||
		connector.WorkspaceServiceClient() == nil {
		t.Error("a client factory returned nil")
	}
}

func TestConnectorCloseIsIdempotentlySafe(t *testing.T) {
	dir := configDir(t)
	server := startFakeServer(t)
	useCliToken(t, dir, server, fakeJWT(time.Now().Add(time.Hour)))

	connector, err := NewConnector(context.Background(), "myorg", WithServerURL(server.url))
	if err != nil {
		t.Fatalf("NewConnector: %v", err)
	}
	if err := connector.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// A call after Close fails rather than panicking.
	if _, err := connector.ManagementServiceClient().ListOrgs(
		context.Background(), &v1.ListOrgsRequest{}); err == nil {
		t.Error("ListOrgs succeeded after Close, want an error")
	}
}

func TestErrUnauthenticatedIsExported(t *testing.T) {
	configDir(t)
	server := startFakeServer(t)
	connector := newTestConnector(t, server, "myorg")

	// The connector reports it directly, before the gRPC transport turns it
	// into a status.
	if _, err := connector.authorization(context.Background()); !errors.Is(err, ErrUnauthenticated) {
		t.Errorf("authorization() error = %v, want ErrUnauthenticated", err)
	}
}
