package device

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"cs-cloud/internal/platform"
	"cs-cloud/internal/provider"
	"cs-cloud/internal/version"
)

func CheckGatewayConnectivity(ctx context.Context, dev *DeviceInfo) error {
	gatewayURL, err := AssignGateway(ctx, dev)
	if err != nil {
		return fmt.Errorf("gateway assignment failed: %w", err)
	}

	healthURL := strings.TrimRight(gatewayURL, "/") + "/health"
	healthCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(healthCtx, http.MethodGet, healthURL, nil)
	if err != nil {
		return nil
	}

	resp, err := platform.HTTPClient().Do(req)
	if err != nil {
		return fmt.Errorf("gateway unreachable (%s): %w", gatewayURL, err)
	}
	resp.Body.Close()

	if resp.StatusCode >= 500 {
		return fmt.Errorf("gateway returned %d", resp.StatusCode)
	}

	return nil
}

func setDeviceAuthHeaders(req *http.Request, device *DeviceInfo) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+device.DeviceToken)
	if cred, err := provider.LoadCredentials(); err == nil && cred != nil && cred.AccessToken != "" {
		req.Header.Set("X-User-Token", cred.AccessToken)
	}
}

func ValidateDeviceToken(ctx context.Context, device *DeviceInfo) error {
	reqBody := map[string]any{
		"deviceID": device.DeviceID,
		"version":  version.Get(),
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return err
	}

	url := GetCloudAPIURL(nil, "/cloud/device/gateway-assign", device.BaseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	setDeviceAuthHeaders(req, device)

	resp, err := platform.HTTPClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("device token validation failed: %d %s", resp.StatusCode, string(respBody))
	}

	return nil
}

func AssignGateway(ctx context.Context, device *DeviceInfo) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	reqBody := map[string]any{
		"deviceID": device.DeviceID,
		"version":  version.Get(),
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	url := GetCloudAPIURL(nil, "/cloud/device/gateway-assign", device.BaseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	setDeviceAuthHeaders(req, device)

	resp, err := platform.HTTPClient().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("gateway-assign failed: %d %s", resp.StatusCode, string(respBody))
	}

	var data struct {
		GatewayURL string `json:"gatewayURL"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return "", err
	}

	return data.GatewayURL, nil
}
