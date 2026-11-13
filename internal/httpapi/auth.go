package httpapi

import (
	"context"
	"crypto/subtle"
	"errors"
	"mockatoms/internal/auth"
	"mockatoms/internal/store"
	"net"
	"net/http"
	"strings"
	"time"
)

func sessionOf(r *http.Request) store.Session {
	v, _ := r.Context().Value(ctxKey("session")).(store.Session)
	return v
}

func (s *Server) cookieName(r *http.Request, account bool) string {
	name := "atoms_session"
	if account {
		name = "atoms_account"
	}
	if strings.HasPrefix(s.Config.PublicURL, "https://") || r.TLS != nil {
		name = "__Host-" + name
	}
	return name
}
func (s *Server) setCookie(w http.ResponseWriter, r *http.Request, token string, account bool) {
	maxAge := 365 * 86400
	if account {
		maxAge = 30 * 86400
	}
	if token == "" {
		maxAge = -1
	}
	name := s.cookieName(r, account)
	http.SetCookie(w, &http.Cookie{Name: name, Value: token, Path: "/", HttpOnly: true, Secure: strings.HasPrefix(name, "__Host-"), SameSite: http.SameSiteLaxMode, MaxAge: maxAge})
}

func (s *Server) resolveSession(w http.ResponseWriter, r *http.Request) (string, store.Session, error) {
	bootstrap := r.URL.Path == "/api/bootstrap"
	if cookie, err := r.Cookie(s.cookieName(r, true)); err == nil {
		session, err := s.Store.Session(cookie.Value, false)
		if err == nil && session.UserID != "" {
			return cookie.Value, session, nil
		}
		if !bootstrap {
			return "", store.Session{}, store.ErrSession
		}
		s.setCookie(w, r, "", true)
	}
	if cookie, err := r.Cookie(s.cookieName(r, false)); err == nil {
		session, err := s.Store.Session(cookie.Value, true)
		if err == nil && session.UserID == "" {
			return cookie.Value, session, nil
		}
	}
	if !bootstrap {
		return "", store.Session{}, store.ErrSession
	}
	token, session, err := s.Store.NewGuest()
	if err == nil {
		s.setCookie(w, r, token, false)
	}
	return token, session, err
}

func (s *Server) sessionMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		publicCatalog := r.Method == "GET" && (r.URL.Path == "/api/catalog" || strings.HasPrefix(r.URL.Path, "/api/catalog/"))
		if !strings.HasPrefix(r.URL.Path, "/api/") || publicCatalog {
			next.ServeHTTP(w, r)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 3*1024*1024)
		token, session, err := s.resolveSession(w, r)
		if err != nil {
			s.fail(w, 401, store.ErrSession)
			return
		}
		if r.Method != "GET" && r.Method != "HEAD" {
			origin := r.Header.Get("Origin")
			expected := "http://" + r.Host
			if r.TLS != nil {
				expected = "https://" + r.Host
			}
			if origin != "" && origin != expected && origin != s.Config.PublicURL {
				s.fail(w, 403, errors.New("请求来源无效"))
				return
			}
			if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(csrf(token))) != 1 {
				s.fail(w, 403, errors.New("请求校验失败，请刷新页面"))
				return
			}
			principal := session.Workspace
			if session.UserID != "" {
				principal = "user:" + session.UserID
			}
			s.mu.Lock()
			now := time.Now()
			limit := s.rates[principal]
			if now.Sub(limit.At) > time.Minute {
				limit = window{At: now}
			}
			limit.Count++
			s.rates[principal] = limit
			for key, item := range s.rates {
				if now.Sub(item.At) > 2*time.Minute {
					delete(s.rates, key)
				}
			}
			s.mu.Unlock()
			if limit.Count > 60 {
				s.fail(w, 429, errors.New("操作过于频繁，请稍后重试"))
				return
			}
		}
		workspace := session.Workspace
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(parts) >= 3 && parts[0] == "api" && parts[1] == "projects" {
			workspace, err = s.Store.ProjectOwner(session, parts[2])
			if err != nil {
				s.fail(w, 404, err)
				return
			}
		}
		ctx := context.WithValue(r.Context(), ctxKey("session"), session)
		ctx = context.WithValue(ctx, ctxKey("owner"), workspace)
		ctx = context.WithValue(ctx, ctxKey("csrf"), csrf(token))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if s.Config.TrustProxy && net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback() {
		parts := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
		candidate := strings.TrimSpace(parts[len(parts)-1])
		if net.ParseIP(candidate) != nil {
			return candidate
		}
	}
	return host
}

func (s *Server) authenticate(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	var body struct {
		Username    string `json:"username"`
		Password    string `json:"password"`
		ImportGuest bool   `json:"import_guest"`
	}
	if err := decode(r, &body); err != nil {
		s.fail(w, 400, err)
		return
	}
	username, err := auth.Username(body.Username)
	if err != nil {
		s.fail(w, 400, err)
		return
	}
	if err = s.Store.AuthAttempt(username, s.clientIP(r)); err != nil {
		s.fail(w, 429, err)
		return
	}
	current := sessionOf(r)
	var token string
	register := r.URL.Path == "/api/auth/register"
	if register {
		if current.UserID != "" {
			s.fail(w, 409, errors.New("请先退出当前账号"))
			return
		}
		hash, hashErr := auth.Hash(r.Context(), body.Password)
		if hashErr != nil {
			s.fail(w, 400, hashErr)
			return
		}
		token, err = s.Store.Register(current, username, hash)
	} else {
		id, hash, lookupErr := s.Store.Credential(username)
		if lookupErr != nil {
			s.fail(w, 503, errors.New("账号服务暂不可用"))
			return
		}
		if !auth.Verify(r.Context(), hash, body.Password) || id == "" {
			s.fail(w, 401, errors.New("用户名或密码错误"))
			return
		}
		var guest *store.Session
		if body.ImportGuest {
			cookie, cookieErr := r.Cookie(s.cookieName(r, false))
			if cookieErr == nil {
				g, loadErr := s.Store.Session(cookie.Value, true)
				if loadErr == nil && g.UserID == "" {
					guest = &g
				}
			}
			if guest == nil {
				s.fail(w, 409, errors.New("游客会话已变化，请刷新后重试"))
				return
			}
		}
		token, err = s.Store.Login(id, guest)
	}
	if err != nil {
		s.fail(w, 409, err)
		return
	}
	if current.UserID != "" {
		_ = s.Store.RevokeSession(current.TokenHash)
	}
	s.setCookie(w, r, token, true)
	if register || body.ImportGuest {
		s.setCookie(w, r, "", false)
	}
	send(w, 200, map[string]any{"ok": true, "csrf": csrf(token)})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	session := sessionOf(r)
	if err := s.Store.RevokeSession(session.TokenHash); err != nil {
		s.fail(w, 503, errors.New("退出失败，请重试"))
		return
	}
	s.setCookie(w, r, "", session.UserID != "")
	send(w, 200, map[string]any{"ok": true})
}
