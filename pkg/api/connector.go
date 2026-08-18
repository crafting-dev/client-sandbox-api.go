// Package api is a thin wrapper over the gRPC sandbox APIs (protobuf package
// sandboxes.api.v1).
//
// The use of the library starts with a [Connector], which resolves the
// configuration and the authentication and provides the context for gRPC API
// calls. All the gRPC clients are created from it, and they carry the
// authentication context without the caller having to specify any metadata:
//
//	connector, err := api.NewConnector(ctx, "myorg")
//	if err != nil {
//	    return err
//	}
//	defer connector.Close()
//
//	resp, err := connector.ManagementServiceClient().ListSandboxes(ctx, req)
//
// The protobuf messages and the gRPC clients themselves are the generated code
// in package [pkg/proto/sandboxes/api/v1]. This library adds no schema of its
// own: refer to the original protobuf files for the schema and the comments.
//
// [pkg/proto/sandboxes/api/v1]: https://pkg.go.dev/github.com/crafting-dev/client-sandbox-api.go/pkg/proto/sandboxes/api/v1
package api

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"

	v1 "github.com/crafting-dev/client-sandbox-api.go/pkg/proto/sandboxes/api/v1"
)

// ErrUnauthenticated is the error a call fails with when the connector has no
// authentication context, because none was available when it was constructed.
// Recover from it with [Connector.Login].
//
// A call made on a client of an unauthenticated connector fails with the gRPC
// status code UNAUTHENTICATED and this message, rather than with this error
// value: the gRPC transport turns it into a status.
var ErrUnauthenticated = errors.New(
	"the connector is unauthenticated: no authentication information was found, " +
		"login with Connector.Login")

// OrgInfo is the org a [Connector] is bound to.
type OrgInfo struct {
	// ID is the org ID, which most requests need.
	ID string

	// Name is the org name, as the server knows it.
	Name string
}

// options holds the resolved values of the [Option] arguments of [NewConnector].
type options struct {
	// serverURL is the default server URL.
	serverURL string

	// loginToken is the login token of a service account to login with.
	loginToken string

	// httpClient is used for the authentication endpoints, which are plain
	// HTTP rather than gRPC.
	httpClient *http.Client

	// dialOptions are appended to the ones the connector uses itself.
	dialOptions []grpc.DialOption
}

// An Option overrides one of the defaults of a [Connector].
type Option func(*options)

// WithServerURL sets the default server URL, for example
// https://myorg.sandboxes.site.
//
// This overrides the built-in default, but it is still overridden by the
// environment variable [EnvServerURL] and by server_url from the local config
// file.
func WithServerURL(serverURL string) Option {
	return func(o *options) { o.serverURL = serverURL }
}

// WithLoginToken logs in as a service account with the given login token.
func WithLoginToken(loginToken string) Option {
	return func(o *options) { o.loginToken = loginToken }
}

// WithHTTPClient sets the HTTP client used for the authentication endpoints,
// which are plain HTTP rather than gRPC. It defaults to [http.DefaultClient].
func WithHTTPClient(client *http.Client) Option {
	return func(o *options) { o.httpClient = client }
}

// WithGRPCDialOptions appends dial options to the ones the connector uses for
// its own connection. They come after, so they override the defaults.
func WithGRPCDialOptions(dialOptions ...grpc.DialOption) Option {
	return func(o *options) { o.dialOptions = append(o.dialOptions, dialOptions...) }
}

// A Connector handles the configuration and the authentication, and provides
// the context for gRPC API calls. All the gRPC clients are created from it.
//
// A Connector is safe for concurrent use, and must be closed when it is no
// longer needed so it releases its connection to the server.
type Connector struct {
	// config is the resolved configuration.
	config configuration

	// orgName is the org name requested by the caller, empty when none was.
	orgName string

	// httpClient is used for the authentication endpoints.
	httpClient *http.Client

	// conn is the single connection all the clients of this connector share.
	conn *grpc.ClientConn

	// mu guards the authentication context and the org, which an explicit
	// login and a token renewal both replace.
	mu sync.Mutex

	// auth is the current authentication context, nil while unauthenticated.
	auth *authToken

	// org is the org this connector is bound to, nil while unauthenticated.
	org *OrgInfo

	// renewMu serializes the token renewals, so concurrent calls which all
	// noticed the token is about to expire share a single renewal request.
	renewMu sync.Mutex
}

// NewConnector constructs a Connector, which resolves the configuration,
// resolves the authentication context at best effort, and binds to the org.
//
// The org is the org name to bind to. When it is empty, the connector binds to
// the only org the authenticated identity is a member of, and fails when there
// is more than one. The org is resolved with ManagementService.ListOrgs, so
// [Connector.Org] reports the name as the server knows it along with the org ID.
//
// Resolving the authentication context may perform a login, and binding to the
// org is an API call, so both use the given context.
//
// It fails when the configuration is unusable, when a login was triggered and
// failed, or when the org could not be resolved. A connector for which no
// authentication information was available at all does not fail: it is
// constructed unauthenticated, see [ErrUnauthenticated].
func NewConnector(ctx context.Context, org string, opts ...Option) (*Connector, error) {
	resolved := options{httpClient: http.DefaultClient}
	for _, opt := range opts {
		opt(&resolved)
	}

	config, err := resolveConfiguration(resolved.serverURL)
	if err != nil {
		return nil, err
	}

	connector := &Connector{
		config:     config,
		orgName:    org,
		httpClient: resolved.httpClient,
	}
	if connector.conn, err = connector.dial(resolved.dialOptions); err != nil {
		return nil, err
	}

	// From here on the connection has to be released when anything fails: the
	// caller has no connector to close.
	auth, err := connector.resolveAuth(ctx, resolved.loginToken)
	if err != nil {
		connector.conn.Close()
		return nil, err
	}
	connector.auth = auth

	if auth != nil {
		if connector.org, err = connector.resolveOrg(ctx); err != nil {
			connector.conn.Close()
			return nil, err
		}
	}
	return connector, nil
}

// Authenticated reports whether this connector has an authentication context.
//
// It is false only when no authentication information was available at all when
// the connector was constructed, because a login which was triggered and failed
// fails the construction instead. See [ErrUnauthenticated].
func (c *Connector) Authenticated() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.auth != nil
}

// Org returns the org this connector is bound to, or nil when the connector is
// not authenticated.
func (c *Connector) Org() *OrgInfo {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.org == nil {
		return nil
	}
	org := *c.org
	return &org
}

// ServerURL returns the URL of the server this connector talks to, without a
// trailing slash.
func (c *Connector) ServerURL() string {
	return c.config.serverURL
}

// ConfigFolder returns the local configuration folder this connector resolved.
func (c *Connector) ConfigFolder() string {
	return c.config.configFolder
}

// Login logs in as a service account with a login token, and replaces the
// current authentication context with the result. The org is resolved again
// after the login.
//
// This is how a program recovers from an unauthenticated connector. The gRPC
// clients created before the login keep working: they read the authentication
// context per call rather than capturing it.
//
// When the login succeeds but the org cannot be resolved, the new
// authentication context is kept and the org is left unset.
func (c *Connector) Login(ctx context.Context, loginToken string) error {
	auth, err := loginWithToken(ctx, c.httpClient, c.config.serverURL, loginToken, sourceLogin)
	if err != nil {
		return err
	}

	c.mu.Lock()
	c.auth = auth
	c.org = nil
	c.mu.Unlock()

	org, err := c.resolveOrg(ctx)
	if err != nil {
		return err
	}

	c.mu.Lock()
	c.org = org
	c.mu.Unlock()
	return nil
}

// Close releases the connection to the server which the clients of this
// connector share. The clients cannot be used afterwards.
func (c *Connector) Close() error {
	return c.conn.Close()
}

// InformationServiceClient returns the client for sandboxes.api.v1.InformationService.
func (c *Connector) InformationServiceClient() v1.InformationServiceClient {
	return v1.NewInformationServiceClient(c.conn)
}

// LLMServiceClient returns the client for sandboxes.api.v1.LLMService.
func (c *Connector) LLMServiceClient() v1.LLMServiceClient {
	return v1.NewLLMServiceClient(c.conn)
}

// ManagementServiceClient returns the client for sandboxes.api.v1.ManagementService.
func (c *Connector) ManagementServiceClient() v1.ManagementServiceClient {
	return v1.NewManagementServiceClient(c.conn)
}

// SnapshotManagementServiceClient returns the client for
// sandboxes.api.v1.SnapshotManagementService.
func (c *Connector) SnapshotManagementServiceClient() v1.SnapshotManagementServiceClient {
	return v1.NewSnapshotManagementServiceClient(c.conn)
}

// SystemAdminServiceClient returns the client for sandboxes.api.v1.SystemAdminService.
func (c *Connector) SystemAdminServiceClient() v1.SystemAdminServiceClient {
	return v1.NewSystemAdminServiceClient(c.conn)
}

// TimeSeriesServiceClient returns the client for sandboxes.api.v1.TimeSeriesService.
func (c *Connector) TimeSeriesServiceClient() v1.TimeSeriesServiceClient {
	return v1.NewTimeSeriesServiceClient(c.conn)
}

// TrafficServiceClient returns the client for sandboxes.api.v1.TrafficService.
func (c *Connector) TrafficServiceClient() v1.TrafficServiceClient {
	return v1.NewTrafficServiceClient(c.conn)
}

// WorkloadServiceClient returns the client for sandboxes.api.v1.WorkloadService.
func (c *Connector) WorkloadServiceClient() v1.WorkloadServiceClient {
	return v1.NewWorkloadServiceClient(c.conn)
}

// WorkspaceServiceClient returns the client for sandboxes.api.v1.WorkspaceService.
func (c *Connector) WorkspaceServiceClient() v1.WorkspaceServiceClient {
	return v1.NewWorkspaceServiceClient(c.conn)
}

// dial creates the connection all the clients of this connector share.
//
// The connection is lazy, so this performs no I/O: the authentication context
// is attached per call, which is what makes a renewal and an explicit login
// apply to the clients which already exist.
func (c *Connector) dial(extra []grpc.DialOption) (*grpc.ClientConn, error) {
	transport := insecure.NewCredentials()
	if c.config.secure {
		transport = credentials.NewTLS(&tls.Config{})
	}
	dialOptions := append([]grpc.DialOption{
		grpc.WithTransportCredentials(transport),
		grpc.WithPerRPCCredentials(perRPCAuth{connector: c}),
	}, extra...)

	conn, err := grpc.NewClient(c.config.grpcTarget, dialOptions...)
	if err != nil {
		return nil, fmt.Errorf("unable to connect to %s: %w", c.config.grpcTarget, err)
	}
	return conn, nil
}

// resolveAuth resolves the authentication context at best effort.
//
// The sources are tried in the order documented for the library. The first one
// which has the information available decides the outcome: if its login
// procedure fails, the whole construction fails rather than falling through to
// the remaining sources. It returns a nil token when no authentication
// information is available at all.
func (c *Connector) resolveAuth(ctx context.Context, loginToken string) (*authToken, error) {
	serverURL := c.config.serverURL

	if loginToken != "" {
		return loginWithToken(ctx, c.httpClient, serverURL, loginToken, sourceOptions)
	}
	if envToken := os.Getenv(EnvAuthToken); envToken != "" {
		return loginWithToken(ctx, c.httpClient, serverURL, envToken, sourceEnv)
	}
	if envTokenFile := os.Getenv(EnvAuthTokenFile); envTokenFile != "" {
		return loginWithTokenFile(ctx, c.httpClient, serverURL, envTokenFile, sourceEnvFile)
	}
	return cliAuthToken(c.config.configFolder, serverURL)
}

// resolveOrg binds to the org by listing the orgs of the authenticated identity.
func (c *Connector) resolveOrg(ctx context.Context) (*OrgInfo, error) {
	request := &v1.ListOrgsRequest{}
	if c.orgName != "" {
		request.FilterByNames = []string{c.orgName}
	}

	response, err := c.ManagementServiceClient().ListOrgs(ctx, request)
	if err != nil {
		return nil, fmt.Errorf("unable to list orgs: %w", err)
	}

	var orgs []OrgInfo
	for _, withMembers := range response.GetOrgs() {
		meta := withMembers.GetOrg().GetMeta()
		if meta.GetName() != "" {
			orgs = append(orgs, OrgInfo{ID: meta.GetId(), Name: meta.GetName()})
		}
	}

	if c.orgName != "" {
		// The server may not honor filter_by_names, so match locally too.
		for _, org := range orgs {
			if org.Name == c.orgName {
				return &org, nil
			}
		}
		return nil, fmt.Errorf("org %q is not accessible", c.orgName)
	}

	switch len(orgs) {
	case 0:
		return nil, errors.New("the current identity is not a member of any org")
	case 1:
		return &orgs[0], nil
	default:
		names := make([]string, 0, len(orgs))
		for _, org := range orgs {
			names = append(names, org.Name)
		}
		return nil, fmt.Errorf(
			"the org name is required, the current identity is a member of: %s",
			strings.Join(names, ", "))
	}
}

// authorization returns the value of the gRPC metadata authorization for the
// next call, renewing the token first when it is about to expire.
func (c *Connector) authorization(ctx context.Context) (string, error) {
	c.mu.Lock()
	auth := c.auth
	c.mu.Unlock()

	if auth == nil {
		return "", ErrUnauthenticated
	}
	if auth.needsRenew(time.Now()) {
		renewed, err := c.renew(ctx, auth)
		if err != nil {
			return "", err
		}
		auth = renewed
	}
	return "Bearer " + auth.token, nil
}

// renew renews the authentication token, sharing a single request between all
// the concurrent calls which noticed the token is about to expire.
func (c *Connector) renew(ctx context.Context, expiring *authToken) (*authToken, error) {
	c.renewMu.Lock()
	defer c.renewMu.Unlock()

	c.mu.Lock()
	current := c.auth
	c.mu.Unlock()
	if current == nil {
		return nil, ErrUnauthenticated
	}
	// Another call, or an explicit login, may have replaced the token while
	// this one was waiting for the lock. Compare the identity rather than the
	// token itself: a renewal this early can legitimately reissue the same one.
	if current != expiring && !current.needsRenew(time.Now()) {
		return current, nil
	}

	renewed, err := renewToken(ctx, c.httpClient, c.config.serverURL, current)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	// Do not undo a login which happened while the renewal was in flight.
	if c.auth == current {
		c.auth = renewed
	}
	c.mu.Unlock()
	return renewed, nil
}

// perRPCAuth attaches the authentication context of a connector to every call.
//
// The token is read per call rather than captured, so a renewal or an explicit
// login applies to the clients which already exist.
type perRPCAuth struct {
	connector *Connector
}

// GetRequestMetadata implements [credentials.PerRPCCredentials].
func (a perRPCAuth) GetRequestMetadata(ctx context.Context, _ ...string) (map[string]string, error) {
	authorization, err := a.connector.authorization(ctx)
	if err != nil {
		return nil, err
	}
	return map[string]string{"authorization": authorization}, nil
}

// RequireTransportSecurity implements [credentials.PerRPCCredentials].
//
// The token is only withheld from an insecure connection when the server URL
// asked for a secure one, which cannot happen: both come from the same scheme.
// A cleartext server URL is an explicit choice by the caller.
func (a perRPCAuth) RequireTransportSecurity() bool {
	return a.connector.config.secure
}
