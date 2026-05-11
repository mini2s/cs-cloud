package localserver

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"cs-cloud/internal/logger"
)

const findFilesCacheTTL = 30 * time.Second

type fileSearchIndex struct {
	workspace string
	files     []string
	dirs      []string
	builtAt   time.Time
}

type fileSearchBuild struct {
	done chan struct{}
	idx  *fileSearchIndex
	err  error
}

type rankedPath struct {
	rel        string
	isDir      bool
	score      int
	depth      int
	pathLength int
}

var skipDirs = map[string]bool{
	"node_modules": true,
	".git":         true,
	".svn":         true,
	".hg":          true,
	"__pycache__":  true,
	".next":        true,
	".nuxt":        true,
	"dist":         true,
	"build":        true,
	"target":       true,
	".cache":       true,
	".tox":         true,
	".venv":        true,
	"venv":         true,
	".idea":        true,
	".turbo":       true,
	"coverage":     true,
	".sst":         true,
}

// @Summary      Search files by name
// @Description  Fuzzy file search by name. Automatically skips common directories (node_modules, .git, dist, build, etc.).
// @Tags         Runtime
// @Produce      json
// @Param        query      query  string  false  "Search keyword (case-insensitive)"
// @Param        directory  query  string  false  "Target directory (relative to workspace root)"  default(.)
// @Param        dirs       query  string  false  "Include directories in results"  Enums(true, false)  default(true)
// @Param        limit      query  int     false  "Max results"  minimum(1)  maximum(200)  default(10)
// @Success      200  {object}  envelope{data=[]string}
// @Failure      400  {object}  envelope
// @Router       /runtime/find/file [get]
func (s *Server) handleFindFiles(w http.ResponseWriter, r *http.Request) {
	begin := time.Now()
	query := r.URL.Query().Get("query")
	dirs := r.URL.Query().Get("dirs")
	limitStr := r.URL.Query().Get("limit")

	limit := 10
	if n, err := strconv.Atoi(limitStr); err == nil && n > 0 && n <= 200 {
		limit = n
	}

	includeDirs := dirs != "false"

	dir := r.URL.Query().Get("directory")
	if dir == "" {
		dir = "."
	}

	absDir, _, err := s.resolvePath(r, dir)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "BAD_REQUEST", err.Error())
		return
	}

	info, err := os.Stat(absDir)
	if err != nil || !info.IsDir() {
		writeErr(w, http.StatusBadRequest, "BAD_REQUEST", fmt.Sprintf("directory not found: %s", absDir))
		return
	}

	idx, err := s.findFilesIndex(absDir)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}

	results := searchIndexedPaths(idx, absDir, query, includeDirs, limit)
	logger.Info("runtime find/file workspace=%s query=%q dirs=%t limit=%d results=%d files=%d dirsCount=%d cost=%s",
		absDir, query, includeDirs, limit, len(results), len(idx.files), len(idx.dirs), time.Since(begin))

	writeOK(w, results)
}

func (s *Server) findFilesIndex(absDir string) (*fileSearchIndex, error) {
	if s.findFilesCache == nil {
		s.findFilesCache = make(map[string]*fileSearchIndex)
	}
	if s.findFilesBuilds == nil {
		s.findFilesBuilds = make(map[string]*fileSearchBuild)
	}

	now := time.Now()
	s.findFilesMu.Lock()
	if idx, ok := s.findFilesCache[absDir]; ok && now.Sub(idx.builtAt) < findFilesCacheTTL {
		s.findFilesMu.Unlock()
		logger.Info("runtime find/file cache hit workspace=%s age=%s files=%d dirs=%d", absDir, now.Sub(idx.builtAt), len(idx.files), len(idx.dirs))
		return idx, nil
	}
	if build, ok := s.findFilesBuilds[absDir]; ok {
		s.findFilesMu.Unlock()
		logger.Info("runtime find/file cache wait workspace=%s", absDir)
		<-build.done
		return build.idx, build.err
	}
	build := &fileSearchBuild{done: make(chan struct{})}
	s.findFilesBuilds[absDir] = build
	s.findFilesMu.Unlock()
	logger.Info("runtime find/file cache miss workspace=%s", absDir)

	idx, err := buildFileSearchIndex(absDir)

	s.findFilesMu.Lock()
	defer s.findFilesMu.Unlock()
	delete(s.findFilesBuilds, absDir)
	if err == nil {
		s.findFilesCache[absDir] = idx
	}
	build.idx = idx
	build.err = err
	close(build.done)
	return idx, err
}

func (s *Server) invalidateFindFilesCache(workspace string) {
	s.findFilesMu.Lock()
	defer s.findFilesMu.Unlock()
	if workspace == "" {
		s.findFilesCache = make(map[string]*fileSearchIndex)
		logger.Info("runtime find/file cache invalidated all")
		return
	}
	delete(s.findFilesCache, workspace)
	logger.Info("runtime find/file cache invalidated workspace=%s", workspace)
}

func buildFileSearchIndex(absDir string) (*fileSearchIndex, error) {
	begin := time.Now()
	idx := &fileSearchIndex{
		workspace: absDir,
		files:     make([]string, 0, 256),
		dirs:      make([]string, 0, 64),
		builtAt:   time.Now(),
	}

	err := filepath.WalkDir(absDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}

		rel, err := filepath.Rel(absDir, path)
		if err != nil || rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)

		if shouldSkip(rel) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if d.IsDir() {
			idx.dirs = append(idx.dirs, rel+"/")
			return nil
		}

		idx.files = append(idx.files, rel)
		return nil
	})
	if err != nil {
		return nil, err
	}

	logger.Info("runtime find/file index built workspace=%s files=%d dirs=%d cost=%s", absDir, len(idx.files), len(idx.dirs), time.Since(begin))

	return idx, nil
}

func searchIndexedPaths(idx *fileSearchIndex, absDir string, query string, includeDirs bool, limit int) []string {
	normalizedQuery := filepath.ToSlash(strings.ToLower(query))
	matchRelativePath := strings.Contains(normalizedQuery, "/")

	items := idx.files
	if includeDirs {
		items = make([]string, 0, len(idx.files)+len(idx.dirs))
		items = append(items, idx.files...)
		items = append(items, idx.dirs...)
	}

	ranked := make([]rankedPath, 0, len(items))
	for _, rel := range items {
		trimmed := strings.TrimSuffix(rel, "/")
		candidate := strings.ToLower(filepath.Base(trimmed))
		if matchRelativePath {
			candidate = strings.ToLower(rel)
		}
		isDir := strings.HasSuffix(rel, "/")
		if isDir {
			candidate += "/"
		}

		if normalizedQuery != "" && !strings.Contains(candidate, normalizedQuery) {
			continue
		}

		ranked = append(ranked, rankedPath{
			rel:        rel,
			isDir:      isDir,
			score:      matchScore(rel, normalizedQuery, matchRelativePath),
			depth:      strings.Count(trimmed, "/"),
			pathLength: len(trimmed),
		})
	}

	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].score != ranked[j].score {
			return ranked[i].score > ranked[j].score
		}
		if ranked[i].depth != ranked[j].depth {
			return ranked[i].depth < ranked[j].depth
		}
		if ranked[i].pathLength != ranked[j].pathLength {
			return ranked[i].pathLength < ranked[j].pathLength
		}
		return ranked[i].rel < ranked[j].rel
	})

	results := make([]string, 0, min(limit, len(ranked)))
	for _, item := range ranked {
		results = append(results, filepath.Join(absDir, filepath.FromSlash(strings.TrimSuffix(item.rel, "/"))))
		if len(results) >= limit {
			break
		}
	}

	return results
}

func matchScore(rel string, query string, matchRelativePath bool) int {
	if query == "" {
		return 1
	}

	trimmed := strings.TrimSuffix(rel, "/")
	base := strings.ToLower(filepath.Base(trimmed))
	full := strings.ToLower(rel)

	if matchRelativePath {
		score := 10
		switch {
		case full == query:
			score = 120
		case strings.HasPrefix(full, query):
			score = 100
		case strings.Contains(full, "/"+query):
			score = 80
		case strings.Contains(full, query):
			score = 60
		}
		if strings.HasSuffix(rel, "/") {
			score += 5
		}
		return score
	}

	score := 10
	switch {
	case base == query:
		score = 120
	case strings.HasPrefix(base, query):
		score = 100
	case strings.Contains(base, query):
		score = 80
	case strings.Contains(full, query):
		score = 60
	}
	if strings.HasSuffix(rel, "/") {
		score += 5
	}
	return score
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func shouldSkip(rel string) bool {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	for _, part := range parts {
		if skipDirs[part] {
			return true
		}
	}
	return false
}
