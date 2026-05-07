package localserver

import "net/http"

func setCORSHeaders(headers http.Header, origin string) {
	if origin == "" {
		origin = "*"
	}
	headers.Set("Access-Control-Allow-Origin", origin)
	headers.Set("Access-Control-Allow-Methods", "GET,POST,PUT,PATCH,DELETE,OPTIONS")
	headers.Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Workspace-Directory, x-opencode-directory, x-csc-directory")
	headers.Set("Access-Control-Max-Age", "86400")
}

func stripCORSHeaders(headers http.Header) {
	headers.Del("Access-Control-Allow-Origin")
	headers.Del("Access-Control-Allow-Methods")
	headers.Del("Access-Control-Allow-Headers")
	headers.Del("Access-Control-Max-Age")
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setCORSHeaders(w.Header(), r.Header.Get("Origin"))

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}
