package localserver

import (
	"net/http"
	"time"
)

type healthData struct {
	Status  string     `json:"status" example:"ok"`
	Uptime  int64      `json:"uptime" example:"12345"`
	Version string     `json:"version" example:"1.0.0"`
	Tunnel  *tunnelData `json:"tunnel,omitempty"`
}

type tunnelData struct {
	Connected   bool       `json:"connected"`
	ConnectedAt *time.Time `json:"connected_at,omitempty"`
}

// @Summary      Health check
// @Description  Returns server health status, uptime, version and tunnel connectivity.
// @Tags         Runtime
// @Produce      json
// @Success      200  {object}  envelope{data=healthData}
// @Router       /runtime/health [get]
func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	data := healthData{
		Status:  "ok",
		Uptime:  int64(time.Since(startTime).Seconds()),
		Version: s.version,
	}
	if s.tunnelStatus != nil {
		ts := s.tunnelStatus.TunnelStatus()
		data.Tunnel = &tunnelData{
			Connected:   ts.Connected,
			ConnectedAt: ts.ConnectedAt,
		}
	}
	writeOK(w, data)
}
