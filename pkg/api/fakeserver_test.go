package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	v1 "github.com/crafting-dev/client-sandbox-api.go/pkg/proto/sandboxes/api/v1"
)

// httpRequest is a request received by the auth endpoints of the fake server.
type httpRequest struct {
	// path is the request path, including the query string.
	path string

	// accept is the value of the Accept header.
	accept string

	// authorization is the value of the Authorization header.
	authorization string
}

// fakeServer is a fake Crafting Sandbox server.
//
// The real server serves the auth endpoints over HTTP/1.1 and the gRPC services
// over HTTP/2 on the same port. This reproduces that by routing on the request
// itself: an HTTP/2 request carrying the gRPC content type goes to the gRPC
// server, and everything else to the auth endpoints. So a test exercises the
// whole flow — login, then bind to an org — against a single server URL.
type fakeServer struct {
	v1.UnimplementedManagementServiceServer

	// mu guards everything a test configures or a request records, because the
	// requests are served on their own goroutines.
	mu sync.Mutex

	// orgs is what ManagementService.ListOrgs reports.
	orgs []OrgInfo

	// loginTokens maps the login tokens the auth endpoint accepts to the JWT
	// it issues for each of them.
	loginTokens map[string]string

	// renewedToken is the JWT the renew endpoint issues, empty to reject
	// renewals.
	renewedToken string

	// httpRequests are the requests the auth endpoints received so far.
	httpRequests []httpRequest

	// grpcAuthorizations are the values of the authorization metadata of the
	// gRPC calls received so far.
	grpcAuthorizations []string

	grpcServer *grpc.Server
	listener   net.Listener
	url        string
}

// startFakeServer starts a fake server on an ephemeral port, and stops it when
// the test ends.
func startFakeServer(t *testing.T) *fakeServer {
	t.Helper()

	server := &fakeServer{
		orgs:        []OrgInfo{{ID: "org-id-1", Name: "myorg"}},
		loginTokens: map[string]string{},
		grpcServer:  grpc.NewServer(),
	}
	v1.RegisterManagementServiceServer(server.grpcServer, server)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	server.listener = listener
	server.url = "http://" + listener.Addr().String()

	// h2c serves the cleartext HTTP/2 the gRPC client speaks, next to the
	// HTTP/1.1 of the auth endpoints, on this one listener.
	handler := h2c.NewHandler(http.HandlerFunc(server.serve), &http2.Server{})
	httpServer := &http.Server{Handler: handler}
	go func() { _ = httpServer.Serve(listener) }()

	t.Cleanup(func() {
		_ = httpServer.Close()
		server.grpcServer.Stop()
	})
	return server
}

// serve routes a request to the gRPC server or to the auth endpoints.
func (s *fakeServer) serve(w http.ResponseWriter, r *http.Request) {
	if r.ProtoMajor == 2 && strings.HasPrefix(r.Header.Get("Content-Type"), "application/grpc") {
		s.grpcServer.ServeHTTP(w, r)
		return
	}
	s.serveAuth(w, r)
}

// serveAuth serves the auth endpoints:
//
//   - GET /auth/token/$login_token?json exchanges a login token for a JWT;
//   - GET /auth/token?json renews the JWT of the Authorization header.
func (s *fakeServer) serveAuth(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.httpRequests = append(s.httpRequests, httpRequest{
		path:          r.URL.RequestURI(),
		accept:        r.Header.Get("Accept"),
		authorization: r.Header.Get("Authorization"),
	})

	var issued string
	switch {
	case r.URL.Path == "/auth/token":
		issued = s.renewedToken
	case strings.HasPrefix(r.URL.Path, "/auth/token/"):
		issued = s.loginTokens[strings.TrimPrefix(r.URL.Path, "/auth/token/")]
	}
	s.mu.Unlock()

	if issued == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"token": issued})
}

// ListOrgs implements the only RPC the connector itself calls.
func (s *fakeServer) ListOrgs(
	ctx context.Context,
	request *v1.ListOrgsRequest,
) (*v1.ListOrgsResponse, error) {
	authorization := ""
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if values := md.Get("authorization"); len(values) > 0 {
			authorization = values[0]
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.grpcAuthorizations = append(s.grpcAuthorizations, authorization)

	response := &v1.ListOrgsResponse{}
	for _, org := range s.orgs {
		if len(request.GetFilterByNames()) > 0 && !contains(request.GetFilterByNames(), org.Name) {
			continue
		}
		response.Orgs = append(response.Orgs, &v1.OrgWithMembers{
			Org: &v1.Org{Meta: &v1.ObjectMeta{Id: org.ID, Name: org.Name}},
		})
	}
	return response, nil
}

// setOrgs replaces the orgs ListOrgs reports.
func (s *fakeServer) setOrgs(orgs ...OrgInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.orgs = orgs
}

// acceptLoginToken makes the auth endpoint exchange a login token for a JWT.
func (s *fakeServer) acceptLoginToken(loginToken string, jwt string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loginTokens[loginToken] = jwt
}

// acceptRenewal makes the renew endpoint issue a JWT.
func (s *fakeServer) acceptRenewal(jwt string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.renewedToken = jwt
}

// recordedHTTP returns the requests the auth endpoints received so far.
func (s *fakeServer) recordedHTTP() []httpRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]httpRequest(nil), s.httpRequests...)
}

// recordedGRPC returns the authorization metadata of the gRPC calls received
// so far.
func (s *fakeServer) recordedGRPC() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.grpcAuthorizations...)
}

// fakeJWT builds a JWT with the given expiration time, which is left out of the
// claims when it is the zero time. The signature is not a real one: nothing in
// the library verifies it.
func fakeJWT(expiresAt time.Time) string {
	claims := map[string]any{"sub": "test"}
	if !expiresAt.IsZero() {
		claims["exp"] = expiresAt.Unix()
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		panic(err)
	}
	return fmt.Sprintf("%s.%s.signature",
		base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`)),
		base64.RawURLEncoding.EncodeToString(payload))
}

// contains reports whether values holds want.
func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// statusCode returns the gRPC status code an error carries, as a string.
func statusCode(err error) string {
	return status.Code(err).String()
}
