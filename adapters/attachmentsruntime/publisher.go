// Package attachmentsruntime adapts attachment snapshot publications to the
// Runtime application service without coupling either bounded context to the
// other's internals.
package attachmentsruntime

import (
	"context"
	"strconv"
	"strings"

	attachmentsapp "github.com/keir-research/ai-native-paas/internal/attachments/application"
	attachmentsdomain "github.com/keir-research/ai-native-paas/internal/attachments/domain"
	runtimeapp "github.com/keir-research/ai-native-paas/internal/runtime/application"
	runtimedomain "github.com/keir-research/ai-native-paas/internal/runtime/domain"
	attachmentsv1 "github.com/keir-research/ai-native-paas/pkg/contracts/attachments/v1"
	runtimev1 "github.com/keir-research/ai-native-paas/pkg/contracts/runtime/v1"
)

type Publisher struct {
	Runtime *runtimeapp.Service
	ActorID string
}

var _ attachmentsapp.RuntimeSnapshotPublisher = (*Publisher)(nil)

func (p *Publisher) Publish(ctx context.Context, snapshot attachmentsv1.AttachmentSnapshot) error {
	if p == nil || p.Runtime == nil || p.Runtime.Store == nil {
		return attachmentsdomain.NewError(attachmentsdomain.CodeUnavailable, "runtime release service unavailable")
	}
	if err := snapshot.Validate(); err != nil {
		return attachmentsdomain.NewError(attachmentsdomain.CodeInvalidArgument, "invalid attachment snapshot")
	}
	var active runtimedomain.Release
	hasActive := false
	err := p.Runtime.Store.Transact(ctx, func(tx runtimeapp.Tx) error {
		environment, ok := tx.GetEnvironment(snapshot.EnvironmentID)
		if !ok || environment.TenantID != snapshot.TenantID || environment.ApplicationID != snapshot.ApplicationID {
			return attachmentsdomain.NewError(attachmentsdomain.CodeForbidden, "runtime environment does not match attachment snapshot")
		}
		if strings.TrimSpace(environment.ActiveReleaseID) == "" {
			return nil
		}
		active, ok = tx.GetRelease(environment.ActiveReleaseID)
		if !ok || active.TenantID != snapshot.TenantID || active.ApplicationID != snapshot.ApplicationID {
			return attachmentsdomain.NewError(attachmentsdomain.CodeInternal, "active runtime release is inconsistent")
		}
		hasActive = true
		return nil
	})
	if err != nil || !hasActive {
		return err
	}

	config := active.Configuration
	config.AttachmentSnapshotRef = snapshot.SnapshotID + ":v" + strconv.FormatInt(snapshot.Version, 10)
	actor := strings.TrimSpace(p.ActorID)
	if actor == "" {
		actor = "attachments-controller"
	}
	_, err = p.Runtime.Deploy(ctx, runtimev1.DeployRequest{
		TenantID: snapshot.TenantID, ApplicationID: snapshot.ApplicationID, EnvironmentID: snapshot.EnvironmentID,
		Artifact: active.Artifact, Configuration: config,
		IdempotencyKey: "attachments-snapshot-" + snapshot.SnapshotID, ActorID: actor,
	})
	return err
}
