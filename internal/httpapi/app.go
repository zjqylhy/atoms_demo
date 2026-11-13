package httpapi

import (
	"errors"
	"mockatoms/internal/core"
	"mockatoms/internal/store"
	"net/http"
)

func (s *Server) appFailure(w http.ResponseWriter, err error) {
	status := 400
	switch {
	case errors.Is(err, store.ErrCopyDeleted):
		s.failCode(w, 410, "copy_deleted", err.Error())
		return
	case errors.Is(err, store.ErrSession):
		status = 401
	case errors.Is(err, core.ErrNotFound):
		status = 404
	case errors.Is(err, store.ErrGrant):
		status = 403
	case errors.Is(err, store.ErrConflict), errors.Is(err, store.ErrSchema):
		status = 409
	}
	s.fail(w, status, err)
}

func (s *Server) previewAccess(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Version string `json:"version"`
	}
	if err := decode(r, &in); err != nil {
		s.appFailure(w, err)
		return
	}
	out, err := s.Store.PreviewAccess(sessionOf(r).TokenHash, r.PathValue("id"), in.Version)
	if err != nil {
		s.appFailure(w, err)
		return
	}
	send(w, 200, out)
}

func (s *Server) appState(w http.ResponseWriter, r *http.Request) {
	id, hash := r.PathValue("id"), sessionOf(r).TokenHash
	if readMethod(r) {
		state, err := s.Store.LoadAppState(hash, id, r.Header.Get("X-Preview-Grant"))
		if err != nil {
			s.appFailure(w, err)
			return
		}
		send(w, 200, state)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, core.MaxAppStateBytes+2048)
	var in store.SaveAppRequest
	if err := decode(r, &in); err != nil {
		s.appFailure(w, err)
		return
	}
	revision, err := s.Store.SaveAppState(hash, id, in)
	if err != nil {
		s.appFailure(w, err)
		return
	}
	send(w, 200, map[string]any{"revision": revision, "saved": true})
}

func (s *Server) appBackups(w http.ResponseWriter, r *http.Request) {
	backups, err := s.Store.AppBackups(sessionOf(r).TokenHash, r.PathValue("id"))
	if err != nil {
		s.appFailure(w, err)
		return
	}
	send(w, 200, backups)
}

func (s *Server) resetAppState(w http.ResponseWriter, r *http.Request) {
	var in store.ResetAppRequest
	if err := decode(r, &in); err != nil {
		s.appFailure(w, err)
		return
	}
	revision, err := s.Store.ResetAppState(sessionOf(r).TokenHash, r.PathValue("id"), in)
	if err != nil {
		s.appFailure(w, err)
		return
	}
	send(w, 200, map[string]any{"revision": revision, "saved": true})
}
