package app

import (
	"cmp"
	"crypto/sha256"
	"crypto/subtle"
	"fmt"
	"log"
	"net/http"
)

// login is tpm's Local Login check, in front of the whole mux. It denies by
// default: every path needs login except the skip list, so unknown and
// future paths are challenged rather than 404'd.
type login struct {
	style, username string
	user, pass      [sha256.Size]byte // digests, so comparing them leaks no lengths
}

func newLogin(s loginSettings) *login {
	return &login{style: s.style, username: s.username, user: sha256.Sum256([]byte(s.username)), pass: sha256.Sum256([]byte(s.password))}
}

// String is the startup line naming the login setup; never the password.
func (l *login) String() string {
	if l.style == "" {
		return "login: none by tpm (Proxy Login, if any, is up to Traefik)"
	}
	return fmt.Sprintf("login: Local Login, %s style, user %q", l.style, l.username)
}

// wrap puts the check in front of next.
func (l *login) wrap(next http.Handler) http.Handler {
	if l.style == "" {
		return next
	}
	// The skip list: paths that never need Local Login.
	skip := http.NewServeMux()
	skip.Handle("GET /static/", next)
	skip.Handle("/traefik/config", next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h, pattern := skip.Handler(r); pattern != "" {
			h.ServeHTTP(w, r)
			return
		}
		if l.basic(r) {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("WWW-Authenticate", `Basic realm="tpm", charset="UTF-8"`)
		http.Error(w, "Sign in to tpm.", http.StatusUnauthorized)
	})
}

// basic reports whether r carries the right Basic credentials, logging a
// wrong attempt. A request with none isn't a failure: browsers send one
// first to get the prompt.
func (l *login) basic(r *http.Request) bool {
	user, pass, ok := r.BasicAuth()
	if !ok {
		return false
	}
	if l.matches(user, pass) {
		return true
	}
	// X-Forwarded-For is only logged, never used for a decision.
	log.Printf("login failed: user %q from %s (Basic)", user, cmp.Or(r.Header.Get("X-Forwarded-For"), r.RemoteAddr))
	return false
}

// matches compares both credentials in constant time, whichever is wrong.
func (l *login) matches(user, pass string) bool {
	u, p := sha256.Sum256([]byte(user)), sha256.Sum256([]byte(pass))
	return subtle.ConstantTimeCompare(u[:], l.user[:])&subtle.ConstantTimeCompare(p[:], l.pass[:]) == 1
}
