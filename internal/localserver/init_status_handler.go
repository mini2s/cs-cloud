package localserver

import (
	"net/http"
	"path/filepath"
	"time"
)

type initStatusAgent struct {
	State   string `json:"state"`
	Healthy bool   `json:"healthy"`
}

type initStatusPrewarm struct {
	Status     string `json:"status"`
	StartedAt  string `json:"started_at,omitempty"`
	FinishedAt string `json:"finished_at,omitempty"`
	Error      string `json:"error,omitempty"`
}

type initStatusData struct {
	Directory string            `json:"directory"`
	Ready     bool              `json:"ready"`
	Agent     initStatusAgent   `json:"agent"`
	Prewarm   initStatusPrewarm `json:"prewarm"`
}

// @Summary      Get directory initialization status
// @Description  Returns the initialization/warmup status for a specific workspace directory. Use X-Workspace-Directory header or directory query parameter. If omitted, returns status for all tracked directories.
// @Tags         Runtime
// @Produce      json
// @Param        directory  query  string  false  "Workspace directory to check"
// @Success      200  {object}  envelope{data=initStatusData}
// @Failure      400  {object}  envelope
// @Router       /runtime/init-status [get]
func (s *Server) handleInitStatus(w http.ResponseWriter, r *http.Request) {
	dir := r.URL.Query().Get("directory")
	if dir == "" {
		dir = getWorkspaceDir(r)
	}

	if dir != "" {
		abs, err := filepath.Abs(filepath.Clean(dir))
		if err != nil {
			writeErr(w, http.StatusBadRequest, "BAD_REQUEST", "invalid directory: "+err.Error())
			return
		}
		dir = abs
	}

	if dir == "" {
		s.handleInitStatusAll(w, r)
		return
	}

	writeOK(w, s.buildInitStatus(dir))
}

func (s *Server) handleInitStatusAll(w http.ResponseWriter, _ *http.Request) {
	states := s.AllPrewarmStates()
	items := make([]initStatusData, 0, len(states))
	for dir := range states {
		items = append(items, s.buildInitStatus(dir))
	}
	writeOK(w, map[string]any{"directories": items})
}

func (s *Server) buildInitStatus(dir string) initStatusData {
	agentInfo := s.buildAgentInfo()
	prewarmInfo := s.buildPrewarmInfo(dir)

	if prewarmInfo.Status == "" {
		s.TriggerPrewarmIfNeeded(dir)
		prewarmInfo = s.buildPrewarmInfo(dir)
	}

	ready := agentInfo.Healthy &&
		(prewarmInfo.Status == "completed" || prewarmInfo.Status == "")

	return initStatusData{
		Directory: dir,
		Ready:     ready,
		Agent:     agentInfo,
		Prewarm:   prewarmInfo,
	}
}

func (s *Server) buildAgentInfo() initStatusAgent {
	agents := s.manager.ListAgents()
	if len(agents) == 0 {
		return initStatusAgent{State: "none", Healthy: false}
	}
	a := agents[0]
	state := a.State().String()
	healthy := state == "connected" || state == "session_active"
	return initStatusAgent{State: state, Healthy: healthy}
}

func (s *Server) buildPrewarmInfo(dir string) initStatusPrewarm {
	st := s.GetPrewarmState(dir)
	if st == nil {
		return initStatusPrewarm{Status: ""}
	}
	info := initStatusPrewarm{
		Status: st.Status,
		Error:  st.Error,
	}
	if st.StartedAt != nil {
		info.StartedAt = st.StartedAt.Format(time.RFC3339)
	}
	if st.FinishedAt != nil {
		info.FinishedAt = st.FinishedAt.Format(time.RFC3339)
	}
	return info
}
