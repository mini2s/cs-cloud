package tunnel

import (
	"context"
	"sync"
	"time"

	"cs-cloud/internal/config"
	"cs-cloud/internal/logger"
)

type Manager struct {
	mu            sync.Mutex
	cancel        context.CancelFunc
	localPort     int
	connected     bool
	connectedAt   *time.Time
	sendHeartbeat func(connected bool)
	done          chan struct{}
}

type TunnelStatus struct {
	Connected   bool       `json:"connected"`
	ConnectedAt *time.Time `json:"connected_at,omitempty"`
}

func NewManager() *Manager {
	return &Manager{
		done: make(chan struct{}),
	}
}

func (m *Manager) SetSendHeartbeat(fn func(connected bool)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sendHeartbeat = fn
}

func (m *Manager) SetCancel(cancel context.CancelFunc) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cancel = cancel
}

func (m *Manager) SetConnected(v bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.connected = v
	if v {
		now := time.Now()
		m.connectedAt = &now
	} else {
		m.connectedAt = nil
	}
}

func (m *Manager) IsConnected() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.connected
}

func (m *Manager) Status() TunnelStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	return TunnelStatus{
		Connected:   m.connected,
		ConnectedAt: m.connectedAt,
	}
}

func (m *Manager) Reconnect() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cancel != nil {
		logger.Info("[tunnel-manager] cancelling current tunnel connection for reconnect")
		m.cancel()
		m.cancel = nil
		m.connected = false
	}
}

func (m *Manager) Stop() {
	m.mu.Lock()
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
		m.connected = false
	}
	done := m.done
	m.mu.Unlock()

	if done != nil {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			logger.Warn("[tunnel-manager] timed out waiting for tunnel to stop")
		}
	}
}

func RunManagedTunnel(ctx context.Context, localPort int, mgr *Manager, cfg *config.Config) {
	defer func() {
		if ch := func() chan struct{} {
			mgr.mu.Lock()
			defer mgr.mu.Unlock()
			d := mgr.done
			mgr.done = nil
			return d
		}(); ch != nil {
			close(ch)
		}
	}()

	onSessionChange := func(connected bool) {
		mgr.SetConnected(connected)
		mgr.mu.Lock()
		fn := mgr.sendHeartbeat
		mgr.mu.Unlock()
		if fn != nil {
			fn(connected)
		}
	}

	for {
		select {
		case <-ctx.Done():
			logger.Info("[tunnel-manager] tunnel manager stopped")
			return
		default:
		}

		tunnelCtx, cancel := context.WithCancel(ctx)
		mgr.SetCancel(cancel)
		mgr.SetConnected(false)

		logger.Info("[tunnel-manager] starting tunnel connection (port=%d)", localPort)
		_ = Connect(tunnelCtx, localPort, cfg, onSessionChange)

		mgr.SetConnected(false)

		select {
		case <-ctx.Done():
			logger.Info("[tunnel-manager] tunnel manager stopped")
			return
		case <-time.After(time.Second):
		}

		logger.Info("[tunnel-manager] tunnel disconnected, reconnecting...")
	}
}
