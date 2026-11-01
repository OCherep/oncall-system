package main

import (
	"context"
	"net/http"
	"strings"
)

// Ідентичність запиту береться ВИКЛЮЧНО з серверної сесії (cookie oncall_session
// або Authorization: Bearer <session_token>). Поля actor/role/author_name/created_by
// з тіла запиту ігноруються для авторизації та аудиту (CUT-04 AC1–AC3).

type authCtxKey struct{}

func withAuthContext(r *http.Request, s *sessionInfo) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), authCtxKey{}, s))
}

// sessionFrom повертає сесію, яку поклав requireAuth/withOptionalSession.
func sessionFrom(r *http.Request) (*sessionInfo, bool) {
	s, ok := r.Context().Value(authCtxKey{}).(*sessionInfo)
	return s, ok && s != nil
}

// sessionActor — ім'я користувача для аудиту (name, або username якщо name порожній).
func sessionActor(r *http.Request) string {
	s, ok := sessionFrom(r)
	if !ok {
		return ""
	}
	if n := strings.TrimSpace(s.Name); n != "" {
		return n
	}
	return s.Username
}

// sessionRole — роль із сесії ("admin", "user", ...); "" якщо сесії немає.
func sessionRole(r *http.Request) string {
	s, ok := sessionFrom(r)
	if !ok {
		return ""
	}
	return s.Role
}

func sessionIsAdmin(r *http.Request) bool { return sessionRole(r) == "admin" }

// withOptionalSession кладе сесію в контекст, якщо вона валідна, але не блокує запит.
func withOptionalSession(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s, ok := lookupSession(sessionTokenFromRequest(r)); ok {
			r = withAuthContext(r, s)
		}
		next(w, r)
	}
}

// requireAuthExceptMethods: сесія обов'язкова для всіх методів, крім перелічених
// (для них — optional session). Використовується для гостьового POST /api/incidents.
func requireAuthExceptMethods(next http.HandlerFunc, open ...string) http.HandlerFunc {
	authed := requireAuth(false, next)
	opt := withOptionalSession(next)
	return func(w http.ResponseWriter, r *http.Request) {
		for _, m := range open {
			if r.Method == m {
				opt(w, r)
				return
			}
		}
		authed(w, r)
	}
}
