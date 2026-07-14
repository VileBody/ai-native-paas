package multiarch_test

import (
	"context"
	"strings"
	"testing"

	"github.com/keir-research/ai-native-paas/internal/build/application"
	"github.com/keir-research/ai-native-paas/internal/build/domain"
	"github.com/keir-research/ai-native-paas/internal/build/multiarch"
	"github.com/keir-research/ai-native-paas/internal/build/registry"
)

func platform(platform, character string, verified bool) multiarch.PlatformResult {
	return multiarch.PlatformResult{Platform: platform, Digest: "sha256:" + strings.Repeat(character, 64), MediaType: multiarch.ManifestMediaType, SizeBytes: 1024, Verified: verified}
}

func TestBuild_MultiArchManifestContainsOnlyVerifiedPlatformDigests(t *testing.T) {
	requested := []string{"linux/amd64", "linux/arm64"}
	results := []multiarch.PlatformResult{platform("linux/amd64", "a", true), platform("linux/arm64", "b", false)}
	if _, err := multiarch.Assemble(requested, results, false); !domain.HasCode(err, domain.CodePolicyRejected) {
		t.Fatalf("strict production policy err=%v", err)
	}
	partial, err := multiarch.Assemble(requested, results, true)
	if err != nil || !partial.Partial || partial.ProductionAllowed || len(partial.Descriptors) != 1 || partial.Descriptors[0].Digest != results[0].Digest {
		t.Fatalf("partial=%+v err=%v", partial, err)
	}

	complete, err := multiarch.Assemble(requested, []multiarch.PlatformResult{platform("linux/arm64", "b", true), platform("linux/amd64", "a", true)}, false)
	if err != nil || complete.Partial || !complete.ProductionAllowed || len(complete.Descriptors) != 2 {
		t.Fatalf("complete=%+v err=%v", complete, err)
	}
	layout := t.TempDir()
	if err := multiarch.WriteOCILayout(layout, complete); err != nil {
		t.Fatal(err)
	}
	localRegistry := registry.NewLocal(t.TempDir())
	repository := "registry.test/tenants/t1/apps/p1"
	published, err := localRegistry.Publish(context.Background(), "t1", repository, application.BuildOutput{OCILayoutPath: layout, ManifestDigest: complete.Digest, MediaType: complete.MediaType})
	if err != nil || published.Digest != complete.Digest || published.MediaType != multiarch.IndexMediaType {
		t.Fatalf("published=%+v err=%v", published, err)
	}
	resolved, err := localRegistry.Resolve(context.Background(), "t1", repository+"@"+complete.Digest)
	if err != nil || resolved != published {
		t.Fatalf("resolved=%+v err=%v", resolved, err)
	}
}
