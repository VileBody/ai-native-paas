package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/keir-research/ai-native-paas/internal/platformprofile"
	"github.com/keir-research/ai-native-paas/internal/workspace"
	"github.com/keir-research/ai-native-paas/internal/workspaceagent"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if os.Getenv("WORKSPACE_AGENT_GIT_ASKPASS") == "1" {
		if workspaceagent.ExecuteGitAskpass(os.Args[1:]) != nil {
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 {
		var operationErr error
		switch os.Args[1] {
		case "verified-git-apply-patch":
			operationErr = workspaceagent.ExecuteVerifiedGitApplyPatch(os.Args[2:])
		case "verified-git-checkout":
			operationErr = workspaceagent.ExecuteVerifiedGitCheckout(os.Args[2:])
		case "verified-git-commit":
			operationErr = workspaceagent.ExecuteVerifiedGitCommit(os.Args[2:])
		case "verified-git-push":
			operationErr = workspaceagent.ExecuteVerifiedGitPush(os.Args[2:])
		case "verified-tofu-apply":
			operationErr = workspaceagent.ExecuteVerifiedTofuApply(os.Args[2:])
		case "verified-tofu-plan":
			operationErr = workspaceagent.ExecuteVerifiedTofuPlan(os.Args[2:])
		default:
			logger.Error("unknown workspace-agent subcommand")
			os.Exit(2)
		}
		if operationErr != nil {
			logger.Error("verified workspace operation denied", "error", operationErr)
			os.Exit(1)
		}
		return
	}
	profile, err := platformprofile.Validate(os.Getenv("PLATFORM_PROFILE"), "workspace-agent",
		platformprofile.Prod("outbound-spiffe-mtls"),
		platformprofile.Prod("durable-command-journal"),
		platformprofile.Prod("process-group-executor"),
		platformprofile.Prod("streaming-secret-redaction"),
		platformprofile.Prod("remote-command-credential-broker"),
		platformprofile.Prod("durable-remote-redacted-output"),
	)
	if err != nil || profile != platformprofile.Production {
		logger.Error("workspace-agent requires a valid production profile")
		os.Exit(1)
	}
	configFile := os.Getenv("WORKSPACE_AGENT_CONFIG_FILE")
	if configFile == "" {
		configFile = "/var/lib/ai-native-paas/identity/workspace-agent.json"
	}
	config, err := workspaceagent.LoadConfig(configFile)
	if err != nil {
		logger.Error("load workspace-agent configuration", "error", err)
		os.Exit(1)
	}
	client, err := workspaceagent.NewClient(config)
	if err != nil {
		logger.Error("initialize workspace-agent mTLS client", "error", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	proxy, err := workspaceagent.StartLocalProxy(ctx, "127.0.0.1:18081", client)
	if err != nil {
		logger.Error("initialize workspace local egress proxy", "error", err)
		os.Exit(1)
	}
	defer proxy.Close()
	journal, err := workspaceagent.NewJournal(config.JournalDirectory, config.WorkspaceID, time.Now)
	if err != nil {
		logger.Error("initialize workspace-agent journal", "error", err)
		os.Exit(1)
	}
	agent := &workspaceagent.Agent{
		WorkspaceID: config.WorkspaceID, Control: client, Journal: journal,
		Resolver: workspaceagent.RemoteEnvironmentResolver{Control: client, Now: time.Now},
		Executor: workspaceagent.Executor{WorkspaceRoot: config.WorkspaceRoot, Policy: workspace.DefaultCommandPolicy(), Now: time.Now, RequireCgroup: true},
		Outputs:  workspaceagent.FileOutputSink{Directory: filepath.Join(config.JournalDirectory, "output")},
		Policy:   workspace.DefaultCommandPolicy(), Now: time.Now,
		SystemEnvironment: map[string]string{"HTTP_PROXY": proxy.URL(), "HTTPS_PROXY": proxy.URL(), "NO_PROXY": "127.0.0.1,localhost"},
		Log:               func(message string, values ...any) { logger.Info(message, values...) },
	}
	if err := agent.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		logger.Error("workspace-agent stopped", "error", err)
		os.Exit(1)
	}
}
