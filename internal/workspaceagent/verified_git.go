package workspaceagent

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	sourcev2 "github.com/keir-research/ai-native-paas/pkg/contracts/source/v2"
)

var repositoryIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

func ExecuteGitAskpass(arguments []string) error {
	if len(arguments) > 1 {
		return errors.New("invalid Git credential prompt")
	}
	prompt := ""
	if len(arguments) == 1 {
		prompt = strings.ToLower(arguments[0])
	}
	name := "GIT_TOKEN"
	if strings.Contains(prompt, "username") {
		name = "GIT_USERNAME"
	}
	value := os.Getenv(name)
	if value == "" || strings.ContainsRune(value, '\x00') {
		return errors.New("Git command credential is unavailable")
	}
	_, err := fmt.Fprintln(os.Stdout, value)
	return err
}

func ExecuteVerifiedGitCheckout(arguments []string) error {
	if len(arguments) != 4 || !repositoryIDPattern.MatchString(arguments[0]) || !commitSHAPattern.MatchString(arguments[2]) || !sourcev2.ValidBranch(arguments[3]) {
		return errors.New("repository, URL, exact revision and branch are required")
	}
	cloneURL, err := verifiedCloneURL(arguments[1])
	if err != nil {
		return err
	}
	if err := requireWorkspaceDirectory(); err != nil {
		return err
	}
	return checkoutExactRevision(cloneURL, arguments[2], arguments[3])
}

func checkoutExactRevision(cloneURL, expectedSHA, branch string) error {
	gitDirectory := filepath.Join(".", ".git")
	if _, err := os.Stat(gitDirectory); errors.Is(err, fs.ErrNotExist) {
		entries, readErr := os.ReadDir(".")
		if readErr != nil || len(entries) != 0 {
			return errors.New("workspace checkout directory is not empty")
		}
		if err := runGit("init", "--initial-branch=main", "."); err != nil {
			return err
		}
		if err := runGit("remote", "add", "origin", cloneURL); err != nil {
			return err
		}
	} else if err != nil {
		return errors.New("inspect workspace repository")
	}
	origin, err := gitOutput("remote", "get-url", "origin")
	if err != nil || strings.TrimSpace(string(origin)) != cloneURL {
		return errors.New("workspace repository origin changed")
	}
	if err := validateRepositoryMetadata(); err != nil {
		return err
	}
	if dirty, err := gitOutput("status", "--porcelain=v1", "-z", "--untracked-files=normal"); err != nil || len(dirty) != 0 {
		return errors.New("workspace repository is not clean")
	}
	if err := runGit("fetch", "--no-tags", "--depth=1", "origin", expectedSHA); err != nil {
		return errors.New("fetch exact repository revision")
	}
	if err := runGit("checkout", "--detach", "FETCH_HEAD"); err != nil {
		return errors.New("checkout exact repository revision")
	}
	if err := verifyGitHead(expectedSHA); err != nil {
		return err
	}
	if err := rejectImplicitGitDependencies(); err != nil {
		return err
	}
	if err := runGit("checkout", "-B", branch, expectedSHA); err != nil {
		return errors.New("create exact-base workspace branch")
	}
	return verifyGitHead(expectedSHA)
}

func ExecuteVerifiedGitApplyPatch(arguments []string) error {
	if len(arguments) < 2 || !commitSHAPattern.MatchString(arguments[0]) || len(arguments) > 127 {
		return errors.New("exact revision and bounded patch files are required")
	}
	if err := requireWorkspaceDirectory(); err != nil {
		return err
	}
	if err := verifyGitHead(arguments[0]); err != nil {
		return err
	}
	if err := validateRepositoryMetadata(); err != nil {
		return err
	}
	mutations := make([]sourcev2.PatchMutation, 0, len(arguments)-1)
	seen := make(map[string]struct{}, len(arguments)-1)
	for _, encoded := range arguments[1:] {
		if len(encoded) > 4096 {
			return errors.New("patch file exceeds command argument limit")
		}
		raw, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
		if err != nil {
			return errors.New("decode patch file")
		}
		var mutation sourcev2.PatchMutation
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&mutation) != nil || mutation.Validate() != nil {
			return errors.New("invalid repository patch file")
		}
		if _, exists := seen[mutation.Path]; exists {
			return errors.New("duplicate repository patch path")
		}
		seen[mutation.Path] = struct{}{}
		if err := rejectSymlinkPath(mutation.Path); err != nil {
			return err
		}
		mutations = append(mutations, mutation)
	}
	for _, mutation := range mutations {
		if mutation.Delete {
			if err := os.Remove(mutation.Path); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return errors.New("delete repository patch file")
			}
			continue
		}
		content, _ := base64.StdEncoding.Strict().DecodeString(mutation.ContentBase64)
		if err := os.MkdirAll(filepath.Dir(mutation.Path), 0o755); err != nil {
			return errors.New("create repository patch directory")
		}
		temporary, err := os.CreateTemp(filepath.Dir(mutation.Path), ".platform-patch-*")
		if err != nil {
			return errors.New("create repository patch temporary file")
		}
		temporaryName := temporary.Name()
		if err := temporary.Chmod(0o644); err == nil {
			_, err = temporary.Write(content)
		}
		if closeErr := temporary.Close(); err == nil {
			err = closeErr
		}
		for index := range content {
			content[index] = 0
		}
		if err != nil {
			_ = os.Remove(temporaryName)
			return errors.New("write repository patch file")
		}
		if err := os.Rename(temporaryName, mutation.Path); err != nil {
			_ = os.Remove(temporaryName)
			return errors.New("publish repository patch file")
		}
	}
	return nil
}

func ExecuteVerifiedGitCommit(arguments []string) error {
	if len(arguments) != 8 || !repositoryIDPattern.MatchString(arguments[0]) || !commitSHAPattern.MatchString(arguments[1]) || !sourcev2.ValidBranch(arguments[2]) || !repositoryIDPattern.MatchString(arguments[3]) || !repositoryIDPattern.MatchString(arguments[4]) || !repositoryIDPattern.MatchString(arguments[5]) || !planDigestPattern.MatchString(arguments[6]) || !validCommitTitle(arguments[7]) {
		return errors.New("verified repository commit binding is invalid")
	}
	if err := requireWorkspaceDirectory(); err != nil {
		return err
	}
	if err := validateRepositoryMetadata(); err != nil {
		return err
	}
	commandID, sessionID, err := commandBinding()
	if err != nil {
		return err
	}
	commitSHA, recoverExisting, err := exactCommitState(arguments[1], arguments[0], arguments[2], arguments[3], arguments[4], arguments[5], arguments[6])
	if err != nil {
		return err
	}
	if !recoverExisting {
		paths, err := changedPaths()
		if err != nil || len(paths) == 0 {
			return errors.New("repository has no committable changes")
		}
		if err := verifyActualChangeSet(arguments[0], arguments[1], arguments[2], arguments[6], paths); err != nil {
			return err
		}
		if rule, path, found := scanChangedFiles(paths); found {
			return fmt.Errorf("repository secret scan blocked rule %s in %s", rule, path)
		}
		if err := rejectImplicitGitDependencies(); err != nil {
			return err
		}
		if err := runGit("add", "-A", "--"); err != nil {
			return errors.New("stage repository changes")
		}
		message := arguments[7] + "\n\nTask-ID: " + arguments[4] + "\nOperation-ID: " + arguments[5] + "\nActor-ID: " + arguments[3] + "\nRepository-ID: " + arguments[0] + "\nSource-Plan-Hash: " + arguments[6]
		if err := runGit("-c", "user.name=AI Native Platform Agent", "-c", "user.email=agent@platform.invalid", "commit", "--no-gpg-sign", "--no-verify", "-m", message); err != nil {
			return errors.New("create governed repository commit")
		}
		head, err := gitOutput("rev-parse", "--verify", "HEAD^{commit}")
		if err != nil || !commitSHAPattern.MatchString(strings.TrimSpace(string(head))) {
			return errors.New("resolve governed repository commit")
		}
		commitSHA = strings.TrimSpace(string(head))
	}
	receipt, err := signCommitReceipt(commandID, sessionID, arguments, commitSHA)
	if err != nil {
		return err
	}
	config, err := LoadConfig(configPath())
	if err != nil {
		return err
	}
	client, err := NewClient(config)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	if err := client.SubmitCommitReceipt(ctx, receipt); err != nil {
		return fmt.Errorf("submit authenticated Git commit receipt: %w", err)
	}
	return nil
}

func ExecuteVerifiedGitPush(arguments []string) error {
	if len(arguments) != 7 || !repositoryIDPattern.MatchString(arguments[0]) || !sourcev2.ValidBranch(arguments[2]) || !commitSHAPattern.MatchString(arguments[3]) || !commitSHAPattern.MatchString(arguments[4]) || !planDigestPattern.MatchString(arguments[5]) || arguments[6] != "absent" && !commitSHAPattern.MatchString(arguments[6]) {
		return errors.New("verified repository push binding is invalid")
	}
	cloneURL, err := verifiedCloneURL(arguments[1])
	if err != nil {
		return err
	}
	if err := requireWorkspaceDirectory(); err != nil {
		return err
	}
	if err := validateRepositoryMetadata(); err != nil {
		return err
	}
	origin, err := gitOutput("remote", "get-url", "origin")
	if err != nil || strings.TrimSpace(string(origin)) != cloneURL {
		return errors.New("workspace repository origin changed")
	}
	if err := verifyGitHead(arguments[4]); err != nil {
		return err
	}
	parent, err := gitOutput("rev-parse", "--verify", arguments[4]+"^1")
	if err != nil || strings.TrimSpace(string(parent)) != arguments[3] {
		return errors.New("repository commit does not descend from exact source base")
	}
	paths, err := changedPathsBetween(arguments[3], arguments[4])
	if err != nil || len(paths) == 0 {
		return errors.New("inspect governed repository commit")
	}
	if err := verifyActualChangeSet(arguments[0], arguments[3], arguments[2], arguments[5], paths); err != nil {
		return err
	}
	if rule, path, found := scanChangedFiles(paths); found {
		return fmt.Errorf("repository secret scan blocked rule %s in %s", rule, path)
	}
	if err := rejectImplicitGitDependencies(); err != nil {
		return err
	}
	message, err := gitOutput("show", "-s", "--format=%B", arguments[4])
	if err != nil || !strings.Contains(string(message), "Repository-ID: "+arguments[0]) || !strings.Contains(string(message), "Source-Plan-Hash: "+arguments[5]) {
		return errors.New("repository commit lacks governed identity trailer")
	}
	expected := arguments[6]
	if expected == "absent" {
		expected = ""
	}
	return pushWithLease(arguments[2], expected)
}

func pushWithLease(branch, expected string) error {
	lease := "--force-with-lease=refs/heads/" + branch + ":" + expected
	if err := runGit("push", "--porcelain", lease, "origin", "HEAD:refs/heads/"+branch); err != nil {
		return errors.New("repository push conflicts with expected remote revision")
	}
	return nil
}

func verifiedCloneURL(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() != "gitlab.com" || parsed.Port() != "" && parsed.Port() != "443" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.RawPath != "" || !strings.HasSuffix(parsed.Path, ".git") || strings.Count(strings.Trim(parsed.Path, "/"), "/") < 1 {
		return "", errors.New("repository clone URL is not an approved GitLab HTTPS URL")
	}
	return parsed.String(), nil
}

func runGit(arguments ...string) error {
	command, err := gitCommand(arguments...)
	if err != nil {
		return err
	}
	command.Stdout, command.Stderr = os.Stdout, os.Stderr
	return command.Run()
}

func gitOutput(arguments ...string) ([]byte, error) {
	command, err := gitCommand(arguments...)
	if err != nil {
		return nil, err
	}
	return command.Output()
}

func gitCommand(arguments ...string) (*exec.Cmd, error) {
	git, err := exec.LookPath("git")
	if err != nil {
		return nil, errors.New("Git executable is unavailable")
	}
	self, err := os.Executable()
	if err != nil {
		return nil, errors.New("resolve workspace agent executable")
	}
	command := exec.Command(git, arguments...)
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS="+self, "GIT_ASKPASS_REQUIRE=force", "WORKSPACE_AGENT_GIT_ASKPASS=1", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_LFS_SKIP_SMUDGE=1")
	return command, nil
}

func requireWorkspaceDirectory() error {
	config, err := LoadConfig(configPath())
	if err != nil {
		return err
	}
	current, err := os.Getwd()
	if err != nil {
		return errors.New("resolve workspace repository directory")
	}
	relative, err := filepath.Rel(config.WorkspaceRoot, current)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("repository operation escapes workspace root")
	}
	return nil
}

func configPath() string {
	if value := os.Getenv("WORKSPACE_AGENT_CONFIG_FILE"); value != "" {
		return value
	}
	return "/var/lib/ai-native-paas/identity/workspace-agent.json"
}

func verifyGitHead(expected string) error {
	head, err := gitOutput("rev-parse", "--verify", "HEAD^{commit}")
	if err != nil || strings.TrimSpace(string(head)) != expected {
		return errors.New("workspace source revision does not match command binding")
	}
	return nil
}

func rejectImplicitGitDependencies() error {
	if info, err := os.Stat(".gitmodules"); err == nil && info.Size() > 0 {
		return errors.New("repository submodules are disabled by beta policy")
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return errors.New("inspect repository submodule policy")
	}
	dependencyFound := false
	_ = filepath.WalkDir(".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || dependencyFound {
			return fs.SkipAll
		}
		if entry.IsDir() && entry.Name() == ".git" {
			return filepath.SkipDir
		}
		if entry.Name() != ".gitattributes" {
			return nil
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil || len(raw) > 1<<20 || bytes.Contains(bytes.ToLower(raw), []byte("filter=")) {
			dependencyFound = true
		}
		return nil
	})
	if dependencyFound {
		return errors.New("repository Git content filters and LFS objects are disabled by beta policy")
	}
	return nil
}

func rejectSymlinkPath(value string) error {
	current := "."
	for _, segment := range strings.Split(filepath.Dir(value), string(filepath.Separator)) {
		if segment == "." || segment == "" {
			continue
		}
		current = filepath.Join(current, segment)
		info, err := os.Lstat(current)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return errors.New("repository patch path crosses a non-directory or symlink")
		}
	}
	if info, err := os.Lstat(value); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return errors.New("repository patch target is a symlink")
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return errors.New("inspect repository patch target")
	}
	return nil
}

func changedPaths() ([]string, error) {
	tracked, err := gitOutput("diff", "--name-only", "--no-renames", "-z", "HEAD", "--")
	if err != nil {
		return nil, err
	}
	untracked, err := gitOutput("ls-files", "--others", "--exclude-standard", "-z", "--")
	if err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	for _, raw := range append(bytes.Split(tracked, []byte{0}), bytes.Split(untracked, []byte{0})...) {
		if len(raw) == 0 {
			continue
		}
		value := string(raw)
		mutation := sourcev2.PatchMutation{Path: value, Delete: true}
		if mutation.Validate() != nil {
			return nil, errors.New("repository contains an unsafe changed path")
		}
		seen[value] = struct{}{}
	}
	result := make([]string, 0, len(seen))
	for value := range seen {
		result = append(result, value)
	}
	sortStrings(result)
	return result, nil
}

func changedPathsBetween(baseSHA, commitSHA string) ([]string, error) {
	raw, err := gitOutput("diff", "--name-only", "--no-renames", "-z", baseSHA, commitSHA, "--")
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{})
	for _, item := range bytes.Split(raw, []byte{0}) {
		if len(item) == 0 {
			continue
		}
		path := string(item)
		if (sourcev2.PatchMutation{Path: path, Delete: true}).Validate() != nil {
			return nil, errors.New("repository commit contains an unsafe path")
		}
		seen[path] = struct{}{}
	}
	result := make([]string, 0, len(seen))
	for path := range seen {
		result = append(result, path)
	}
	sortStrings(result)
	return result, nil
}

func validateRepositoryMetadata() error {
	raw, err := gitOutput("config", "--local", "--name-only", "--list", "-z")
	if err != nil {
		return errors.New("inspect repository local configuration")
	}
	for _, item := range bytes.Split(raw, []byte{0}) {
		key := strings.ToLower(strings.TrimSpace(string(item)))
		if key == "" {
			continue
		}
		for _, forbidden := range []string{"alias.", "credential.", "diff.external", "difftool.", "filter.", "include.", "includeif.", "protocol.", "url."} {
			if key == forbidden || strings.HasPrefix(key, forbidden) {
				return errors.New("repository local configuration violates execution policy")
			}
		}
		if key == "core.hookspath" || key == "core.fsmonitor" || key == "core.sshcommand" {
			return errors.New("repository local configuration violates execution policy")
		}
	}
	return nil
}

var secretRules = []struct {
	id      string
	pattern *regexp.Regexp
}{
	{"provider-key-sentinel", regexp.MustCompile(`provider[-_]key[-_]sentinel`)},
	{"private-key", regexp.MustCompile(`-----BEGIN (?:[A-Z0-9 ]+ )?PRIVATE KEY-----`)},
	{"gitlab-token", regexp.MustCompile(`glpat-[A-Za-z0-9_-]{16,}`)},
	{"github-token", regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{20,}`)},
	{"aws-access-key", regexp.MustCompile(`AKIA[0-9A-Z]{16}`)},
	{"credential-assignment", regexp.MustCompile(`(?i)(?:token|secret|password|api[_-]?key)\s*[:=]\s*["']?[A-Za-z0-9_./+=-]{20,}`)},
}

func scanChangedFiles(paths []string) (string, string, bool) {
	total := int64(0)
	for _, path := range paths {
		info, err := os.Lstat(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil || !info.Mode().IsRegular() || info.Size() > 4<<20 || total+info.Size() > 32<<20 {
			return "unscannable-file", path, true
		}
		total += info.Size()
		raw, err := os.ReadFile(path)
		if err != nil {
			return "unscannable-file", path, true
		}
		for _, rule := range secretRules {
			if rule.pattern.Match(raw) {
				return rule.id, path, true
			}
		}
	}
	return "", "", false
}

func validCommitTitle(value string) bool {
	trimmed := strings.TrimSpace(value)
	return trimmed == value && len(value) > 0 && len(value) <= 256 && !strings.ContainsAny(value, "\r\n\x00")
}

func exactCommitState(baseSHA, repositoryID, branch, agentID, taskID, correlationID, planHash string) (string, bool, error) {
	head, err := gitOutput("rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return "", false, errors.New("resolve workspace repository head")
	}
	actual := strings.TrimSpace(string(head))
	if actual == baseSHA {
		return "", false, nil
	}
	parent, err := gitOutput("rev-parse", "--verify", "HEAD^1")
	message, messageErr := gitOutput("show", "-s", "--format=%B", "HEAD")
	if err != nil || messageErr != nil || strings.TrimSpace(string(parent)) != baseSHA || !strings.Contains(string(message), "Repository-ID: "+repositoryID) || !strings.Contains(string(message), "Actor-ID: "+agentID) || !strings.Contains(string(message), "Task-ID: "+taskID) || !strings.Contains(string(message), "Operation-ID: "+correlationID) || !strings.Contains(string(message), "Source-Plan-Hash: "+planHash) {
		return "", false, errors.New("workspace repository head conflicts with exact base")
	}
	paths, err := changedPathsBetween(baseSHA, actual)
	if err != nil || len(paths) == 0 {
		return "", false, errors.New("inspect recovered governed repository commit")
	}
	if err := verifyActualChangeSet(repositoryID, baseSHA, branch, planHash, paths); err != nil {
		return "", false, err
	}
	if rule, path, found := scanChangedFiles(paths); found {
		return "", false, fmt.Errorf("repository secret scan blocked rule %s in %s", rule, path)
	}
	return actual, true, nil
}

func verifyActualChangeSet(repositoryID, baseSHA, branch, expectedHash string, paths []string) error {
	files := make([]sourcev2.PatchFile, 0, len(paths))
	for _, path := range paths {
		file := sourcev2.PatchFile{Path: path, ContentHash: "sha256:" + strings.Repeat("0", 64)}
		info, err := os.Lstat(path)
		if errors.Is(err, fs.ErrNotExist) {
			file.Delete = true
		} else if err != nil || !info.Mode().IsRegular() || info.Size() > 4<<20 {
			return errors.New("source change plan contains an unhashable file")
		} else {
			raw, err := os.ReadFile(path)
			if err != nil {
				return errors.New("hash source change plan file")
			}
			digest := sha256.Sum256(raw)
			file.ContentHash = "sha256:" + hex.EncodeToString(digest[:])
		}
		files = append(files, file)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	raw, err := json.Marshal(sourcev2.ChangeSet{RepositoryID: repositoryID, BaseSHA: baseSHA, TargetBranch: branch, Files: files})
	if err != nil {
		return errors.New("canonicalize actual source change set")
	}
	digest := sha256.Sum256(raw)
	if "sha256:"+hex.EncodeToString(digest[:]) != expectedHash {
		return errors.New("actual repository changes do not match approved source plan")
	}
	return nil
}

func commandBinding() (string, string, error) {
	commandID := os.Getenv("PLATFORM_COMMAND_ID")
	sessionID := os.Getenv("PLATFORM_EXECUTION_SESSION_ID")
	if !identityPattern.MatchString(commandID) || !identityPattern.MatchString(sessionID) {
		return "", "", errors.New("verified Git command binding is unavailable")
	}
	return commandID, sessionID, nil
}

func signCommitReceipt(commandID, sessionID string, arguments []string, commitSHA string) (sourcev2.AgentCommitReceipt, error) {
	config, err := LoadConfig(configPath())
	if err != nil {
		return sourcev2.AgentCommitReceipt{}, err
	}
	certificatePEM, err := os.ReadFile(config.CertificateFile)
	if err != nil {
		return sourcev2.AgentCommitReceipt{}, errors.New("read workspace identity certificate")
	}
	block, _ := pem.Decode(certificatePEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return sourcev2.AgentCommitReceipt{}, errors.New("decode workspace identity certificate")
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return sourcev2.AgentCommitReceipt{}, errors.New("parse workspace identity certificate")
	}
	privatePEM, err := os.ReadFile(config.PrivateKeyFile)
	if err != nil {
		return sourcev2.AgentCommitReceipt{}, errors.New("read workspace identity private key")
	}
	privateBlock, _ := pem.Decode(privatePEM)
	for index := range privatePEM {
		privatePEM[index] = 0
	}
	if privateBlock == nil {
		return sourcev2.AgentCommitReceipt{}, errors.New("decode workspace identity private key")
	}
	parsedKey, err := x509.ParsePKCS8PrivateKey(privateBlock.Bytes)
	for index := range privateBlock.Bytes {
		privateBlock.Bytes[index] = 0
	}
	privateKey, ok := parsedKey.(*ecdsa.PrivateKey)
	if err != nil || !ok {
		return sourcev2.AgentCommitReceipt{}, errors.New("workspace identity key cannot sign commit attestation")
	}
	issuedAt := time.Now().UTC()
	statement := sourcev2.CommitStatement{
		RepositoryID: arguments[0], BaseSHA: arguments[1], CommitSHA: commitSHA, Branch: arguments[2],
		AgentID: arguments[3], TaskID: arguments[4], CorrelationID: arguments[5], SourcePlanHash: arguments[6], IssuedAt: issuedAt,
	}
	canonical, err := statement.Canonical()
	if err != nil {
		return sourcev2.AgentCommitReceipt{}, err
	}
	statementHash := sha256.Sum256(canonical)
	signature, err := ecdsa.SignASN1(rand.Reader, privateKey, statementHash[:])
	privateKey.D.SetInt64(0)
	if err != nil {
		return sourcev2.AgentCommitReceipt{}, errors.New("sign commit attestation")
	}
	signatureHash := sha256.Sum256(signature)
	certificateHash := sha256.Sum256(certificate.Raw)
	receipt := sourcev2.AgentCommitReceipt{
		SessionID: sessionID, ExecutionSessionID: sessionID, CommandID: commandID, Statement: statement,
		Attestation: sourcev2.CommitAttestation{
			RepositoryID: arguments[0], CommitSHA: commitSHA, AgentID: arguments[3], TaskID: arguments[4], CorrelationID: arguments[5],
			StatementDigest: "sha256:" + hex.EncodeToString(statementHash[:]), SignatureDigest: "sha256:" + hex.EncodeToString(signatureHash[:]), IssuedAt: issuedAt,
		},
		Signature: base64.StdEncoding.EncodeToString(signature), CertificateFingerprint: "sha256:" + hex.EncodeToString(certificateHash[:]),
	}
	if receipt.Validate() != nil {
		return sourcev2.AgentCommitReceipt{}, errors.New("generated commit receipt is invalid")
	}
	return receipt, nil
}
