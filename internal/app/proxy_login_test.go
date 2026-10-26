package app_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// withProxyLogin adds TPM_PROXY_LOGIN_HOST=tpm.example.com to a Local Login env.
func withProxyLogin(env map[string]string) map[string]string {
	env["TPM_PROXY_LOGIN_HOST"] = "tpm.example.com"
	return env
}

func forHost(req *http.Request, host string) *http.Request {
	req.Host = host
	return req
}

func TestProxyLoginHostSkipsLocalLogin(t *testing.T) {
	for style, env := range map[string]func(string) map[string]string{"basic": basicLogin, "form": formLogin} {
		a := newApp(t, withProxyLogin(env(t.TempDir())))
		for _, host := range []string{"tpm.example.com", "TPM.example.com:443"} {
			if rec := do(t, a, forHost(httptest.NewRequest("GET", "/", nil), host)); rec.Code != 200 {
				t.Errorf("%s: GET / for %s without credentials = %d, want 200", style, host, rec.Code)
			}
		}
	}
}

func TestProxyLoginHostSendsSignInAndOutToTheList(t *testing.T) {
	for style, env := range map[string]func(string) map[string]string{"basic": basicLogin, "form": formLogin} {
		a := newApp(t, withProxyLogin(env(t.TempDir())))
		for _, req := range []*http.Request{
			httptest.NewRequest("GET", "/login", nil),
			httptest.NewRequest("POST", "/login", nil),
			httptest.NewRequest("POST", "/logout", nil),
		} {
			rec := do(t, a, forHost(req, "tpm.example.com"))
			if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
				t.Errorf("%s: %s %s = %d to %q, want 303 to /", style, req.Method, req.URL.Path, rec.Code, rec.Header().Get("Location"))
			}
		}
	}
}

func TestProxyLoginHostHasNoSignOut(t *testing.T) {
	a := newApp(t, withProxyLogin(formLogin(t.TempDir())))
	rec := do(t, a, forHost(httptest.NewRequest("GET", "/", nil), "tpm.example.com"))
	assertContains(t, rec.Body.String(), "Proxy Hosts", "<header", "Traefik Proxy Manager")
	assertNotContains(t, rec.Body.String(), "Sign out", "/logout")
}

func TestEveryOtherHostIsAskedForLocalLogin(t *testing.T) {
	for style, tc := range map[string]struct {
		env       func(string) map[string]string
		challenge int
	}{"basic": {basicLogin, http.StatusUnauthorized}, "form": {formLogin, http.StatusSeeOther}} {
		a := newApp(t, withProxyLogin(tc.env(t.TempDir())))
		for _, host := range []string{"tpm-fallback.example.com", "192.0.2.10:8080", "tpm.example.com.evil.test", "example.com"} {
			if rec := do(t, a, forHost(httptest.NewRequest("GET", "/", nil), host)); rec.Code != tc.challenge {
				t.Errorf("%s: GET / for %s = %d, want %d", style, host, rec.Code, tc.challenge)
			}
		}
		// Only Host decides: a client can't claim the Proxy Login hostname in a header.
		req := forHost(httptest.NewRequest("GET", "/", nil), "tpm-fallback.example.com")
		req.Header.Set("X-Forwarded-Host", "tpm.example.com")
		if rec := do(t, a, req); rec.Code != tc.challenge {
			t.Errorf("%s: GET / with X-Forwarded-Host: tpm.example.com = %d, want %d", style, rec.Code, tc.challenge)
		}
	}
}
