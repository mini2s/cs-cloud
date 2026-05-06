package device

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCheckGatewayConnectivity_Success(t *testing.T) {
	gatewaySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
			return
		}
		http.NotFound(w, r)
	}))
	defer gatewaySrv.Close()

	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/cloud-api/cloud/device/gateway-assign" {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]string{"gatewayURL": gatewaySrv.URL})
			return
		}
		http.NotFound(w, r)
	}))
	defer apiSrv.Close()

	dev := &DeviceInfo{
		DeviceID:    "test-device",
		DeviceToken: "test-token",
		BaseURL:     apiSrv.URL,
	}

	err := CheckGatewayConnectivity(context.Background(), dev)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestCheckGatewayConnectivity_AssignGatewayFails(t *testing.T) {
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte("service unavailable"))
	}))
	defer apiSrv.Close()

	dev := &DeviceInfo{
		DeviceID:    "test-device",
		DeviceToken: "test-token",
		BaseURL:     apiSrv.URL,
	}

	err := CheckGatewayConnectivity(context.Background(), dev)
	if err == nil {
		t.Fatal("expected error when gateway-assign fails")
	}
}

func TestCheckGatewayConnectivity_GatewayUnreachable(t *testing.T) {
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/cloud-api/cloud/device/gateway-assign" {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]string{"gatewayURL": "http://127.0.0.1:1"})
			return
		}
		http.NotFound(w, r)
	}))
	defer apiSrv.Close()

	dev := &DeviceInfo{
		DeviceID:    "test-device",
		DeviceToken: "test-token",
		BaseURL:     apiSrv.URL,
	}

	err := CheckGatewayConnectivity(context.Background(), dev)
	if err == nil {
		t.Fatal("expected error when gateway is unreachable")
	}
}

func TestCheckGatewayConnectivity_GatewayReturns500(t *testing.T) {
	gatewaySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer gatewaySrv.Close()

	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/cloud-api/cloud/device/gateway-assign" {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]string{"gatewayURL": gatewaySrv.URL})
			return
		}
		http.NotFound(w, r)
	}))
	defer apiSrv.Close()

	dev := &DeviceInfo{
		DeviceID:    "test-device",
		DeviceToken: "test-token",
		BaseURL:     apiSrv.URL,
	}

	err := CheckGatewayConnectivity(context.Background(), dev)
	if err == nil {
		t.Fatal("expected error when gateway returns 500")
	}
}

func TestCheckGatewayConnectivity_AuthHeadersSent(t *testing.T) {
	gatewaySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	}))
	defer gatewaySrv.Close()

	var receivedAuth string
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"gatewayURL": gatewaySrv.URL})
	}))
	defer apiSrv.Close()

	dev := &DeviceInfo{
		DeviceID:    "test-device",
		DeviceToken: "my-secret-token",
		BaseURL:     apiSrv.URL,
	}

	err := CheckGatewayConnectivity(context.Background(), dev)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if receivedAuth != "Bearer my-secret-token" {
		t.Errorf("expected auth header 'Bearer my-secret-token', got %q", receivedAuth)
	}
}

func TestCheckGatewayConnectivity_ContextCancelled(t *testing.T) {
	blockCh := make(chan struct{})
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-blockCh
		w.WriteHeader(http.StatusOK)
	}))
	defer apiSrv.Close()
	defer close(blockCh)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	dev := &DeviceInfo{
		DeviceID:    "test-device",
		DeviceToken: "test-token",
		BaseURL:     apiSrv.URL,
	}

	err := CheckGatewayConnectivity(ctx, dev)
	if err == nil {
		t.Fatal("expected error when context is cancelled")
	}
}
