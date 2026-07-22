package egressgateway

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"testing"
)

type resolverFake struct {
	addresses []netip.Addr
	err       error
}

type resolverByHost map[string][]netip.Addr

func (r resolverByHost) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	return append([]netip.Addr(nil), r[host]...), nil
}

func (r resolverFake) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return append([]netip.Addr(nil), r.addresses...), r.err
}

func workspaceTLSState(t *testing.T) *tls.ConnectionState {
	t.Helper()
	identity, err := url.Parse("spiffe://workspace.platform.example.com/tenant/tenant-1/project/project-1/workspace/workspace-1/task/task-1/agent/agent-1")
	if err != nil {
		t.Fatal(err)
	}
	leaf := &x509.Certificate{URIs: []*url.URL{identity}}
	return &tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf}, VerifiedChains: [][]*x509.Certificate{{leaf}}}
}

func TestGateway_DeniesUnverifiedTargetsAndDNSRebindingBeforeDial(t *testing.T) {
	for _, test := range []struct {
		name      string
		target    string
		tls       *tls.ConnectionState
		addresses []netip.Addr
		status    int
	}{
		{name: "no mTLS", target: "gitlab.com:443", addresses: []netip.Addr{netip.MustParseAddr("8.8.8.8")}, status: http.StatusUnauthorized},
		{name: "not allowed", target: "evil.example:443", tls: workspaceTLSState(t), addresses: []netip.Addr{netip.MustParseAddr("8.8.8.8")}, status: http.StatusForbidden},
		{name: "non TLS port", target: "gitlab.com:80", tls: workspaceTLSState(t), addresses: []netip.Addr{netip.MustParseAddr("8.8.8.8")}, status: http.StatusForbidden},
		{name: "mixed DNS answer", target: "gitlab.com:443", tls: workspaceTLSState(t), addresses: []netip.Addr{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("169.254.169.254")}, status: http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			dialed := false
			gateway := Gateway{
				AllowedHosts: []string{"gitlab.com", "*.registry.example.com"}, DeniedCIDRs: DefaultDeniedCIDRs(), TrustDomain: "workspace.platform.example.com", Resolver: resolverFake{addresses: test.addresses},
				DialContext: func(context.Context, string, string) (net.Conn, error) { dialed = true; return nil, io.EOF },
			}
			request := httptest.NewRequest(http.MethodConnect, "https://egress.invalid", nil)
			request.Host = test.target
			request.TLS = test.tls
			response := httptest.NewRecorder()
			gateway.ServeHTTP(response, request)
			if response.Code != test.status || dialed {
				t.Fatalf("status=%d dialed=%v body=%q", response.Code, dialed, response.Body.String())
			}
		})
	}
}

func TestGateway_UsesResolvedPublicIPAndBridgesTunnel(t *testing.T) {
	upstreamClient, upstreamServer := net.Pipe()
	defer upstreamServer.Close()
	var dialAddress string
	gateway := Gateway{
		AllowedHosts: []string{"*.example.com"}, DeniedCIDRs: DefaultDeniedCIDRs(), TrustDomain: "workspace.platform.example.com", Resolver: resolverFake{addresses: []netip.Addr{netip.MustParseAddr("8.8.8.8")}},
		DialContext: func(_ context.Context, _, address string) (net.Conn, error) {
			dialAddress = address
			return upstreamClient, nil
		},
	}
	client, server := net.Pipe()
	writer := newHijackWriter(server)
	request := httptest.NewRequest(http.MethodConnect, "https://egress.invalid", nil)
	request.Host = "packages.example.com:443"
	request.TLS = workspaceTLSState(t)
	done := make(chan struct{})
	go func() { gateway.ServeHTTP(writer, request); close(done) }()
	reader := bufio.NewReader(client)
	response, err := http.ReadResponse(reader, request)
	if err != nil || response.StatusCode != http.StatusOK || dialAddress != "8.8.8.8:443" {
		t.Fatalf("response=%#v address=%q err=%v", response, dialAddress, err)
	}
	if _, err := client.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 4)
	if _, err := io.ReadFull(upstreamServer, buffer); err != nil || string(buffer) != "ping" {
		t.Fatalf("upstream=%q err=%v", buffer, err)
	}
	_ = client.Close()
	_ = upstreamServer.Close()
	<-done
}

func TestGateway_AllowsOnlyExactNonStandardControlPlaneTarget(t *testing.T) {
	for _, test := range []struct {
		name   string
		target string
		status int
	}{
		{name: "exact control plane", target: "workspace-manager.example.com:32443", status: http.StatusOK},
		{name: "adjacent port", target: "workspace-manager.example.com:32444", status: http.StatusForbidden},
		{name: "other host", target: "packages.example.com:32443", status: http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			gateway := Gateway{
				AllowedHosts: []string{"workspace-manager.example.com", "packages.example.com"}, ControlPlaneTargets: []string{"workspace-manager.example.com:32443"},
				DeniedCIDRs: DefaultDeniedCIDRs(), TrustDomain: "workspace.platform.example.com", Resolver: resolverFake{addresses: []netip.Addr{netip.MustParseAddr("8.8.8.8")}},
			}
			request := httptest.NewRequest(http.MethodConnect, "https://egress.invalid", nil)
			request.Host = test.target
			request.TLS = workspaceTLSState(t)
			if test.status != http.StatusOK {
				gateway.DialContext = func(context.Context, string, string) (net.Conn, error) {
					t.Fatal("denied target reached dialer")
					return nil, io.EOF
				}
				response := httptest.NewRecorder()
				gateway.ServeHTTP(response, request)
				if response.Code != test.status {
					t.Fatalf("status=%d", response.Code)
				}
				return
			}
			upstreamClient, upstreamServer := net.Pipe()
			defer upstreamServer.Close()
			gateway.DialContext = func(context.Context, string, string) (net.Conn, error) { return upstreamClient, nil }
			client, server := net.Pipe()
			done := make(chan struct{})
			go func() { gateway.ServeHTTP(newHijackWriter(server), request); close(done) }()
			response, err := http.ReadResponse(bufio.NewReader(client), request)
			if err != nil || response.StatusCode != test.status {
				t.Fatalf("status=%v err=%v", response, err)
			}
			_ = client.Close()
			_ = upstreamServer.Close()
			<-done
		})
	}
}

func TestGateway_AllowsOnlyExactInternalServiceInsideServiceCIDR(t *testing.T) {
	resolver := resolverByHost{
		"harbor.harbor-system.svc": {netip.MustParseAddr("10.101.111.195")},
		"evil.harbor-system.svc":   {netip.MustParseAddr("10.101.111.196")},
		"public.example.com":       {netip.MustParseAddr("192.168.73.6")},
	}
	base := Gateway{
		AllowedHosts:           []string{"harbor.harbor-system.svc", "evil.harbor-system.svc", "public.example.com"},
		InternalServiceTargets: []string{"harbor.harbor-system.svc:443"},
		InternalServiceCIDRs:   []netip.Prefix{netip.MustParsePrefix("10.96.0.0/12")},
		DeniedCIDRs:            DefaultDeniedCIDRs(), TrustDomain: "workspace.platform.example.com", Resolver: resolver,
	}

	upstreamClient, upstreamServer := net.Pipe()
	defer upstreamServer.Close()
	allowed := base
	allowed.DialContext = func(_ context.Context, _, address string) (net.Conn, error) {
		if address != "10.101.111.195:443" {
			t.Fatalf("unexpected internal address %q", address)
		}
		return upstreamClient, nil
	}
	client, server := net.Pipe()
	request := httptest.NewRequest(http.MethodConnect, "https://egress.invalid", nil)
	request.Host = "harbor.harbor-system.svc:443"
	request.TLS = workspaceTLSState(t)
	done := make(chan struct{})
	go func() { allowed.ServeHTTP(newHijackWriter(server), request); close(done) }()
	response, err := http.ReadResponse(bufio.NewReader(client), request)
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("exact internal service response=%#v err=%v", response, err)
	}
	_ = client.Close()
	_ = upstreamServer.Close()
	<-done

	for _, target := range []string{"evil.harbor-system.svc:443", "public.example.com:443", "harbor.harbor-system.svc:8443"} {
		denied := base
		denied.DialContext = func(context.Context, string, string) (net.Conn, error) {
			t.Fatalf("denied internal target %q reached dialer", target)
			return nil, io.EOF
		}
		request := httptest.NewRequest(http.MethodConnect, "https://egress.invalid", nil)
		request.Host = target
		request.TLS = workspaceTLSState(t)
		response := httptest.NewRecorder()
		denied.ServeHTTP(response, request)
		if response.Code != http.StatusForbidden {
			t.Fatalf("target=%q status=%d", target, response.Code)
		}
	}
}

type hijackWriter struct {
	connection net.Conn
	buffer     *bufio.ReadWriter
	header     http.Header
}

func newHijackWriter(connection net.Conn) *hijackWriter {
	return &hijackWriter{connection: connection, buffer: bufio.NewReadWriter(bufio.NewReader(connection), bufio.NewWriter(connection)), header: make(http.Header)}
}

func (w *hijackWriter) Header() http.Header { return w.header }
func (w *hijackWriter) WriteHeader(int)     {}
func (w *hijackWriter) Write(value []byte) (int, error) {
	return w.buffer.Write(value)
}
func (w *hijackWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return w.connection, w.buffer, nil
}

func TestGateway_ValidatesAllowlistPatterns(t *testing.T) {
	for _, invalid := range []string{"", "127.0.0.1", "https://gitlab.com", "gitlab.com:443", "*.com", "bad host.example"} {
		gateway := Gateway{AllowedHosts: []string{invalid}, TrustDomain: "workspace.platform.example.com", Resolver: resolverFake{}, DialContext: DefaultDialContext}
		if err := gateway.Validate(); err == nil {
			t.Fatalf("invalid host pattern accepted: %q", invalid)
		}
	}
}

func TestGateway_RejectsInvalidControlPlaneTargets(t *testing.T) {
	for _, target := range []string{"", "workspace-manager.example.com", "127.0.0.1:32443", "unlisted.example.com:32443", "workspace-manager.example.com:0"} {
		gateway := Gateway{
			AllowedHosts: []string{"workspace-manager.example.com"}, ControlPlaneTargets: []string{target},
			TrustDomain: "workspace.platform.example.com", Resolver: resolverFake{}, DialContext: DefaultDialContext,
		}
		if err := gateway.Validate(); err == nil {
			t.Fatalf("invalid control-plane target accepted: %q", target)
		}
	}
}

func TestGateway_RejectsInvalidInternalServiceBoundaries(t *testing.T) {
	for _, gateway := range []Gateway{
		{AllowedHosts: []string{"harbor.example.com"}, InternalServiceTargets: []string{"harbor.example.com:443"}},
		{AllowedHosts: []string{"harbor.example.com"}, InternalServiceTargets: []string{"harbor.example.com:8443"}, InternalServiceCIDRs: []netip.Prefix{netip.MustParsePrefix("10.96.0.0/12")}},
		{AllowedHosts: []string{"harbor.example.com"}, InternalServiceTargets: []string{"harbor.example.com:443"}, InternalServiceCIDRs: []netip.Prefix{netip.MustParsePrefix("0.0.0.0/0")}},
	} {
		gateway.TrustDomain = "workspace.platform.example.com"
		gateway.Resolver = resolverFake{}
		gateway.DialContext = DefaultDialContext
		if err := gateway.Validate(); err == nil {
			t.Fatal("invalid internal service boundary accepted")
		}
	}
}
