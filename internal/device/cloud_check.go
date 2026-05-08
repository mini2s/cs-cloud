package device

import (
	"context"
	"fmt"
	"io"
	"net/http"

	"cs-cloud/internal/cloud"
)

func IsDeviceRegisteredOnCloud(ctx context.Context, dev *DeviceInfo, userAccessToken string) (bool, error) {
	if dev == nil {
		return false, fmt.Errorf("no device info")
	}

	cc := cloud.NewClient(nil)
	url := cc.URL(cloud.DeviceGetPath(dev.DeviceID), dev.BaseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, err
	}
	cc.SetUserAuthHeaders(req, userAccessToken)

	resp, err := cc.HTTPClient().Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusOK:
		return true, nil
	case resp.StatusCode == http.StatusNotFound:
		return false, nil
	default:
		body, _ := io.ReadAll(resp.Body)
		return false, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(body))
	}
}
