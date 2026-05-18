package device

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"cs-cloud/internal/platform"
	"cs-cloud/internal/provider"
)

var (
	cachedDeviceID        string
	cachedDeviceIDOnce    sync.Once
	cachedLegacyDeviceID  string
	cachedLegacyOnce      sync.Once
)

func GetDeviceID() string {
	cachedDeviceIDOnce.Do(func() {
		cachedDeviceID = provider.GenerateMachineID()
	})
	return cachedDeviceID
}

func GetLegacyDeviceID() string {
	cachedLegacyOnce.Do(func() {
		cachedLegacyDeviceID = provider.GenerateLegacyMachineID()
	})
	return cachedLegacyDeviceID
}

type DeviceInfo struct {
	DeviceID     string `json:"device_id"`
	DeviceToken  string `json:"device_token"`
	AuthUserID   string `json:"auth_user_id"`
	RegisteredAt string `json:"registered_at"`
	BaseURL      string `json:"base_url"`
}

func DevicePath() (string, error) {
	return filepath.Join(platform.CoStrictShareDir(), "device.json"), nil
}

func LoadDevice() (*DeviceInfo, error) {
	p, err := DevicePath()
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
	var info DeviceInfo
	if err := json.Unmarshal(b, &info); err != nil {
		return nil, fmt.Errorf("decode device file: %w", err)
	}
	if info.DeviceToken == "" {
		return nil, nil
	}
	info.DeviceID = GetDeviceID()
	return &info, nil
}

func SaveDevice(info *DeviceInfo) error {
	if info != nil {
		info.DeviceID = GetDeviceID()
	}
	p, err := DevicePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return fmt.Errorf("create device dir: %w", err)
	}
	b, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return fmt.Errorf("encode device file: %w", err)
	}
	if err := os.WriteFile(p, b, 0o600); err != nil {
		return fmt.Errorf("write device file: %w", err)
	}
	return nil
}
