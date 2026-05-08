package device

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"cs-cloud/internal/cloud"
	"cs-cloud/internal/version"
)

func (c *Client) RotateToken(ctx context.Context) error {
	dev, err := LoadDevice()
	if err != nil {
		return err
	}
	if dev == nil {
		return fmt.Errorf("device not registered")
	}

	url := c.cloud.URL(cloud.DeviceTokenRotatePath(dev.DeviceID), dev.BaseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return err
	}
	c.cloud.SetDeviceAuthHeadersWithUser(req, dev.DeviceToken, userAccessToken())

	resp, err := c.cloud.HTTPClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("token rotation failed: %d %s", resp.StatusCode, string(body))
	}

	var data struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return err
	}

	dev.DeviceToken = data.Token
	if err := SaveDevice(dev); err != nil {
		return err
	}
	return nil
}

func (c *Client) Heartbeat() error {
	dev, err := LoadDevice()
	if err != nil || dev == nil {
		return fmt.Errorf("device not registered")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	reqBody := map[string]any{
		"deviceID": dev.DeviceID,
		"version":  version.Get(),
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return err
	}

	url := c.cloud.URL(cloud.DeviceHeartbeatPath(dev.DeviceID), dev.BaseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	c.cloud.SetDeviceAuthHeadersWithUser(req, dev.DeviceToken, userAccessToken())

	resp, err := c.cloud.HTTPClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("heartbeat failed: %d %s", resp.StatusCode, string(respBody))
	}
	return nil
}

func (c *Client) SetOnline() error {
	return nil
}
