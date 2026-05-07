package localserver

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"time"

	"cs-cloud/internal/logger"
)

func (s *Server) handleProxy(w http.ResponseWriter, r *http.Request) {
	endpoint := s.manager.Endpoint()
	if endpoint == "" {
		writeErr(w, http.StatusServiceUnavailable, "UNAVAILABLE", "no agent backend available")
		return
	}

	targetURL, err := url.Parse(endpoint)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", "invalid backend endpoint")
		return
	}

	backend := s.manager.DefaultBackend()
	d, ok := s.manager.GetDriver(backend)
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "UNAVAILABLE", "no driver for backend: "+backend)
		return
	}

	var rewriteFunc func(map[string]string) string
	var transformFunc func(io.ReadCloser) io.ReadCloser
	cleanPath := strings.TrimPrefix(r.URL.Path, "/api/v1")
	for _, rt := range d.ProxyRoutes() {
		if r.Method != rt.Method {
			continue
		}
		if matchRoute(cleanPath, rt.Prefix) {
			rewriteFunc = rt.Rewrite
			transformFunc = rt.Transform
			break
		}
	}

	if rewriteFunc == nil {
		writeErr(w, http.StatusNotFound, "NOT_FOUND", "no proxy route for "+r.URL.Path)
		return
	}

	pathValues := extractPathValues(r)
	target := rewriteFunc(pathValues)

	if transformFunc != nil && r.Body != nil {
		r.Body = transformFunc(r.Body)
		buf, err := io.ReadAll(r.Body)
		r.Body.Close()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "INTERNAL", "failed to read request body")
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(buf))
		r.ContentLength = int64(len(buf))
		r.Header.Set("Content-Length", strconv.Itoa(len(buf)))
	}

	targetAddr := targetURL.Scheme + "://" + targetURL.Host + target
	start := time.Now()

	proxy := httputil.NewSingleHostReverseProxy(targetURL)
	proxy.FlushInterval = -1

	headerMap := d.HeaderMap()

	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.URL.Path = target
		req.URL.RawPath = ""
		req.Host = targetURL.Host
		for from, to := range headerMap {
			if v := req.Header.Get(from); v != "" {
				req.Header.Set(to, v)
			}
		}
	}

	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		logger.Error("proxy %s %s -> %s %d %s err: %v", r.Method, r.URL.Path, targetAddr, http.StatusBadGateway, time.Since(start), err)
		http.Error(w, "bad gateway", http.StatusBadGateway)
	}

	proxy.ModifyResponse = func(resp *http.Response) error {
		stripCORSHeaders(resp.Header)
		if resp.StatusCode >= 400 {
			body, readErr := io.ReadAll(resp.Body)
			if readErr != nil {
				logger.Error("proxy %s %s -> %s %d %s, failed to read response body: %v", r.Method, r.URL.Path, targetAddr, resp.StatusCode, time.Since(start), readErr)
				return nil
			}
			resp.Body = io.NopCloser(bytes.NewReader(body))
			logger.Error("proxy %s %s -> %s %d %s, body: %s", r.Method, r.URL.Path, targetAddr, resp.StatusCode, time.Since(start), body)
		} else {
			logger.Info("proxy %s %s -> %s %d %s", r.Method, r.URL.Path, targetAddr, resp.StatusCode, time.Since(start))
		}
		return nil
	}

	proxy.ServeHTTP(w, r)
}

func extractPathValues(r *http.Request) map[string]string {
	vals := make(map[string]string)
	for _, key := range []string{"id"} {
		if v := r.PathValue(key); v != "" {
			vals[key] = v
		}
	}
	return vals
}

func matchRoute(path, pattern string) bool {
	patternParts := strings.Split(strings.Trim(pattern, "/"), "/")
	pathParts := strings.Split(strings.Trim(path, "/"), "/")

	if len(pathParts) < len(patternParts) {
		return false
	}

	for i, pp := range patternParts {
		if strings.HasPrefix(pp, "{") && strings.HasSuffix(pp, "}") {
			continue
		}
		if pp != pathParts[i] {
			return false
		}
	}

	if len(patternParts) > 0 && !strings.Contains(patternParts[len(patternParts)-1], "{") &&
		len(pathParts) > len(patternParts) {
		return false
	}

	return len(pathParts) == len(patternParts)
}
