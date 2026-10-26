package app

import (
	"cmp"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// A Form session lasts a fixed 7 days from sign-in, however much it's used.
const (
	sessionCookie   = "tpm_session"
	sessionLifetime = 7 * 24 * time.Hour
)

// login is tpm's Local Login check, in front of the whole mux. It denies by
// default: every path needs login except the skip list, so unknown and
// future paths are challenged rather than 404'd.
type login struct {
	loginSettings
	user, pass [sha256.Size]byte // digests, so comparing them leaks no lengths
	now        func() time.Time
	mu         sync.Mutex
	key        []byte // signs Form sessions; kept only in memory, so a restart signs everyone out
}

func newLogin(s loginSettings, now func() time.Time) *login {
	return &login{loginSettings: s, user: sha256.Sum256([]byte(s.username)), pass: sha256.Sum256([]byte(s.password)), now: now, key: newKey()}
}

// newKey is a fresh session key, 32 bytes from crypto/rand.
func newKey() []byte {
	key := make([]byte, 32)
	rand.Read(key)
	return key
}

// String is the startup line naming the login setup; never the password.
func (l *login) String() string {
	if l.style == "" {
		return "login: none by tpm (Proxy Login, if any, is up to Traefik)"
	}
	line := fmt.Sprintf("login: Local Login, %s style, user %q", l.style, l.username)
	if l.proxyHost != "" {
		line += "; skipped on Proxy Login host " + l.proxyHost
	}
	return line
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
	if l.style == "form" {
		skip.Handle("GET /login", next)
		skip.Handle("POST /login", next)
		skip.Handle("POST /logout", next)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if l.onProxyLoginHost(r) {
			if r.URL.Path == "/login" || r.URL.Path == "/logout" {
				// Nothing to sign in or out of here; a stray bookmark lands on the list.
				http.Redirect(w, r, "/", http.StatusSeeOther)
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		if h, pattern := skip.Handler(r); pattern != "" {
			h.ServeHTTP(w, r)
			return
		}
		switch {
		case l.style == "basic" && l.basic(r), l.style == "form" && l.session(r):
			next.ServeHTTP(w, r)
		case l.style == "basic":
			w.Header().Set("WWW-Authenticate", `Basic realm="tpm", charset="UTF-8"`)
			http.Error(w, "Sign in to tpm.", http.StatusUnauthorized)
		case r.Header.Get("HX-Request") != "":
			// htmx follows HX-Redirect before its responseHandling, so even
			// the status poll sends the page to sign-in.
			w.Header().Set("HX-Redirect", "/login")
			http.Error(w, "Sign in to tpm.", http.StatusUnauthorized)
		default:
			http.Redirect(w, r, "/login", http.StatusSeeOther)
		}
	})
}

// onProxyLoginHost reports whether r is for the Proxy Login hostname, which
// Traefik's forwardAuth has already let through. Only Host decides, ignoring
// case and port, never X-Forwarded-Host or an identity header (ADR 0001).
func (s loginSettings) onProxyLoginHost(r *http.Request) bool {
	h, _, err := net.SplitHostPort(r.Host)
	if err != nil {
		h = r.Host
	}
	return s.proxyHost != "" && strings.EqualFold(h, s.proxyHost)
}

// signOut reports whether the page for r offers Sign out: only a Form
// session has one to end, and the Proxy Login hostname has no session.
func (s loginSettings) signOut(r *http.Request) bool {
	return s.style == "form" && !s.onProxyLoginHost(r)
}

// routes adds Form style's sign-in page and Sign out to mux; Basic style
// has neither, so there they're a 404.
func (l *login) routes(mux *http.ServeMux) {
	if l.style != "form" {
		return
	}
	mux.HandleFunc("GET /login", func(w http.ResponseWriter, r *http.Request) {
		render(w, http.StatusOK, "login.html", signInView{})
	})
	mux.HandleFunc("POST /login", func(w http.ResponseWriter, r *http.Request) {
		user := r.FormValue("username")
		if !l.matches(user, r.FormValue("password")) {
			l.failed(r, user)
			render(w, http.StatusUnauthorized, "login.html", signInView{Username: user, Wrong: true})
			return
		}
		exp := strconv.FormatInt(l.now().Add(sessionLifetime).Unix(), 10)
		l.setCookie(w, r, exp+"."+l.sign(exp), int(sessionLifetime.Seconds()))
		// Always the list, never the page first asked for: no open redirect.
		http.Redirect(w, r, "/", http.StatusSeeOther)
	})
	mux.HandleFunc("POST /logout", func(w http.ResponseWriter, r *http.Request) {
		// Sessions are only signed, not stored, so ending one means a new
		// key: every session ends, not just this browser's. Only a signed-in
		// request may do it, so nobody else can sign the user out.
		if l.session(r) {
			l.mu.Lock()
			l.key = newKey()
			l.mu.Unlock()
		}
		l.setCookie(w, r, "", -1)
		http.Redirect(w, r, "/login", http.StatusSeeOther)
	})
}

// setCookie sets (or, with maxAge -1, clears) the session cookie. It's
// Secure when Traefik got the request over https; that header only decides
// the flag, never access.
func (l *login) setCookie(w http.ResponseWriter, r *http.Request, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: value, Path: "/", MaxAge: maxAge,
		HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: r.Header.Get("X-Forwarded-Proto") == "https",
	})
}

// sign is the HMAC-SHA256 of a session's expiry.
func (l *login) sign(exp string) string {
	l.mu.Lock()
	m := hmac.New(sha256.New, l.key)
	l.mu.Unlock()
	m.Write([]byte(exp))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// session reports whether r carries a signed, unexpired session cookie. A
// missing, forged and expired one all look the same.
func (l *login) session(r *http.Request) bool {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return false
	}
	exp, sig, _ := strings.Cut(c.Value, ".")
	unix, err := strconv.ParseInt(exp, 10, 64)
	return hmac.Equal([]byte(sig), []byte(l.sign(exp))) && err == nil && l.now().Unix() < unix
}

// signInView is the sign-in page; after a wrong attempt, with the username as typed.
type signInView struct {
	Username string
	Wrong    bool
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
	l.failed(r, user)
	return false
}

// failed logs a wrong sign-in attempt as user. X-Forwarded-For is only
// logged, never used for a decision.
func (l *login) failed(r *http.Request, user string) {
	log.Printf("login failed: user %q from %s (%s)", user, cmp.Or(r.Header.Get("X-Forwarded-For"), r.RemoteAddr), strings.ToUpper(l.style[:1])+l.style[1:])
}

// matches compares both credentials in constant time, whichever is wrong.
func (l *login) matches(user, pass string) bool {
	u, p := sha256.Sum256([]byte(user)), sha256.Sum256([]byte(pass))
	return subtle.ConstantTimeCompare(u[:], l.user[:])&subtle.ConstantTimeCompare(p[:], l.pass[:]) == 1
}
