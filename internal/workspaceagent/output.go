package workspaceagent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

type FileOutputSink struct{ Directory string }

func (s FileOutputSink) Persist(commandID string, stdout, stderr []byte, truncated bool) error {
	if !filepath.IsAbs(s.Directory) || filepath.Clean(s.Directory) != s.Directory || !identityPattern.MatchString(commandID) {
		return errors.New("workspace output sink is invalid")
	}
	if err := os.MkdirAll(s.Directory, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(s.Directory, 0o700); err != nil {
		return err
	}
	info, err := os.Stat(s.Directory)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return errors.New("workspace output directory permissions are invalid")
	}
	metadata, err := json.Marshal(map[string]any{"schema": "workspace.platform.example.com/redacted-output/v1", "truncated": truncated})
	if err != nil {
		return err
	}
	digest := outputIdentity(commandID)
	for _, item := range []struct {
		suffix string
		raw    []byte
	}{{"stdout", stdout}, {"stderr", stderr}, {"json", append(metadata, '\n')}} {
		if err := writeAtomic(filepath.Join(s.Directory, digest+"."+item.suffix), item.raw, 0o600); err != nil {
			return err
		}
	}
	return syncDirectory(s.Directory)
}

func outputIdentity(commandID string) string {
	digest := sha256.Sum256([]byte(commandID))
	return hex.EncodeToString(digest[:])
}
