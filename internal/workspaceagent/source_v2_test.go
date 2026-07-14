package workspaceagent

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sourcev2 "github.com/keir-research/ai-native-paas/pkg/contracts/source/v2"
)

func TestSource_CheckoutUsesExactCommitNotMutableBranchHead(t *testing.T) {
	remote, author := sourceRepository(t)
	writeSourceFile(t, author, "version.txt", "A\n")
	runSourceGit(t, author, "add", "version.txt")
	runSourceGit(t, author, "commit", "-m", "A")
	base := sourceGitOutput(t, author, "rev-parse", "HEAD")
	runSourceGit(t, author, "push", "origin", "HEAD:main")
	writeSourceFile(t, author, "version.txt", "B\n")
	runSourceGit(t, author, "commit", "-am", "B")
	runSourceGit(t, author, "push", "origin", "HEAD:main")

	checkout := t.TempDir()
	withSourceDirectory(t, checkout, func() {
		if err := checkoutExactRevision(remote, base, "agent/task-1"); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile("version.txt")
		if err != nil || string(raw) != "A\n" {
			t.Fatalf("exact checkout content=%q err=%v", raw, err)
		}
	})
}

func TestSource_PatchRejectsNonCanonicalAndGitInternalPaths(t *testing.T) {
	_, repository := sourceRepositoryWithCommit(t)
	configureSourceWorkspace(t, repository, false)
	base := sourceGitOutput(t, repository, "rev-parse", "HEAD")
	valid := encodedMutation(t, sourcev2.PatchMutation{Path: "safe.txt", ContentBase64: base64.StdEncoding.EncodeToString([]byte("safe"))})
	invalid := encodedMutation(t, sourcev2.PatchMutation{Path: "../escape", ContentBase64: base64.StdEncoding.EncodeToString([]byte("bad"))})
	withSourceDirectory(t, repository, func() {
		err := ExecuteVerifiedGitApplyPatch([]string{base, valid, invalid})
		if err == nil {
			t.Fatal("non-canonical patch path was accepted")
		}
		if _, statErr := os.Stat("safe.txt"); !os.IsNotExist(statErr) {
			t.Fatalf("partial patch mutation remains: %v", statErr)
		}
	})

	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(repository, "escape")); err != nil {
		t.Fatal(err)
	}
	symlink := encodedMutation(t, sourcev2.PatchMutation{Path: "escape/file.txt", ContentBase64: base64.StdEncoding.EncodeToString([]byte("bad"))})
	withSourceDirectory(t, repository, func() {
		if err := ExecuteVerifiedGitApplyPatch([]string{base, symlink}); err == nil {
			t.Fatal("symlink escape was accepted")
		}
	})
	if _, err := os.Stat(filepath.Join(outside, "file.txt")); !os.IsNotExist(err) {
		t.Fatalf("symlink escape mutated outside workspace: %v", err)
	}
}

func TestSource_AgentCommitContainsSignedAttestationAndCorrelation(t *testing.T) {
	_, repository := sourceRepositoryWithCommit(t)
	configureSourceWorkspace(t, repository, true)
	base := sourceGitOutput(t, repository, "rev-parse", "HEAD")
	writeSourceFile(t, repository, "change.txt", "governed\n")
	runSourceGit(t, repository, "add", "change.txt")
	contentDigest := sha256.Sum256([]byte("governed\n"))
	changeSet, _ := json.Marshal(sourcev2.ChangeSet{
		RepositoryID: "repo-1", BaseSHA: base, TargetBranch: "agent/task-1",
		Files: []sourcev2.PatchFile{{Path: "change.txt", ContentHash: "sha256:" + hex.EncodeToString(contentDigest[:])}},
	})
	planDigest := sha256.Sum256(changeSet)
	planHash := "sha256:" + hex.EncodeToString(planDigest[:])
	message := "governed change\n\nTask-ID: task-1\nOperation-ID: corr-1\nActor-ID: agent-1\nRepository-ID: repo-1\nSource-Plan-Hash: " + planHash
	runSourceGit(t, repository, "commit", "-m", message)
	commit := sourceGitOutput(t, repository, "rev-parse", "HEAD")
	withSourceDirectory(t, repository, func() {
		recovered, existing, err := exactCommitState(base, "repo-1", "agent/task-1", "agent-1", "task-1", "corr-1", planHash)
		if err != nil || !existing || recovered != commit {
			t.Fatalf("governed commit recovery=%q existing=%v err=%v", recovered, existing, err)
		}
		receipt, err := signCommitReceipt("command-1", "session-1", []string{"repo-1", base, "agent/task-1", "agent-1", "task-1", "corr-1", planHash, "governed change"}, commit)
		if err != nil || receipt.Validate() != nil {
			t.Fatalf("receipt=%#v err=%v", receipt, err)
		}
		certificate := sourceCertificate(t, os.Getenv("WORKSPACE_AGENT_CONFIG_FILE"))
		statement, _ := receipt.Statement.Canonical()
		digest := sha256.Sum256(statement)
		signature, _ := base64.StdEncoding.DecodeString(receipt.Signature)
		publicKey, ok := certificate.PublicKey.(*ecdsa.PublicKey)
		if !ok || !ecdsa.VerifyASN1(publicKey, digest[:], signature) {
			t.Fatal("commit attestation signature does not verify")
		}
	})
}

func TestSource_ConcurrentPushUsesExpectedBaseSHA(t *testing.T) {
	remote, author := sourceRepositoryWithCommit(t)
	base := sourceGitOutput(t, author, "rev-parse", "HEAD")
	first := checkoutSourceCopy(t, remote, base)
	second := checkoutSourceCopy(t, remote, base)
	writeSourceFile(t, first, "first.txt", "first")
	runSourceGit(t, first, "add", "first.txt")
	runSourceGit(t, first, "commit", "-m", "first")
	withSourceDirectory(t, first, func() {
		if err := pushWithLease("main", base); err != nil {
			t.Fatal(err)
		}
	})
	writeSourceFile(t, second, "second.txt", "second")
	runSourceGit(t, second, "add", "second.txt")
	runSourceGit(t, second, "commit", "-m", "second")
	withSourceDirectory(t, second, func() {
		if err := pushWithLease("main", base); err == nil {
			t.Fatal("stale expected-base push succeeded")
		}
	})
}

func TestSource_SecretScannerBlocksCredentialBeforeCommit(t *testing.T) {
	_, repository := sourceRepositoryWithCommit(t)
	base := sourceGitOutput(t, repository, "rev-parse", "HEAD")
	sentinel := "provider-key-sentinel-DO-NOT-LEAK"
	writeSourceFile(t, repository, "config.env", "API_KEY="+sentinel+"\n")
	paths, err := withChangedPaths(t, repository)
	if err != nil {
		t.Fatal(err)
	}
	var rule, path string
	var blocked bool
	withSourceDirectory(t, repository, func() { rule, path, blocked = scanChangedFiles(paths) })
	if !blocked || rule != "provider-key-sentinel" || path != "config.env" {
		t.Fatalf("blocked=%v rule=%q path=%q paths=%q", blocked, rule, path, paths)
	}
	if strings.Contains(rule+path, sentinel) || sourceGitOutput(t, repository, "rev-parse", "HEAD") != base {
		t.Fatal("secret scanner leaked the value or created a commit")
	}
}

func TestSource_SubmoduleAndLFSFollowExplicitPolicy(t *testing.T) {
	remote, author := sourceRepository(t)
	writeSourceFile(t, author, ".gitmodules", "[submodule \"outside\"]\n\tpath = outside\n\turl = https://example.com/outside.git\n")
	runSourceGit(t, author, "add", ".gitmodules")
	runSourceGit(t, author, "commit", "-m", "submodule")
	commit := sourceGitOutput(t, author, "rev-parse", "HEAD")
	runSourceGit(t, author, "push", "origin", "HEAD:main")
	checkout := t.TempDir()
	withSourceDirectory(t, checkout, func() {
		err := checkoutExactRevision(remote, commit, "agent/task-1")
		if err == nil || !strings.Contains(err.Error(), "submodules are disabled") {
			t.Fatalf("submodule policy error=%v", err)
		}
	})
}

func TestSource_CheckoutCredentialRemovedFromDiskAndGitConfig(t *testing.T) {
	remote, author := sourceRepositoryWithCommit(t)
	commit := sourceGitOutput(t, author, "rev-parse", "HEAD")
	checkout := t.TempDir()
	credential := "glpat-this-must-not-persist-0123456789"
	t.Setenv("GIT_USERNAME", "project-bot")
	t.Setenv("GIT_TOKEN", credential)
	withSourceDirectory(t, checkout, func() {
		if err := checkoutExactRevision(remote, commit, "agent/task-1"); err != nil {
			t.Fatal(err)
		}
	})
	_ = os.Unsetenv("GIT_TOKEN")
	_ = os.Unsetenv("GIT_USERNAME")
	err := filepath.WalkDir(checkout, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		raw, readErr := os.ReadFile(path)
		if readErr == nil && strings.Contains(string(raw), credential) {
			t.Fatalf("credential persisted in %s", path)
		}
		return readErr
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSource_GitAskpassReadsOnlyCommandScopedEnvironment(t *testing.T) {
	t.Setenv("GIT_USERNAME", "project-bot")
	t.Setenv("GIT_TOKEN", "short-lived-token")
	previous := os.Stdout
	defer func() { os.Stdout = previous }()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writer
	if err := ExecuteGitAskpass([]string{"Username for 'https://gitlab.com':"}); err != nil {
		t.Fatal(err)
	}
	_ = writer.Close()
	os.Stdout = previous
	raw := make([]byte, 64)
	count, err := reader.Read(raw)
	_ = reader.Close()
	if err != nil || string(raw[:count]) != "project-bot\n" {
		t.Fatalf("username askpass=%q err=%v", raw[:count], err)
	}
}

func sourceRepository(t *testing.T) (string, string) {
	t.Helper()
	remote := filepath.Join(t.TempDir(), "remote.git")
	runSourceGit(t, "", "init", "--bare", remote)
	author := t.TempDir()
	runSourceGit(t, author, "init", "--initial-branch=main")
	runSourceGit(t, author, "config", "user.name", "Test Agent")
	runSourceGit(t, author, "config", "user.email", "agent@example.invalid")
	runSourceGit(t, author, "remote", "add", "origin", remote)
	return remote, author
}

func sourceRepositoryWithCommit(t *testing.T) (string, string) {
	t.Helper()
	remote, author := sourceRepository(t)
	writeSourceFile(t, author, "README.md", "base\n")
	runSourceGit(t, author, "add", "README.md")
	runSourceGit(t, author, "commit", "-m", "base")
	runSourceGit(t, author, "push", "origin", "HEAD:main")
	return remote, author
}

func checkoutSourceCopy(t *testing.T, remote, base string) string {
	t.Helper()
	directory := t.TempDir()
	withSourceDirectory(t, directory, func() {
		if err := checkoutExactRevision(remote, base, "main"); err != nil {
			t.Fatal(err)
		}
	})
	runSourceGit(t, directory, "config", "user.name", "Test Agent")
	runSourceGit(t, directory, "config", "user.email", "agent@example.invalid")
	return directory
}

func runSourceGit(t *testing.T, directory string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", arguments...)
	if directory != "" {
		command.Dir = directory
	}
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", arguments, err, output)
	}
}

func sourceGitOutput(t *testing.T, directory string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = directory
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
	output, err := command.Output()
	if err != nil {
		t.Fatalf("git %v: %v", arguments, err)
	}
	return strings.TrimSpace(string(output))
}

func writeSourceFile(t *testing.T, directory, name, content string) {
	t.Helper()
	path := filepath.Join(directory, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func withSourceDirectory(t *testing.T, directory string, action func()) {
	t.Helper()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(directory); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(previous) }()
	action()
}

func encodedMutation(t *testing.T, mutation sourcev2.PatchMutation) string {
	t.Helper()
	raw, err := json.Marshal(mutation)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

func configureSourceWorkspace(t *testing.T, root string, identity bool) {
	t.Helper()
	identityDirectory := t.TempDir()
	certificatePath := filepath.Join(identityDirectory, "agent.crt")
	privateKeyPath := filepath.Join(identityDirectory, "agent.key")
	caPath := filepath.Join(identityDirectory, "ca.crt")
	if identity {
		certificatePEM, privatePEM := sourceIdentity(t)
		writeSecureSourceFile(t, certificatePath, certificatePEM)
		writeSecureSourceFile(t, privateKeyPath, privatePEM)
		writeSecureSourceFile(t, caPath, certificatePEM)
	}
	config := Config{
		ControlPlaneURL: "https://workspace.example.invalid", EgressGatewayURL: "https://egress.example.invalid:8443",
		WorkspaceID: "workspace-1", CorrelationID: "corr-1", CertificateFile: certificatePath, PrivateKeyFile: privateKeyPath,
		CAFile: caPath, JournalDirectory: filepath.Join(root, ".journal"), WorkspaceRoot: root,
	}
	raw, _ := json.Marshal(config)
	configPath := filepath.Join(identityDirectory, "config.json")
	writeSecureSourceFile(t, configPath, raw)
	t.Setenv("WORKSPACE_AGENT_CONFIG_FILE", configPath)
}

func sourceIdentity(t *testing.T) ([]byte, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "workspace"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(10 * time.Minute), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER})
}

func sourceCertificate(t *testing.T, configPath string) *x509.Certificate {
	t.Helper()
	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var config Config
	if err := json.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(config.CertificateFile)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(raw)
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return certificate
}

func writeSecureSourceFile(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
}

func withChangedPaths(t *testing.T, directory string) ([]string, error) {
	t.Helper()
	var paths []string
	var err error
	withSourceDirectory(t, directory, func() { paths, err = changedPaths() })
	return paths, err
}

func sourceReceiptFingerprint(certificate *x509.Certificate) string {
	digest := sha256.Sum256(certificate.Raw)
	return "sha256:" + hex.EncodeToString(digest[:])
}
