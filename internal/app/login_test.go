package app_test

import (
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"gitlab.example.com/home/devops/traefik-proxy-manager/internal/app"
)

func TestLocalLoginSettingsFailStartup(t *testing.T) {
	for name, tc := range map[string]struct {
		env    map[string]string
		want   []string
		unwant []string
	}{
		"style isn't exact": {
			map[string]string{"TPM_LOCAL_AUTH": "Basic", "TPM_AUTH_USERNAME": "admin", "TPM_AUTH_PASSWORD": "pw"},
			[]string{`TPM_LOCAL_AUTH "Basic" isn't basic or form`}, nil,
		},
		"form isn't available yet": {
			map[string]string{"TPM_LOCAL_AUTH": "form", "TPM_AUTH_USERNAME": "admin", "TPM_AUTH_PASSWORD": "pw"},
			[]string{`TPM_LOCAL_AUTH "form"`}, nil,
		},
		"no credentials": {
			map[string]string{"TPM_LOCAL_AUTH": "basic"},
			[]string{
				"TPM_AUTH_USERNAME is required when TPM_LOCAL_AUTH is set",
				"TPM_AUTH_PASSWORD is required when TPM_LOCAL_AUTH is set",
			}, nil,
		},
		"empty password": {
			map[string]string{"TPM_LOCAL_AUTH": "basic", "TPM_AUTH_USERNAME": "admin", "TPM_AUTH_PASSWORD": ""},
			[]string{"TPM_AUTH_PASSWORD is required when TPM_LOCAL_AUTH is set"},
			[]string{"TPM_AUTH_USERNAME is required"},
		},
		"credentials without a style": {
			map[string]string{"TPM_AUTH_USERNAME": "admin", "TPM_AUTH_PASSWORD": "pw"},
			[]string{"TPM_AUTH_USERNAME and TPM_AUTH_PASSWORD only apply with Local Login; set TPM_LOCAL_AUTH to basic or form, or remove them"}, nil,
		},
		"password without a style": {
			map[string]string{"TPM_AUTH_PASSWORD": "pw"},
			[]string{"TPM_AUTH_PASSWORD only applies with Local Login; set TPM_LOCAL_AUTH to basic or form, or remove it"},
			[]string{"TPM_AUTH_USERNAME"},
		},
		"username with a colon": {
			map[string]string{"TPM_LOCAL_AUTH": "basic", "TPM_AUTH_USERNAME": "ad:min", "TPM_AUTH_PASSWORD": "pw"},
			[]string{`TPM_AUTH_USERNAME can't contain ":" (Basic style splits on it)`}, nil,
		},
		"several at once": {
			map[string]string{"TPM_LOCAL_AUTH": "Basic", "TPM_AUTH_USERNAME": "ad:min", "TPM_CERT_RESOLVERS": ""},
			[]string{
				`TPM_LOCAL_AUTH "Basic" isn't basic or form`,
				`TPM_AUTH_USERNAME can't contain ":" (Basic style splits on it)`,
				"TPM_AUTH_PASSWORD is required when TPM_LOCAL_AUTH is set",
				"TPM_CERT_RESOLVERS is required",
			}, nil,
		},
	} {
		t.Run(name, func(t *testing.T) {
			env := internalOnly(t.TempDir())
			for k, v := range tc.env {
				env[k] = v
			}
			_, err := app.New(func(k string) string { return env[k] }, time.Now)
			if err == nil {
				t.Fatalf("New succeeded, want errors %q", tc.want)
			}
			assertContains(t, err.Error(), tc.want...)
			assertNotContains(t, err.Error(), tc.unwant...)
		})
	}
}

func TestLocalLoginTakesCredentialsAsTheyAre(t *testing.T) {
	env := internalOnly(t.TempDir())
	env["TPM_LOCAL_AUTH"] = "basic"
	env["TPM_AUTH_USERNAME"] = " admin "
	env["TPM_AUTH_PASSWORD"] = ":" // a colon is fine in the password, and there's no strength rule
	a := newApp(t, env)
	if rec := do(t, a, withBasic(httptest.NewRequest("GET", "/", nil), " admin ", ":")); rec.Code != 200 {
		t.Fatalf("GET / with the untrimmed credentials = %d, want 200", rec.Code)
	}
	if rec := do(t, a, withBasic(httptest.NewRequest("GET", "/", nil), "admin", ":")); rec.Code != 401 {
		t.Fatalf("GET / with a trimmed username = %d, want 401", rec.Code)
	}
}

// basicLogin is an install with Basic-style Local Login as admin / s3cret.
func basicLogin(dataDir string) map[string]string {
	env := internalOnly(dataDir)
	env["TPM_LOCAL_AUTH"] = "basic"
	env["TPM_AUTH_USERNAME"] = "admin"
	env["TPM_AUTH_PASSWORD"] = "s3cret"
	return env
}

func withBasic(req *http.Request, user, pass string) *http.Request {
	req.SetBasicAuth(user, pass)
	return req
}

func TestBasicLoginChallengesEveryPathWithoutTheRightCredentials(t *testing.T) {
	a := newApp(t, basicLogin(t.TempDir()))
	requests := map[string]func() *http.Request{
		"list": func() *http.Request { return httptest.NewRequest("GET", "/", nil) },
		"htmx status poll": func() *http.Request {
			req := httptest.NewRequest("GET", "/proxy-hosts/status", nil)
			req.Header.Set("HX-Request", "true")
			return req
		},
		"POST": func() *http.Request {
			req := httptest.NewRequest("POST", "/proxy-hosts", strings.NewReader(hostForm("app.example.com").Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			return req
		},
		"unknown path": func() *http.Request { return httptest.NewRequest("GET", "/no-such-page", nil) },
	}
	for name, req := range requests {
		for creds, sign := range map[string]func(*http.Request) *http.Request{
			"none":           func(r *http.Request) *http.Request { return r },
			"wrong password": func(r *http.Request) *http.Request { return withBasic(r, "admin", "guess") },
			"wrong username": func(r *http.Request) *http.Request { return withBasic(r, "root", "s3cret") },
		} {
			t.Run(name+"/"+creds, func(t *testing.T) {
				rec := do(t, a, sign(req()))
				if rec.Code != http.StatusUnauthorized {
					t.Fatalf("status = %d, want 401", rec.Code)
				}
				if got, want := rec.Header().Get("WWW-Authenticate"), `Basic realm="tpm", charset="UTF-8"`; got != want {
					t.Fatalf("WWW-Authenticate = %q, want %q", got, want)
				}
			})
		}
	}
	assertSnapshot(t, a, placeholderSnapshot("web-int"))
}

func TestBasicLoginLetsTheRightCredentialsIn(t *testing.T) {
	a := newApp(t, basicLogin(t.TempDir()))
	if rec := do(t, a, withBasic(httptest.NewRequest("GET", "/", nil), "admin", "s3cret")); rec.Code != 200 {
		t.Fatalf("GET / = %d, want 200", rec.Code)
	}
	req := withBasic(httptest.NewRequest("POST", "/proxy-hosts", strings.NewReader(hostForm("app.example.com").Encode())), "admin", "s3cret")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	do(t, a, req)
	assertContains(t, do(t, a, withBasic(httptest.NewRequest("GET", "/", nil), "admin", "s3cret")).Body.String(), "app.example.com")
}

func TestBasicLoginLeavesStaticFilesAndTraefikConfigOpen(t *testing.T) {
	a := newApp(t, basicLogin(t.TempDir()))
	assertContains(t, get(t, a, "/static/app.css"), "tailwindcss")
	traefikConfig(t, a)

	req := httptest.NewRequest("GET", "/traefik/config", nil)
	req.Header.Set("X-Forwarded-For", "203.0.113.7")
	if rec := do(t, a, req); rec.Code != http.StatusNotFound {
		t.Fatalf("GET /traefik/config through a router = %d, want 404", rec.Code)
	}
}

func TestSkipListCantBeReachedAround(t *testing.T) {
	a := newApp(t, basicLogin(t.TempDir()))
	for _, path := range []string{"/static/../proxy-hosts/new", "/traefik/config/../../"} {
		if rec := do(t, a, httptest.NewRequest("GET", path, nil)); rec.Code != http.StatusUnauthorized {
			t.Errorf("GET %s = %d, want 401", path, rec.Code)
		}
	}
}

// logs collects what tpm logs through the standard logger during the test.
func logs(t *testing.T) *strings.Builder {
	var b strings.Builder
	log.SetOutput(&b)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return &b
}

func TestStartupLogNamesTheLoginSetup(t *testing.T) {
	out := logs(t)
	newApp(t, internalOnly(t.TempDir()))
	assertContains(t, out.String(), "login: none by tpm (Proxy Login, if any, is up to Traefik)\n")

	out.Reset()
	newApp(t, basicLogin(t.TempDir()))
	assertContains(t, out.String(), `login: Local Login, basic style, user "admin"`+"\n")
	assertNotContains(t, out.String(), "s3cret")
}

func TestWrongBasicAttemptIsLogged(t *testing.T) {
	a := newApp(t, basicLogin(t.TempDir()))
	out := logs(t)

	do(t, a, httptest.NewRequest("GET", "/", nil))
	if out.Len() != 0 {
		t.Fatalf("a request without credentials logged %q, want nothing", out)
	}

	req := withBasic(httptest.NewRequest("GET", "/", nil), "bob", "guess")
	req.Header.Set("X-Forwarded-For", "203.0.113.7")
	do(t, a, req)
	assertContains(t, out.String(), `login failed: user "bob" from 203.0.113.7 (Basic)`+"\n")
	assertNotContains(t, out.String(), "guess")

	out.Reset()
	req = withBasic(httptest.NewRequest("GET", "/", nil), "bob", "guess")
	req.RemoteAddr = "198.51.100.4:51234"
	do(t, a, req)
	assertContains(t, out.String(), `login failed: user "bob" from 198.51.100.4:51234 (Basic)`)

	out.Reset()
	do(t, a, withBasic(httptest.NewRequest("GET", "/", nil), "admin", "s3cret"))
	if out.Len() != 0 {
		t.Fatalf("a right sign-in logged %q, want nothing", out)
	}
}

func TestNoLocalLoginLeavesEveryPathOpen(t *testing.T) {
	a := newApp(t, internalOnly(t.TempDir()))
	get(t, a, "/")
	if rec := do(t, a, httptest.NewRequest("GET", "/no-such-page", nil)); rec.Code != 404 {
		t.Fatalf("GET /no-such-page = %d, want 404", rec.Code)
	}
}
