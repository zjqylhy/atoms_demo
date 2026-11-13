package httpapi

import (
	"mockatoms/internal/catalog"
	"net/http"
)

func (s *Server) catalog(w http.ResponseWriter, r *http.Request) {
	items, err := catalog.List()
	if err != nil {
		s.fail(w, 500, err)
		return
	}
	send(w, 200, items)
}
func (s *Server) copyPreset(w http.ResponseWriter, r *http.Request) {
	var in struct {
		RequestID string `json:"request_id"`
	}
	if err := decode(r, &in); err != nil {
		s.fail(w, 400, err)
		return
	}
	p, err := s.Store.CopyPreset(sessionOf(r).TokenHash, r.PathValue("template"), in.RequestID)
	if err != nil {
		s.appFailure(w, err)
		return
	}
	send(w, 201, p.Public())
}
