package localserver

import (
	"net/http"

	"cs-cloud/internal/agent"
	"cs-cloud/internal/logger"
)

// @Summary      List agent commands
// @Description  Returns the command manifest including built-in and agent-provided slash commands.
// @Tags         Agent
// @Produce      json
// @Param        include  query  string  false  "Comma-separated scopes to include"
// @Success      200  {object}  envelope{data=map[string]any}
// @Failure      500  {object}  envelope
// @Router       /agents/commands [get]
func (s *Server) handleCommands(w http.ResponseWriter, r *http.Request) {
	scopes := agent.ParseIncludeScopes(r.URL.Query().Get("include"))

	var agentCmds []agent.SlashCommand

	endpoint := s.manager.Endpoint()
	if endpoint != "" {
		backend := s.manager.DefaultBackend()
		d, err := s.manager.ResolveDriver(backend)
		if err != nil {
			logger.Warn("no driver for backend %s, falling back to builtin only: %v", backend, err)
		} else {
			cmds, err := d.FetchCommands(endpoint)
			if err != nil {
				logger.Warn("failed to fetch agent commands, falling back to builtin only: %v", err)
			} else {
				agentCmds = cmds
			}
		}
	} else {
		logger.Warn("no agent endpoint available, returning builtin commands only")
	}

	manifest, err := agent.BuildManifest(scopes, agentCmds)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, manifest)
}
