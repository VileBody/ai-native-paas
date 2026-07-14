// Package egressgateway implements the only network exit available to a
// disposable workspace VM.
package egressgateway

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"sort"
	"strings"
	"time"
)

type Resolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}

type Gateway struct {
	AllowedHosts []string
	DeniedCIDRs  []netip.Prefix
	TrustDomain  string
	Resolver     Resolver
	DialContext  func(context.Context, string, string) (net.Conn, error)
	Log          func(string, ...any)
}

func (g Gateway) Validate() error {
	if len(g.AllowedHosts) == 0 || g.Resolver == nil || g.DialContext == nil || strings.TrimSpace(g.TrustDomain) == "" || strings.ContainsAny(g.TrustDomain, "/:@ \\") {
		return errors.New("workspace egress gateway dependencies are unavailable")
	}
	for _, pattern := range g.AllowedHosts {
		if !validHostPattern(pattern) {
			return errors.New("workspace egress allowlist is invalid")
		}
	}
	for _, prefix := range g.DeniedCIDRs {
		if !prefix.IsValid() {
			return errors.New("workspace egress denied CIDR is invalid")
		}
	}
	return nil
}

func (g Gateway) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	if g.Validate() != nil {
		http.Error(response, "unavailable", http.StatusServiceUnavailable)
		return
	}
	identity, err := workspaceIdentity(request.TLS, g.TrustDomain)
	if err != nil {
		http.Error(response, "mTLS identity required", http.StatusUnauthorized)
		return
	}
	if request.Method != http.MethodConnect {
		response.Header().Set("Allow", http.MethodConnect)
		http.Error(response, "CONNECT required", http.StatusMethodNotAllowed)
		return
	}
	host, port, err := net.SplitHostPort(request.Host)
	if err != nil || port != "443" {
		http.Error(response, "target denied", http.StatusForbidden)
		return
	}
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	if net.ParseIP(host) != nil || !g.allowed(host) {
		http.Error(response, "target denied", http.StatusForbidden)
		return
	}
	addresses, err := g.Resolver.LookupNetIP(request.Context(), "ip", host)
	if err != nil || len(addresses) == 0 || g.hasDeniedAddress(addresses) {
		http.Error(response, "target resolution denied", http.StatusForbidden)
		return
	}
	sort.Slice(addresses, func(i, j int) bool { return addresses[i].Compare(addresses[j]) < 0 })
	var upstream net.Conn
	for _, address := range addresses {
		upstream, err = g.DialContext(request.Context(), "tcp", net.JoinHostPort(address.String(), port))
		if err == nil {
			break
		}
	}
	if upstream == nil {
		http.Error(response, "target unavailable", http.StatusBadGateway)
		return
	}
	downstream, buffered, err := http.NewResponseController(response).Hijack()
	if err != nil {
		_ = upstream.Close()
		return
	}
	if _, err := buffered.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil || buffered.Flush() != nil {
		_ = downstream.Close()
		_ = upstream.Close()
		return
	}
	if g.Log != nil {
		g.Log("workspace egress tunnel established", "identity", identity, "target_host", host)
	}
	go bridge(downstream, upstream)
}

func (g Gateway) allowed(host string) bool {
	for _, raw := range g.AllowedHosts {
		pattern := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(raw), "."))
		if host == pattern || strings.HasPrefix(pattern, "*.") && strings.HasSuffix(host, pattern[1:]) && host != pattern[2:] {
			return true
		}
	}
	return false
}

func (g Gateway) hasDeniedAddress(addresses []netip.Addr) bool {
	for _, address := range addresses {
		if !address.IsValid() || address.IsPrivate() || address.IsLoopback() || address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() || address.IsMulticast() || address.IsUnspecified() {
			return true
		}
		for _, prefix := range g.DeniedCIDRs {
			if prefix.Contains(address) {
				return true
			}
		}
	}
	return false
}

func validHostPattern(value string) bool {
	value = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(value), "."))
	if strings.HasPrefix(value, "*.") {
		value = value[2:]
	}
	if value == "" || net.ParseIP(value) != nil || strings.ContainsAny(value, "/:@ \\") {
		return false
	}
	parsed, err := url.Parse("https://" + value)
	return err == nil && parsed.Hostname() == value && strings.Contains(value, ".")
}

func workspaceIdentity(state *tls.ConnectionState, trustDomain string) (string, error) {
	if state == nil || len(state.VerifiedChains) == 0 || len(state.PeerCertificates) == 0 {
		return "", errors.New("verified client certificate required")
	}
	leaf := state.PeerCertificates[0]
	if len(leaf.URIs) != 1 || leaf.URIs[0].Scheme != "spiffe" || leaf.URIs[0].Host != trustDomain {
		return "", errors.New("workspace SPIFFE identity required")
	}
	segments := strings.Split(strings.Trim(leaf.URIs[0].Path, "/"), "/")
	if len(segments) != 10 || segments[0] != "tenant" || segments[2] != "project" || segments[4] != "workspace" || segments[6] != "task" || segments[8] != "agent" {
		return "", errors.New("workspace SPIFFE identity required")
	}
	for index := 1; index < len(segments); index += 2 {
		if segments[index] == "" || strings.ContainsAny(segments[index], " \\:@") {
			return "", errors.New("workspace SPIFFE identity required")
		}
	}
	return leaf.URIs[0].String(), nil
}

func bridge(left, right net.Conn) {
	defer left.Close()
	defer right.Close()
	done := make(chan struct{}, 2)
	copyOne := func(destination, source net.Conn) {
		_, _ = io.Copy(destination, source)
		if closer, ok := destination.(interface{ CloseWrite() error }); ok {
			_ = closer.CloseWrite()
		}
		done <- struct{}{}
	}
	go copyOne(left, right)
	go copyOne(right, left)
	<-done
	<-done
}

func DefaultDeniedCIDRs() []netip.Prefix {
	values := []string{
		"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12", "192.0.0.0/24", "192.168.0.0/16", "198.18.0.0/15", "224.0.0.0/4", "240.0.0.0/4",
		"::/128", "::1/128", "fc00::/7", "fe80::/10", "ff00::/8",
	}
	result := make([]netip.Prefix, 0, len(values))
	for _, value := range values {
		result = append(result, netip.MustParsePrefix(value))
	}
	return result
}

func DefaultDialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext(ctx, network, address)
}
