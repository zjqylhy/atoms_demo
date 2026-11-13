package httpapi

import (
	"errors"
	"mockatoms/internal/store"
	"net/http"
)

func (s *Server) projectOrder(w http.ResponseWriter, r *http.Request) {
	var result store.ProjectOrder
	var err error
	if readMethod(r) {
		result, err = s.Store.ProjectOrder(sessionOf(r).TokenHash)
	} else {
		var body struct {
			Revision int64    `json:"revision"`
			IDs      []string `json:"ids"`
		}
		if err = decode(r, &body); err != nil {
			s.fail(w, 400, err)
			return
		}
		result, err = s.Store.SaveProjectOrder(sessionOf(r).TokenHash, body.Revision, body.IDs)
	}
	if err != nil {
		switch {
		case errors.Is(err, store.ErrOrderConflict):
			s.failCode(w, 409, "order_conflict", err.Error())
		case errors.Is(err, store.ErrInvalidOrder):
			s.fail(w, 400, err)
		default:
			s.fail(w, 503, err)
		}
		return
	}
	send(w, 200, result)
}
