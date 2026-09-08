package httpapi

import (
	_ "embed"
	"net/http"
)

//go:embed static/index.html
var indexHTML []byte

func (s *Server) ServeUI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(indexHTML)
}
