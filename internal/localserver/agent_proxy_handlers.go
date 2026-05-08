package localserver

import "net/http"

// @Summary      List available models
// @Description  Proxies to the agent backend to retrieve the list of available AI models.
// @Tags         Agent
// @Produce      json
// @Success      200  {object}  envelope{data=map[string]any}
// @Failure      503  {object}  envelope
// @Router       /agents/models [get]
func (s *Server) handleAgentModels(w http.ResponseWriter, r *http.Request) {
	s.handleProxy(w, r)
}

// @Summary      List session modes
// @Description  Proxies to the agent backend to retrieve supported session modes.
// @Tags         Agent
// @Produce      json
// @Success      200  {object}  envelope{data=map[string]any}
// @Failure      503  {object}  envelope
// @Router       /agents/session-modes [get]
func (s *Server) handleAgentSessionModes(w http.ResponseWriter, r *http.Request) {
	s.handleProxy(w, r)
}

// @Summary      List MCP servers
// @Description  Proxies to the agent backend to retrieve configured MCP server entries.
// @Tags         Agent
// @Produce      json
// @Success      200  {object}  envelope{data=map[string]any}
// @Failure      503  {object}  envelope
// @Router       /agents/mcp [get]
func (s *Server) handleAgentMCP(w http.ResponseWriter, r *http.Request) {
	s.handleProxy(w, r)
}

// @Summary      List LSP servers
// @Description  Proxies to the agent backend to retrieve configured LSP server entries.
// @Tags         Agent
// @Produce      json
// @Success      200  {object}  envelope{data=map[string]any}
// @Failure      503  {object}  envelope
// @Router       /agents/lsp [get]
func (s *Server) handleAgentLSP(w http.ResponseWriter, r *http.Request) {
	s.handleProxy(w, r)
}
