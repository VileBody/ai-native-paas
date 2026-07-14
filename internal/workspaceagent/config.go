// Package workspaceagent implements the process that runs only inside a
// disposable workspace VM.
package workspaceagent

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var identityPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

type Config struct {
	ControlPlaneURL  string `json:"control_plane_url"`
	WorkspaceID      string `json:"workspace_id"`
	CorrelationID    string `json:"correlation_id"`
	CertificateFile  string `json:"certificate_file"`
	PrivateKeyFile   string `json:"private_key_file"`
	CAFile           string `json:"ca_file"`
	JournalDirectory string `json:"journal_directory"`
	WorkspaceRoot    string `json:"workspace_root"`
}

func LoadConfig(filename string) (Config, error) {
	raw, err := readSecureFile(filename, 64<<10)
	if err != nil {
		return Config{}, errors.New("load workspace agent configuration")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var config Config
	if err := decoder.Decode(&config); err != nil {
		return Config{}, errors.New("parse workspace agent configuration")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return Config{}, errors.New("parse workspace agent configuration")
	}
	if err := config.Validate(); err != nil {
		return Config{}, err
	}
	return config, nil
}

func (c Config) Validate() error {
	endpoint, err := url.Parse(strings.TrimSpace(c.ControlPlaneURL))
	if err != nil || endpoint.Scheme != "https" || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || endpoint.RawPath != "" || endpoint.Path != "" && endpoint.Path != "/" {
		return errors.New("workspace agent control-plane URL is invalid")
	}
	if !identityPattern.MatchString(c.WorkspaceID) || !identityPattern.MatchString(c.CorrelationID) {
		return errors.New("workspace agent identity is invalid")
	}
	for _, filename := range []string{c.CertificateFile, c.PrivateKeyFile, c.CAFile} {
		if !filepath.IsAbs(filename) || filepath.Clean(filename) != filename {
			return errors.New("workspace agent credential path is invalid")
		}
	}
	for _, directory := range []string{c.JournalDirectory, c.WorkspaceRoot} {
		if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory || directory == "/" {
			return errors.New("workspace agent directory is invalid")
		}
	}
	return nil
}

func readSecureFile(filename string, maximum int64) ([]byte, error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() <= 0 || info.Size() > maximum {
		return nil, errors.New("workspace agent file permissions or size are invalid")
	}
	raw, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil || int64(len(raw)) > maximum {
		return nil, errors.New("workspace agent file is invalid")
	}
	return raw, nil
}
