package localserver

import (
	"fmt"
	"net/http"
	"os"
)

type fileWriteRequest struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// @Summary      Write file content
// @Description  Write full file content. Path is sandboxed to the workspace directory unless absolute paths are enabled.
// @Tags         Runtime
// @Accept       json
// @Produce      json
// @Param        request  body  fileWriteRequest  true  "File write request"
// @Success      200  {object}  envelope{data=fileMetaData}
// @Failure      400  {object}  envelope
// @Failure      404  {object}  envelope
// @Router       /runtime/files/content [put]
func (s *Server) handleFileWrite(w http.ResponseWriter, r *http.Request) {
	var req fileWriteRequest
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "BAD_REQUEST", err.Error())
		return
	}
	if req.Path == "" {
		writeErr(w, http.StatusBadRequest, "BAD_REQUEST", "path is required")
		return
	}

	absPath, _, err := s.resolvePath(r, req.Path)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "BAD_REQUEST", err.Error())
		return
	}
	info, err := os.Stat(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			writeErr(w, http.StatusNotFound, "NOT_FOUND", fmt.Sprintf("file not found: %s", absPath))
			return
		}
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	if info.IsDir() {
		writeErr(w, http.StatusBadRequest, "BAD_REQUEST", fmt.Sprintf("not a file: %s", absPath))
		return
	}

	if err := os.WriteFile(absPath, []byte(req.Content), info.Mode()); err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	updated, err := os.Stat(absPath)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}

	writeOK(w, fileMetaData{
		Path:     absPath,
		Size:     updated.Size(),
		Modified: updated.ModTime().UTC(),
		Type:     "file",
	})
}
