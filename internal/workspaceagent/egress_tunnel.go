package workspaceagent

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DialEgress opens a TCP stream only through the configured mTLS CONNECT
// gateway. It deliberately never falls back to direct dialing.
func (c *Client) DialEgress(ctx context.Context, network, address string) (net.Conn, error) {
	if c == nil || c.gateway == nil || c.roots == nil || network != "tcp" && network != "tcp4" && network != "tcp6" {
		return nil, errors.New("workspace egress gateway is unavailable")
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil || strings.TrimSpace(host) == "" || port != "443" {
		return nil, errors.New("workspace egress target is invalid")
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	raw, err := dialer.DialContext(ctx, "tcp", c.gateway.Host)
	if err != nil {
		return nil, errors.New("connect workspace egress gateway")
	}
	tlsConnection := tls.Client(raw, &tls.Config{
		MinVersion: tls.VersionTLS13, RootCAs: c.roots, ServerName: c.gateway.Hostname(), NextProtos: []string{"http/1.1"},
		GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			c.mu.RLock()
			defer c.mu.RUnlock()
			copy := c.certificate
			return &copy, nil
		},
	})
	if err := tlsConnection.HandshakeContext(ctx); err != nil {
		_ = raw.Close()
		return nil, errors.New("authenticate workspace egress gateway")
	}
	request := &http.Request{
		Method: http.MethodConnect, URL: &url.URL{Opaque: address}, Host: address,
		Header: http.Header{"User-Agent": []string{"ai-native-paas-workspace-agent/v0.1"}},
	}
	if err := request.Write(tlsConnection); err != nil {
		_ = tlsConnection.Close()
		return nil, errors.New("request workspace egress tunnel")
	}
	reader := bufio.NewReaderSize(tlsConnection, 16<<10)
	response, err := http.ReadResponse(reader, request)
	if err != nil {
		_ = tlsConnection.Close()
		return nil, errors.New("read workspace egress response")
	}
	if response.StatusCode != http.StatusOK {
		if response.Body != nil {
			_ = response.Body.Close()
		}
		_ = tlsConnection.Close()
		return nil, fmt.Errorf("workspace egress gateway denied tunnel with status %d", response.StatusCode)
	}
	return &bufferedConn{Conn: tlsConnection, reader: reader}, nil
}

type bufferedConn struct {
	net.Conn
	reader io.Reader
}

func (c *bufferedConn) Read(buffer []byte) (int, error) { return c.reader.Read(buffer) }
