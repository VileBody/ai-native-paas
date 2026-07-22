package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/keir-research/ai-native-paas/internal/platformprofile"
	"github.com/keir-research/ai-native-paas/internal/workspace/egressgateway"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	profile, err := platformprofile.Validate(os.Getenv("PLATFORM_PROFILE"), "workspace-egress-gateway",
		platformprofile.Prod("spiffe-mtls-connect-only"),
		platformprofile.Prod("dns-rebinding-and-private-range-deny"),
		platformprofile.Prod("explicit-host-allowlist"),
	)
	if err != nil || profile != platformprofile.Production {
		logger.Error("workspace-egress-gateway requires a valid production profile")
		os.Exit(1)
	}
	allowed := csv("WORKSPACE_EGRESS_ALLOWED_HOSTS")
	denied := egressgateway.DefaultDeniedCIDRs()
	for _, raw := range csv("WORKSPACE_EGRESS_DENIED_CIDRS") {
		prefix, parseErr := netip.ParsePrefix(raw)
		if parseErr != nil {
			logger.Error("invalid workspace egress denied CIDR")
			os.Exit(1)
		}
		denied = append(denied, prefix)
	}
	internalServiceCIDRs := []netip.Prefix{}
	for _, raw := range csv("WORKSPACE_EGRESS_INTERNAL_SERVICE_CIDRS") {
		prefix, parseErr := netip.ParsePrefix(raw)
		if parseErr != nil {
			logger.Error("invalid workspace egress internal service CIDR")
			os.Exit(1)
		}
		internalServiceCIDRs = append(internalServiceCIDRs, prefix)
	}
	gateway := egressgateway.Gateway{
		AllowedHosts: allowed, ControlPlaneTargets: csv("WORKSPACE_EGRESS_CONTROL_PLANE_TARGETS"),
		InternalServiceTargets: csv("WORKSPACE_EGRESS_INTERNAL_SERVICE_TARGETS"), InternalServiceCIDRs: internalServiceCIDRs,
		DeniedCIDRs: denied, TrustDomain: required("WORKSPACE_TRUST_DOMAIN"), Resolver: net.DefaultResolver, DialContext: egressgateway.DefaultDialContext,
		Log: func(message string, values ...any) { logger.Info(message, values...) },
	}
	if err := gateway.Validate(); err != nil {
		logger.Error("invalid workspace egress gateway configuration", "error", err)
		os.Exit(1)
	}
	tlsConfig, err := serverTLSConfig(required("WORKSPACE_EGRESS_SERVER_CERT_FILE"), required("WORKSPACE_EGRESS_SERVER_KEY_FILE"), required("WORKSPACE_EGRESS_CLIENT_CA_FILE"))
	if err != nil {
		logger.Error("initialize workspace egress gateway TLS", "error", err)
		os.Exit(1)
	}
	server := &http.Server{
		Addr: env("WORKSPACE_EGRESS_ADDR", ":8443"), Handler: gateway, TLSConfig: tlsConfig,
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 30 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	go func() {
		logger.Info("workspace-egress-gateway listening", "address", server.Addr)
		if err := server.ListenAndServeTLS("", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("workspace-egress-gateway stopped", "error", err)
			stop()
		}
	}()
	<-ctx.Done()
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdown); err != nil {
		logger.Error("workspace-egress-gateway shutdown failed", "error", err)
	}
}

func serverTLSConfig(certificateFile, privateKeyFile, clientCAFile string) (*tls.Config, error) {
	if certificateFile == "" || privateKeyFile == "" || clientCAFile == "" {
		return nil, errors.New("workspace egress TLS files are required")
	}
	certificate, err := tls.LoadX509KeyPair(certificateFile, privateKeyFile)
	if err != nil {
		return nil, errors.New("load workspace egress server certificate")
	}
	caRaw, err := os.ReadFile(clientCAFile)
	if err != nil || len(caRaw) == 0 {
		return nil, errors.New("load workspace egress client CA")
	}
	clientCAs := x509.NewCertPool()
	if !clientCAs.AppendCertsFromPEM(caRaw) {
		return nil, errors.New("parse workspace egress client CA")
	}
	return &tls.Config{
		MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{certificate}, ClientCAs: clientCAs,
		ClientAuth: tls.RequireAndVerifyClientCert, NextProtos: []string{"http/1.1"},
	}, nil
}

func csv(name string) []string {
	var values []string
	for _, raw := range strings.Split(os.Getenv(name), ",") {
		if value := strings.TrimSpace(raw); value != "" {
			values = append(values, value)
		}
	}
	return values
}

func required(name string) string { return strings.TrimSpace(os.Getenv(name)) }
func env(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
