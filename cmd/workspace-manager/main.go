package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	agentsupport "github.com/keir-research/ai-native-paas/internal/agent/support"
	"github.com/keir-research/ai-native-paas/internal/platformprofile"
	"github.com/keir-research/ai-native-paas/internal/postgresbootstrap"
	"github.com/keir-research/ai-native-paas/internal/workspace"
	"github.com/keir-research/ai-native-paas/internal/workspace/bootstrap"
	workspaceopenbao "github.com/keir-research/ai-native-paas/internal/workspace/openbao"
	workspacepostgres "github.com/keir-research/ai-native-paas/internal/workspace/postgres"
	"github.com/keir-research/ai-native-paas/internal/workspace/session"
	sessionhttp "github.com/keir-research/ai-native-paas/internal/workspace/session/httpapi"
	sessionpostgres "github.com/keir-research/ai-native-paas/internal/workspace/session/postgres"
	workspacetimeweb "github.com/keir-research/ai-native-paas/internal/workspace/timeweb"
)

type managerIDs struct{ inner *agentsupport.IDs }

func (i managerIDs) New(prefix string) string { return i.inner.NewID(prefix) }

type config struct {
	Address               string
	DatabaseURL           string
	ServerCertificateFile string
	ServerPrivateKeyFile  string
	ClientCAFile          string
	TrustDomain           string
	OpenBaoAddress        string
	OpenBaoTokenFile      string
	OpenBaoPKIMount       string
	OpenBaoRole           string
	OpenBaoTTL            time.Duration
	TimewebAPIURL         string
	TimewebTokenFile      string
	TimewebProjectID      int64
	TimewebConfiguratorID int64
	TimewebZone           string
	TimewebBandwidthMbps  int64
	TimewebSystemDiskMiB  int64
	WorkspaceVPCID        string
	WorkspaceImages       map[string]string
	ControlPlaneURL       string
	AllowedEgressHosts    []string
	EgressGatewayCIDRs    []string
	DNSResolverCIDRs      []string
	DeniedCIDRs           []string
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	profile, err := platformprofile.Validate(os.Getenv("PLATFORM_PROFILE"), "workspace-manager",
		platformprofile.Prod("workspace-postgres-store"),
		platformprofile.Prod("workspace-agent-postgres-sessions"),
		platformprofile.Prod("timeweb-workspace-provider"),
		platformprofile.Prod("openbao-pki-and-lease-revocation"),
		platformprofile.Prod("verified-spiffe-mtls"),
	)
	if err != nil || profile != platformprofile.Production {
		logger.Error("workspace-manager requires a valid production profile")
		os.Exit(1)
	}
	settings, err := loadConfig()
	if err != nil {
		logger.Error("invalid workspace-manager configuration", "error", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	db, err := postgresbootstrap.Open(ctx, settings.DatabaseURL)
	if err != nil {
		logger.Error("connect workspace PostgreSQL", "error", err)
		os.Exit(1)
	}
	defer db.Close()
	store := &workspacepostgres.Store{DB: db}
	if err := postgresbootstrap.WithMigrationLock(ctx, db, "workspace", store.Migrate); err != nil {
		logger.Error("migrate workspace PostgreSQL", "error", err)
		os.Exit(1)
	}

	clock := agentsupport.Clock{}
	ids := managerIDs{inner: &agentsupport.IDs{}}
	issuer, err := workspaceopenbao.NewIssuer(workspaceopenbao.Config{
		Address: settings.OpenBaoAddress, TokenFile: settings.OpenBaoTokenFile, PKIMount: settings.OpenBaoPKIMount,
		Role: settings.OpenBaoRole, TrustDomain: settings.TrustDomain, TTL: settings.OpenBaoTTL, Clock: clock,
	})
	if err != nil {
		logger.Error("initialize OpenBao workspace issuer", "error", err)
		os.Exit(1)
	}
	renderer := bootstrap.CloudInitRenderer{Issuer: issuer, ControlPlaneURL: settings.ControlPlaneURL}
	timewebToken, err := readSecureFile(settings.TimewebTokenFile, 16<<10)
	if err != nil {
		logger.Error("load Timeweb workspace credential", "error", err)
		os.Exit(1)
	}
	defer clear(timewebToken)
	provider, err := workspacetimeweb.New(workspacetimeweb.Config{
		BaseURL: settings.TimewebAPIURL, Token: string(timewebToken), ProjectID: settings.TimewebProjectID,
		ConfiguratorID: settings.TimewebConfiguratorID, AvailabilityZone: settings.TimewebZone,
		BandwidthMbps: settings.TimewebBandwidthMbps, SystemDiskMiB: settings.TimewebSystemDiskMiB,
		ImageIDs: settings.WorkspaceImages, EgressGatewayCIDRs: settings.EgressGatewayCIDRs,
		DNSResolverCIDRs: settings.DNSResolverCIDRs, RenderCloudInit: renderer.Render,
	})
	if err != nil {
		logger.Error("initialize Timeweb workspace provider", "error", err)
		os.Exit(1)
	}

	sessions := &session.Registry{Store: &sessionpostgres.Store{DB: db}, Clock: clock, IDs: ids}
	workerID := ids.New("workspace-manager")
	service := &workspace.Service{
		Store: store, Provider: provider, Sessions: sessions, Leases: issuer, Clock: clock, IDs: ids,
		Policy: workspace.DefaultCommandPolicy(), ReconcilerID: workerID, WorkspaceVPCID: settings.WorkspaceVPCID,
		AllowedEgressHosts: settings.AllowedEgressHosts, EgressGatewayCIDRs: settings.EgressGatewayCIDRs,
		DNSResolverCIDRs: settings.DNSResolverCIDRs, DeniedCIDRs: settings.DeniedCIDRs,
	}
	outbox := &workspace.Worker{Store: store, Actions: service, Clock: clock, WorkerID: workerID, Lease: 10 * time.Minute, RetryDelay: 5 * time.Second}
	go runControllers(ctx, logger, outbox, service)

	tlsConfig, err := serverTLSConfig(settings)
	if err != nil {
		logger.Error("initialize workspace mTLS", "error", err)
		os.Exit(1)
	}
	agentHandler := sessionhttp.Handler{
		Registry: sessions, Workspaces: service, Bindings: service, Certificates: issuer,
		Principals: sessionhttp.SPIFFEResolver{TrustDomain: settings.TrustDomain}, MaxBodyBytes: 64 << 10,
	}
	handler := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet && request.URL.Path == "/healthz" {
			healthCtx, cancel := context.WithTimeout(request.Context(), 2*time.Second)
			defer cancel()
			if err := db.PingContext(healthCtx); err != nil {
				http.Error(response, "unavailable", http.StatusServiceUnavailable)
				return
			}
			response.WriteHeader(http.StatusNoContent)
			return
		}
		agentHandler.ServeHTTP(response, request)
	})
	server := &http.Server{
		Addr: settings.Address, Handler: handler, TLSConfig: tlsConfig,
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 35 * time.Second, IdleTimeout: 60 * time.Second,
	}
	go func() {
		logger.Info("workspace-manager listening", "address", settings.Address)
		if err := server.ListenAndServeTLS("", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("workspace-manager server failed", "error", err)
			stop()
		}
	}()
	<-ctx.Done()
	shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdown); err != nil {
		logger.Error("workspace-manager shutdown failed", "error", err)
	}
}

func runControllers(ctx context.Context, logger *slog.Logger, outbox *workspace.Worker, service *workspace.Service) {
	outboxTicker := time.NewTicker(time.Second)
	maintenanceTicker := time.NewTicker(5 * time.Second)
	defer outboxTicker.Stop()
	defer maintenanceTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-outboxTicker.C:
			if _, err := outbox.RunOnce(ctx, 32); err != nil && !errors.Is(err, context.Canceled) {
				logger.Error("workspace outbox iteration failed", "error", err)
			}
		case <-maintenanceTicker.C:
			if _, err := service.Expire(ctx, 32); err != nil && !errors.Is(err, context.Canceled) {
				logger.Error("workspace expiry iteration failed", "error", err)
			}
			if _, err := service.EnforceTimeouts(ctx, 32); err != nil && !errors.Is(err, context.Canceled) {
				logger.Error("workspace timeout iteration failed", "error", err)
			}
		}
	}
}

func serverTLSConfig(settings config) (*tls.Config, error) {
	certificate, err := tls.LoadX509KeyPair(settings.ServerCertificateFile, settings.ServerPrivateKeyFile)
	if err != nil {
		return nil, errors.New("load workspace server certificate")
	}
	ca, err := os.ReadFile(settings.ClientCAFile)
	if err != nil || len(ca) == 0 {
		return nil, errors.New("load workspace client CA")
	}
	clientCAs := x509.NewCertPool()
	if !clientCAs.AppendCertsFromPEM(ca) {
		return nil, errors.New("parse workspace client CA")
	}
	return &tls.Config{
		MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{certificate}, ClientCAs: clientCAs,
		ClientAuth: tls.VerifyClientCertIfGiven,
	}, nil
}

func loadConfig() (config, error) {
	settings := config{
		Address: env("WORKSPACE_MANAGER_ADDR", ":8443"), DatabaseURL: required("DATABASE_URL"),
		ServerCertificateFile: required("WORKSPACE_SERVER_CERT_FILE"), ServerPrivateKeyFile: required("WORKSPACE_SERVER_KEY_FILE"),
		ClientCAFile: required("WORKSPACE_CLIENT_CA_FILE"), TrustDomain: required("WORKSPACE_TRUST_DOMAIN"),
		OpenBaoAddress: required("OPENBAO_ADDR"), OpenBaoTokenFile: required("OPENBAO_TOKEN_FILE"),
		OpenBaoPKIMount: env("OPENBAO_WORKSPACE_PKI_MOUNT", "workspace-pki"), OpenBaoRole: env("OPENBAO_WORKSPACE_PKI_ROLE", "workspace-agent"),
		TimewebAPIURL: env("TIMEWEB_API_URL", "https://api.timeweb.cloud/api/v1"), TimewebTokenFile: required("TIMEWEB_TOKEN_FILE"),
		TimewebZone: env("TIMEWEB_AVAILABILITY_ZONE", "msk-1"), WorkspaceVPCID: required("WORKSPACE_VPC_ID"),
		ControlPlaneURL: required("WORKSPACE_AGENT_PUBLIC_URL"), AllowedEgressHosts: csv("WORKSPACE_ALLOWED_EGRESS_HOSTS"),
		EgressGatewayCIDRs: csv("WORKSPACE_EGRESS_GATEWAY_CIDRS"), DNSResolverCIDRs: csv("WORKSPACE_DNS_RESOLVER_CIDRS"),
		DeniedCIDRs: csv("WORKSPACE_DENIED_CIDRS"),
	}
	var err error
	if settings.OpenBaoTTL, err = duration("OPENBAO_WORKSPACE_CERT_TTL", 15*time.Minute); err != nil {
		return config{}, err
	}
	if settings.TimewebProjectID, err = integer("TIMEWEB_PROJECT_ID", 0); err != nil || settings.TimewebProjectID <= 0 {
		return config{}, errors.New("TIMEWEB_PROJECT_ID must be a positive integer")
	}
	if settings.TimewebConfiguratorID, err = integer("TIMEWEB_WORKSPACE_CONFIGURATOR_ID", 0); err != nil || settings.TimewebConfiguratorID <= 0 {
		return config{}, errors.New("TIMEWEB_WORKSPACE_CONFIGURATOR_ID must be a positive integer")
	}
	if settings.TimewebBandwidthMbps, err = integer("TIMEWEB_WORKSPACE_BANDWIDTH_MBPS", 100); err != nil || settings.TimewebBandwidthMbps <= 0 {
		return config{}, errors.New("TIMEWEB_WORKSPACE_BANDWIDTH_MBPS must be a positive integer")
	}
	if settings.TimewebSystemDiskMiB, err = integer("TIMEWEB_WORKSPACE_SYSTEM_DISK_MIB", 40960); err != nil || settings.TimewebSystemDiskMiB < 10240 {
		return config{}, errors.New("TIMEWEB_WORKSPACE_SYSTEM_DISK_MIB is invalid")
	}
	if err := json.Unmarshal([]byte(required("WORKSPACE_IMAGE_MAP_JSON")), &settings.WorkspaceImages); err != nil || len(settings.WorkspaceImages) == 0 {
		return config{}, errors.New("WORKSPACE_IMAGE_MAP_JSON is invalid")
	}
	for name, value := range map[string]string{
		"DATABASE_URL": settings.DatabaseURL, "WORKSPACE_SERVER_CERT_FILE": settings.ServerCertificateFile,
		"WORKSPACE_SERVER_KEY_FILE": settings.ServerPrivateKeyFile, "WORKSPACE_CLIENT_CA_FILE": settings.ClientCAFile,
		"WORKSPACE_TRUST_DOMAIN": settings.TrustDomain, "OPENBAO_ADDR": settings.OpenBaoAddress,
		"OPENBAO_TOKEN_FILE": settings.OpenBaoTokenFile, "TIMEWEB_TOKEN_FILE": settings.TimewebTokenFile,
		"WORKSPACE_VPC_ID": settings.WorkspaceVPCID, "WORKSPACE_AGENT_PUBLIC_URL": settings.ControlPlaneURL,
	} {
		if strings.TrimSpace(value) == "" {
			return config{}, fmt.Errorf("%s is required", name)
		}
	}
	for name, values := range map[string][]string{
		"WORKSPACE_ALLOWED_EGRESS_HOSTS": settings.AllowedEgressHosts, "WORKSPACE_EGRESS_GATEWAY_CIDRS": settings.EgressGatewayCIDRs,
		"WORKSPACE_DNS_RESOLVER_CIDRS": settings.DNSResolverCIDRs, "WORKSPACE_DENIED_CIDRS": settings.DeniedCIDRs,
	} {
		if len(values) == 0 {
			return config{}, fmt.Errorf("%s is required", name)
		}
	}
	return settings, nil
}

func readSecureFile(filename string, maximum int64) ([]byte, error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, errors.New("open credential file")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() < 16 || info.Size() > maximum {
		return nil, errors.New("credential file permissions or size are invalid")
	}
	raw, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil || int64(len(raw)) > maximum {
		return nil, errors.New("read credential file")
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) < 16 {
		return nil, errors.New("credential file is invalid")
	}
	return raw, nil
}

func required(name string) string { return strings.TrimSpace(os.Getenv(name)) }
func env(name, fallback string) string {
	if value := required(name); value != "" {
		return value
	}
	return fallback
}
func csv(name string) []string {
	var result []string
	for _, value := range strings.Split(required(name), ",") {
		if value = strings.TrimSpace(value); value != "" {
			result = append(result, value)
		}
	}
	return result
}
func integer(name string, fallback int64) (int64, error) {
	value := required(name)
	if value == "" {
		return fallback, nil
	}
	return strconv.ParseInt(value, 10, 64)
}
func duration(name string, fallback time.Duration) (time.Duration, error) {
	value := required(name)
	if value == "" {
		return fallback, nil
	}
	return time.ParseDuration(value)
}
func clear(value []byte) {
	for index := range value {
		value[index] = 0
	}
}
