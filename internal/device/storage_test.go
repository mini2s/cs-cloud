package device

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"cs-cloud/internal/platform"
	"cs-cloud/internal/provider"
)

func resetDeviceIDCache() {
	cachedDeviceID = ""
	cachedDeviceIDOnce.Do(func() {})
	cachedDeviceIDOnce = *new(sync.Once)
}

func setupTestDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	platform.SetDataDir(dir)
	t.Cleanup(func() {
		platform.SetDataDir("")
		resetDeviceIDCache()
	})
	return dir
}

func TestGetDeviceID_NonEmpty(t *testing.T) {
	resetDeviceIDCache()
	id := GetDeviceID()
	if id == "" {
		t.Fatal("GetDeviceID() returned empty string")
	}
	if len(id) != 64 {
		t.Fatalf("expected 64-char hex string, got %d chars: %s", len(id), id)
	}
}

func TestGetDeviceID_MatchesGenerateMachineID(t *testing.T) {
	resetDeviceIDCache()
	got := GetDeviceID()
	want := provider.GenerateMachineID()
	if got != want {
		t.Fatalf("GetDeviceID() = %q, want %q (GenerateMachineID)", got, want)
	}
}

func TestGetDeviceID_CachedConsistently(t *testing.T) {
	resetDeviceIDCache()
	first := GetDeviceID()
	second := GetDeviceID()
	if first != second {
		t.Fatalf("GetDeviceID() returned different values: %q then %q", first, second)
	}
}

func TestLoadDevice_FileNotExist(t *testing.T) {
	setupTestDir(t)
	info, err := LoadDevice()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info != nil {
		t.Fatal("expected nil when device file does not exist")
	}
}

func TestLoadDevice_NoDeviceToken(t *testing.T) {
	_ = setupTestDir(t)
	shareDir := filepath.Join(platform.CoStrictShareDir())
	if err := os.MkdirAll(shareDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := `{"device_id":"fake-id","device_token":"","auth_user_id":"user1"}`
	if err := os.WriteFile(filepath.Join(shareDir, "device.json"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := LoadDevice()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info != nil {
		t.Fatal("expected nil when device_token is empty")
	}
}

func TestLoadDevice_IgnoresStoredID(t *testing.T) {
	dir := setupTestDir(t)
	expectedID := provider.GenerateMachineID()
	content := `{"device_id":"tampered-id","device_token":"valid-token","auth_user_id":"user1"}`
	shareDir := filepath.Join(dir, "share")
	if err := os.MkdirAll(shareDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shareDir, "device.json"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := LoadDevice()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info == nil {
		t.Fatal("expected non-nil DeviceInfo")
	}
	if info.DeviceID == "tampered-id" {
		t.Fatal("LoadDevice() should ignore stored device_id")
	}
	if info.DeviceID != expectedID {
		t.Fatalf("DeviceID = %q, want %q", info.DeviceID, expectedID)
	}
	if info.DeviceToken != "valid-token" {
		t.Fatalf("DeviceToken = %q, want %q", info.DeviceToken, "valid-token")
	}
}

func TestSaveDevice_WritesGeneratedID(t *testing.T) {
	dir := setupTestDir(t)
	info := &DeviceInfo{
		DeviceID:    "fake-id",
		DeviceToken: "my-token",
		AuthUserID:  "user1",
		BaseURL:     "https://example.com",
	}
	if err := SaveDevice(info); err != nil {
		t.Fatalf("SaveDevice() error: %v", err)
	}
	expectedID := provider.GenerateMachineID()
	if info.DeviceID != expectedID {
		t.Fatalf("in-memory DeviceID = %q, want %q", info.DeviceID, expectedID)
	}
	data, err := os.ReadFile(filepath.Join(dir, "share", "device.json"))
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	var saved DeviceInfo
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if saved.DeviceID != expectedID {
		t.Fatalf("saved DeviceID = %q, want %q", saved.DeviceID, expectedID)
	}
}

func TestSaveDevice_LoadDevice_Roundtrip(t *testing.T) {
	_ = setupTestDir(t)
	original := &DeviceInfo{
		DeviceID:    "should-be-overwritten",
		DeviceToken: "roundtrip-token",
		AuthUserID:  "user1",
		BaseURL:     "https://example.com",
	}
	if err := SaveDevice(original); err != nil {
		t.Fatalf("SaveDevice() error: %v", err)
	}
	loaded, err := LoadDevice()
	if err != nil {
		t.Fatalf("LoadDevice() error: %v", err)
	}
	expectedID := provider.GenerateMachineID()
	if loaded.DeviceID != expectedID {
		t.Fatalf("roundtrip DeviceID = %q, want %q", loaded.DeviceID, expectedID)
	}
	if loaded.DeviceToken != "roundtrip-token" {
		t.Fatalf("roundtrip DeviceToken = %q, want %q", loaded.DeviceToken, "roundtrip-token")
	}
	if loaded.AuthUserID != "user1" {
		t.Fatalf("roundtrip AuthUserID = %q, want %q", loaded.AuthUserID, "user1")
	}
}
