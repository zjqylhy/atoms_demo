package httpapi

import (
	"io"
	"mockatoms/internal/appruntime"
	"net/http"
)

func (s *Server) shares(w http.ResponseWriter, r *http.Request) {
	id, hash := r.PathValue("id"), sessionOf(r).TokenHash
	switch r.Method {
	case "GET", "HEAD":
		out, err := s.Store.Shares(hash, id)
		if err != nil {
			s.appFailure(w, err)
			return
		}
		send(w, 200, out)
	case "DELETE":
		if err := s.Store.RevokeShare(hash, id, r.PathValue("share")); err != nil {
			s.appFailure(w, err)
			return
		}
		send(w, 200, map[string]bool{"revoked": true})
	default:
		var in struct {
			Version string `json:"version"`
		}
		if err := decode(r, &in); err != nil {
			s.appFailure(w, err)
			return
		}
		sid, token, err := s.Store.CreateShare(hash, id, in.Version)
		if err != nil {
			s.appFailure(w, err)
			return
		}
		send(w, 201, map[string]string{"id": sid, "path": "/share/" + token})
	}
}

func (s *Server) publicShare(w http.ResponseWriter, r *http.Request) {
	if view := r.PathValue("view"); view != "" && view != "preview" {
		http.NotFound(w, r)
		return
	}
	out, err := s.Store.Shared(r.PathValue("token"))
	if err != nil {
		s.appFailure(w, err)
		return
	}
	if r.PathValue("view") == "preview" {
		markup := out.HTML
		if out.Contract != nil {
			markup = appruntime.Inject(markup)
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data: blob:; font-src data:; connect-src 'none'; form-action 'none'; base-uri 'none'; frame-ancestors 'self'; sandbox allow-scripts allow-forms")
		_, _ = io.WriteString(w, markup)
		return
	}
	send(w, 200, out)
}

func (s *Server) copyShare(w http.ResponseWriter, r *http.Request) {
	var in struct {
		RequestID string `json:"request_id"`
	}
	if err := decode(r, &in); err != nil {
		s.appFailure(w, err)
		return
	}
	p, err := s.Store.CopyShare(sessionOf(r).TokenHash, r.PathValue("token"), in.RequestID)
	if err != nil {
		s.appFailure(w, err)
		return
	}
	send(w, 201, p.Public())
}
