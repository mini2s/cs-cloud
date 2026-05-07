package localserver

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"cs-cloud/internal/app"
	"cs-cloud/internal/device"
	"cs-cloud/internal/logger"
	"cs-cloud/internal/updater"
)

type reconnecter interface {
	Reconnect()
}

type commandEntry struct {
	req         *commandRequest
	status      string
	startedAt   time.Time
	completedAt time.Time
	result      any
	errMsg      string
}

type CommandDispatcher struct {
	mu        sync.Mutex
	active    map[string]*commandEntry
	updater   *updater.Manager
	tunnel    reconnecter
	restarter func()
	reporter  *CommandReporter
	app       *app.App
}

func NewCommandDispatcher(a *app.App, reporter *CommandReporter) *CommandDispatcher {
	return &CommandDispatcher{
		active:   make(map[string]*commandEntry),
		app:      a,
		reporter: reporter,
	}
}

func (d *CommandDispatcher) BindUpdater(u *updater.Manager) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.updater = u
}

func (d *CommandDispatcher) BindTunnel(m reconnecter) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.tunnel = m
}

func (d *CommandDispatcher) BindRestarter(fn func()) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.restarter = fn
}

func (d *CommandDispatcher) BindDeviceClient(c *device.Client) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.reporter != nil {
		d.reporter.deviceClient = c
	}
}

func (d *CommandDispatcher) Dispatch(ctx context.Context, req *commandRequest) (*commandAck, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if _, exists := d.active[req.CommandID]; exists {
		return nil, fmt.Errorf("command %s already exists", req.CommandID)
	}

	for _, entry := range d.active {
		if entry.status == "executing" || entry.status == "accepted" {
			return nil, fmt.Errorf("command %s is already %s", entry.req.CommandID, entry.status)
		}
	}

	entry := &commandEntry{
		req:       req,
		status:    "accepted",
		startedAt: time.Now(),
	}
	d.active[req.CommandID] = entry

	go d.execute(req)

	return writeCommandAck(req.CommandID, "accepted", fmt.Sprintf("%s scheduled", req.Type)), nil
}

func (d *CommandDispatcher) Status(commandID string) (*commandStatusResponse, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	entry, exists := d.active[commandID]
	if !exists {
		return nil, fmt.Errorf("command %s not found", commandID)
	}

	return writeCommandStatus(entry.req, entry.status, entry.errMsg, entry.startedAt, entry.completedAt, entry.result), nil
}

func (d *CommandDispatcher) HandleHeartbeatCommands(cmds []device.CloudCommand) {
	for _, cmd := range cmds {
		req := &commandRequest{
			CommandID: cmd.CommandID,
			Type:      cmd.Type,
			Payload:   cmd.Payload,
			Timestamp: cmd.Timestamp,
		}
		if _, err := d.Dispatch(context.Background(), req); err != nil {
			logger.Warn("[dispatcher] heartbeat command %s rejected: %v", cmd.CommandID, err)
		}
	}
}

func (d *CommandDispatcher) execute(req *commandRequest) {
	d.mu.Lock()
	entry := d.active[req.CommandID]
	entry.status = "executing"
	d.mu.Unlock()

	logger.Info("[dispatcher] executing command %s (type=%s)", req.CommandID, req.Type)

	var result any
	var err error

	switch req.Type {
	case "upgrade":
		result, err = d.execUpgrade(req)
	case "restart":
		result, err = d.execRestart(req)
	case "reconnect":
		result, err = d.execReconnect(req)
	default:
		err = fmt.Errorf("unknown command type: %s", req.Type)
	}

	d.mu.Lock()
	if err != nil {
		entry.status = "failed"
		entry.errMsg = err.Error()
		logger.Error("[dispatcher] command %s failed: %v", req.CommandID, err)
	} else {
		entry.status = "completed"
		entry.result = result
		logger.Info("[dispatcher] command %s completed", req.CommandID)
	}
	entry.completedAt = time.Now()
	d.mu.Unlock()

	go d.reportResult(req.CommandID, entry)
}

func (d *CommandDispatcher) execUpgrade(req *commandRequest) (any, error) {
	d.mu.Lock()
	u := d.updater
	d.mu.Unlock()
	if u == nil {
		return nil, fmt.Errorf("updater not available")
	}

	var payload struct {
		Version     string `json:"version"`
		Force       bool   `json:"force"`
		DownloadURL string `json:"download_url"`
		SHA256      string `json:"sha256"`
	}
	if len(req.Payload) > 0 {
		if err := json.Unmarshal(req.Payload, &payload); err != nil {
			return nil, fmt.Errorf("invalid payload: %w", err)
		}
	}

	if payload.DownloadURL != "" {
		result, err := u.CheckNow(context.Background())
		if err != nil {
			return nil, err
		}
		if result.DownloadURL == "" {
			return nil, fmt.Errorf("no update available")
		}
	}

	if err := u.Apply(context.Background(), payload.Version); err != nil {
		return nil, err
	}

	return map[string]string{"status": "upgrade completed, restart pending"}, nil
}

func (d *CommandDispatcher) execRestart(_ *commandRequest) (any, error) {
	d.mu.Lock()
	fn := d.restarter
	d.mu.Unlock()
	if fn == nil {
		return nil, fmt.Errorf("restarter not available")
	}

	go func() {
		time.Sleep(1 * time.Second)
		fn()
	}()

	return map[string]string{"status": "restart scheduled"}, nil
}

func (d *CommandDispatcher) execReconnect(_ *commandRequest) (any, error) {
	d.mu.Lock()
	m := d.tunnel
	d.mu.Unlock()
	if m == nil {
		return nil, fmt.Errorf("tunnel manager not available")
	}

	m.Reconnect()
	return map[string]string{"status": "reconnect initiated"}, nil
}

func (d *CommandDispatcher) reportResult(commandID string, entry *commandEntry) {
	if d.reporter == nil {
		return
	}

	result := &commandStatusResponse{
		CommandID:   commandID,
		Type:        entry.req.Type,
		Status:      entry.status,
		Result:      entry.result,
	}
	if !entry.startedAt.IsZero() {
		result.StartedAt = entry.startedAt.Format(time.RFC3339)
	}
	if !entry.completedAt.IsZero() {
		result.CompletedAt = entry.completedAt.Format(time.RFC3339)
	}
	if entry.errMsg != "" {
		result.Error = entry.errMsg
	}

	d.reporter.Report(commandID, result)
}

func (d *CommandDispatcher) Cleanup(maxAge time.Duration) {
	d.mu.Lock()
	defer d.mu.Unlock()

	now := time.Now()
	for id, entry := range d.active {
		if entry.status != "executing" && now.Sub(entry.completedAt) > maxAge {
			delete(d.active, id)
		}
	}
}
