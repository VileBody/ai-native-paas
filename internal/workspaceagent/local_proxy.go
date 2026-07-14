package workspaceagent

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type EgressDialer interface {
	DialEgress(context.Context, string, string) (net.Conn, error)
}

type LocalProxy struct {
	listener net.Listener
	server   *http.Server
	once     sync.Once
}

func StartLocalProxy(ctx context.Context, address string, dialer EgressDialer) (*LocalProxy, error) {
	host, _, err := net.SplitHostPort(address)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() || dialer == nil {
		return nil, errors.New("workspace local egress proxy configuration is invalid")
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, errors.New("listen on workspace local egress proxy")
	}
	proxy := &LocalProxy{listener: listener}
	proxy.server = &http.Server{
		Handler:           http.HandlerFunc(proxy.handle(dialer)),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	go func() {
		<-ctx.Done()
		_ = proxy.Close()
	}()
	go func() { _ = proxy.server.Serve(listener) }()
	return proxy, nil
}

func (p *LocalProxy) URL() string {
	if p == nil || p.listener == nil {
		return ""
	}
	return (&url.URL{Scheme: "http", Host: p.listener.Addr().String()}).String()
}

func (p *LocalProxy) Close() error {
	if p == nil {
		return nil
	}
	var err error
	p.once.Do(func() { err = p.server.Close() })
	return err
}

func (p *LocalProxy) handle(dialer EgressDialer) func(http.ResponseWriter, *http.Request) {
	return func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodConnect || request.Host == "" || request.URL.Host != "" && request.URL.Host != request.Host {
			http.Error(response, "HTTPS CONNECT required", http.StatusMethodNotAllowed)
			return
		}
		host, port, err := net.SplitHostPort(request.Host)
		if err != nil || strings.TrimSpace(host) == "" || port != "443" {
			http.Error(response, "target denied", http.StatusForbidden)
			return
		}
		upstream, err := dialer.DialEgress(request.Context(), "tcp", request.Host)
		if err != nil {
			http.Error(response, "egress denied", http.StatusBadGateway)
			return
		}
		downstream, readWriter, err := http.NewResponseController(response).Hijack()
		if err != nil {
			_ = upstream.Close()
			return
		}
		if _, err := readWriter.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil || readWriter.Flush() != nil {
			_ = downstream.Close()
			_ = upstream.Close()
			return
		}
		if buffered := readWriter.Reader.Buffered(); buffered > 0 {
			_, _ = io.CopyN(upstream, readWriter.Reader, int64(buffered))
		}
		go proxyConnections(downstream, upstream)
	}
}

func proxyConnections(downstream, upstream net.Conn) {
	defer downstream.Close()
	defer upstream.Close()
	done := make(chan struct{}, 2)
	copyOne := func(destination, source net.Conn) {
		_, _ = io.Copy(destination, source)
		if closer, ok := destination.(interface{ CloseWrite() error }); ok {
			_ = closer.CloseWrite()
		}
		done <- struct{}{}
	}
	go copyOne(upstream, downstream)
	go copyOne(downstream, upstream)
	<-done
	<-done
}
