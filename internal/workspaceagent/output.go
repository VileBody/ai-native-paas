package workspaceagent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	workspacev1 "github.com/keir-research/ai-native-paas/pkg/contracts/workspace/v1"
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

func (s FileOutputSink) Stream(commandID string, emit func(workspacev1.AgentOutputChunk) error) error {
	if !filepath.IsAbs(s.Directory) || filepath.Clean(s.Directory) != s.Directory || !identityPattern.MatchString(commandID) || emit == nil {
		return errors.New("workspace output source is invalid")
	}
	digest := outputIdentity(commandID)
	metadataRaw, err := readOutputFile(filepath.Join(s.Directory, digest+".json"), 4096)
	if err != nil {
		return err
	}
	var metadata struct {
		Schema    string `json:"schema"`
		Truncated bool   `json:"truncated"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(metadataRaw)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&metadata) != nil || metadata.Schema != "workspace.platform.example.com/redacted-output/v1" {
		return errors.New("workspace output metadata is invalid")
	}
	for _, stream := range []struct {
		name   workspacev1.AgentOutputStream
		suffix string
	}{{workspacev1.AgentOutputStdout, "stdout"}, {workspacev1.AgentOutputStderr, "stderr"}} {
		if err := streamOutput(filepath.Join(s.Directory, digest+"."+stream.suffix), commandID, stream.name, metadata.Truncated, emit); err != nil {
			return err
		}
	}
	return nil
}

func streamOutput(filename, commandID string, stream workspacev1.AgentOutputStream, truncated bool, emit func(workspacev1.AgentOutputChunk) error) error {
	file, err := os.Open(filename)
	if err != nil {
		return errors.New("open workspace output")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() < 0 || info.Size() > 1<<30 {
		return errors.New("workspace output file is invalid")
	}
	total := sha256.New()
	if _, err := io.Copy(total, file); err != nil {
		return errors.New("hash workspace output")
	}
	totalDigest := "sha256:" + hex.EncodeToString(total.Sum(nil))
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return errors.New("rewind workspace output")
	}
	buffer := make([]byte, 32<<10)
	sequence := int64(0)
	offset := int64(0)
	for {
		read, readErr := file.Read(buffer)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return errors.New("read workspace output")
		}
		data := append([]byte(nil), buffer[:read]...)
		offset += int64(read)
		final := offset == info.Size()
		chunkDigest := sha256.Sum256(data)
		chunk := workspacev1.AgentOutputChunk{
			CommandID: commandID, Stream: stream, Sequence: sequence, Data: data,
			ChunkSHA256: "sha256:" + hex.EncodeToString(chunkDigest[:]), Final: final,
		}
		if final {
			chunk.Truncated = truncated
			chunk.TotalSHA256 = totalDigest
		}
		if err := emit(chunk); err != nil {
			return err
		}
		sequence++
		if final {
			return nil
		}
	}
}

func readOutputFile(filename string, maximum int64) ([]byte, error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, errors.New("open workspace output metadata")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() <= 0 || info.Size() > maximum {
		return nil, errors.New("workspace output metadata is invalid")
	}
	return io.ReadAll(io.LimitReader(file, maximum+1))
}

func outputIdentity(commandID string) string {
	digest := sha256.Sum256([]byte(commandID))
	return hex.EncodeToString(digest[:])
}
