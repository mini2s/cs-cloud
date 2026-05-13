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
	sendHeartbeat func(connected bool)
	done          chan struct{}
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
}

func (m *Manager) IsConnected() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.connected
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
		_ = Connect(tunnelCtx, localPort, cfg, mgr.sendHeartbeat)

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
