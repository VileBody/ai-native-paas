package workspaceagent

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"net/url"
	"testing"
)

type egressDialerFake struct {
	address string
}

func mustURL(t *testing.T, value string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func (d *egressDialerFake) DialEgress(_ context.Context, _, address string) (net.Conn, error) {
	d.address = address
	client, server := net.Pipe()
	go func() {
		defer server.Close()
		_, _ = io.Copy(server, server)
	}()
	return client, nil
}

func TestLocalProxy_AllowsOnlyHTTPSConnectThroughEgressDialer(t *testing.T) {
	dialer := &egressDialerFake{}
	proxy, err := StartLocalProxy(context.Background(), "127.0.0.1:0", dialer)
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	connection, err := net.Dial("tcp", proxy.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	request, _ := http.NewRequest(http.MethodConnect, "https://gitlab.com:443", nil)
	request.Host = "gitlab.com:443"
	if err := request.Write(connection); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(connection), request)
	if err != nil || response.StatusCode != http.StatusOK || dialer.address != "gitlab.com:443" {
		t.Fatalf("status=%v address=%q err=%v", response, dialer.address, err)
	}
	if _, err := connection.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 4)
	if _, err := io.ReadFull(connection, buffer); err != nil || string(buffer) != "ping" {
		t.Fatalf("echo=%q err=%v", buffer, err)
	}
}

func TestLocalProxy_DeniesDirectHTTPAndNonTLSPort(t *testing.T) {
	proxy, err := StartLocalProxy(context.Background(), "127.0.0.1:0", &egressDialerFake{})
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(mustURL(t, proxy.URL()))}}
	response, err := client.Get("http://example.com/")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("direct HTTP status=%d", response.StatusCode)
	}
}
