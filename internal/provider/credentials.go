package provider

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	"cs-cloud/internal/cloud"
	"cs-cloud/internal/platform"
)

func CredentialsPath() (string, error) {
	return filepath.Join(platform.CoStrictShareDir(), "auth.json"), nil
}

func LoadCredentials() (*Credentials, error) {
	p, err := CredentialsPath()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var cred Credentials
	if err := json.Unmarshal(b, &cred); err != nil {
		return nil, nil
	}
	if cred.AccessToken == "" || cred.BaseURL == "" {
		return nil, nil
	}
	return &cred, nil
}

func SaveCredentials(cred *Credentials) error {
	p, err := CredentialsPath()
	if err != nil {
		return err
	}
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create credentials dir: %w", err)
	}
	b, err := json.MarshalIndent(cred, "", "  ")
	if err != nil {
		return fmt.Errorf("encode credentials: %w", err)
	}
	if err := os.WriteFile(p, b, 0o600); err != nil {
		return fmt.Errorf("write credentials: %w", err)
	}
	return nil
}

func DeleteCredentials() error {
	p, err := CredentialsPath()
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func GetCoStrictBaseURL(providerAPI string, credBaseURL string) string {
	cc := cloud.NewClient(nil)
	return cc.OIDCBaseURL(credBaseURL)
}

func BuildOAuthParams(includeMachineCode bool, machineID string, state string) []string {
	var params []string
	if includeMachineCode {
		if machineID == "" {
			panic("machineID is required when includeMachineCode is true")
		}
		params = append(params, "machine_code="+url.QueryEscape(machineID))
	}
	if state != "" {
		params = append(params, "state="+url.QueryEscape(state))
	}
	ver := "costrict-cli-dev"
	params = append(params,
		"provider=casdoor",
		"plugin_version="+url.QueryEscape(ver),
		"vscode_version="+url.QueryEscape(ver),
		"uri_scheme=costrict-cli",
	)
	return params
}
