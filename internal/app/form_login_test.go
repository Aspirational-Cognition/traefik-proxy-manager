package app_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"gitlab.example.com/home/devops/traefik-proxy-manager/internal/app"
)

// formLogin is an install with Form-style Local Login as admin / s3cret.
func formLogin(dataDir string) map[string]string {
	env := basicLogin(dataDir)
	env["TPM_LOCAL_AUTH"] = "form"
	return env
}

// assertSentToSignIn checks rec sends a browser to the sign-in page: a 303
// for a normal request, a 401 with HX-Redirect for an htmx one.
func assertSentToSignIn(t *testing.T, rec *httptest.ResponseRecorder, htmx bool) {
	t.Helper()
	switch {
	case htmx && (rec.Code != http.StatusUnauthorized || rec.Header().Get("HX-Redirect") != "/login"):
		t.Fatalf("htmx request = %d, HX-Redirect %q; want 401 to /login", rec.Code, rec.Header().Get("HX-Redirect"))
	case !htmx && (rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login"):
		t.Fatalf("request = %d to %q, want 303 to /login", rec.Code, rec.Header().Get("Location"))
	}
}

func htmxGet(path string) *http.Request {
	req := httptest.NewRequest("GET", path, nil)
	req.Header.Set("HX-Request", "true")
	return req
}

func TestFormLoginSendsEveryPathWithoutASessionToSignIn(t *testing.T) {
	a := newApp(t, formLogin(t.TempDir()))
	for _, path := range []string{"/", "/proxy-hosts/new", "/no-such-page"} {
		assertSentToSignIn(t, do(t, a, httptest.NewRequest("GET", path, nil)), false)
	}
	// Even the 5-second status poll, so an expired session sends the page to sign-in.
	assertSentToSignIn(t, do(t, a, htmxGet("/proxy-hosts/status")), true)
	// Basic credentials are no way in.
	assertSentToSignIn(t, do(t, a, withBasic(httptest.NewRequest("GET", "/", nil), "admin", "s3cret")), false)
}

func TestFormLoginLeavesStaticFilesAndTraefikConfigOpen(t *testing.T) {
	a := newApp(t, formLogin(t.TempDir()))
	assertContains(t, get(t, a, "/static/app.css"), "tailwindcss")
	traefikConfig(t, a)
}

func TestSignInPage(t *testing.T) {
	a := newApp(t, formLogin(t.TempDir()))

	page := get(t, a, "/login")

	assertContains(t, page,
		"<header", ">tpm<", "Traefik Proxy Manager",
		">Sign in</h1>",
		"This address uses tpm's own login: the username and password set in <code", "TPM_AUTH_USERNAME</code> and <code", "TPM_AUTH_PASSWORD</code>.",
		`<form method="post" action="/login"`,
		">Username</label>", `name="username" value="" autocomplete="username" autofocus`,
		">Password</label>", `name="password" type="password" autocomplete="current-password" class`,
		`>Sign in</button>`,
		`href="/static/app.css"`,
	)
	assertNotContains(t, page, "Sign out", "://cdn", "unpkg", "Wrong username or password.")
}

func signIn(t *testing.T, h http.Handler, user, pass string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/login", strings.NewReader(url.Values{"username": {user}, "password": {pass}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Forwarded-For", "203.0.113.7")
	return do(t, h, req)
}

func TestWrongSignInShowsTheErrorAndKeepsTheUsername(t *testing.T) {
	a := newApp(t, formLogin(t.TempDir()))
	out := logs(t)

	for _, creds := range [][2]string{{"bob", "s3cret"}, {"admin", "guess"}} {
		out.Reset()
		rec := signIn(t, a, creds[0], creds[1])

		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("POST /login with %q = %d, want 401", creds, rec.Code)
		}
		if c := rec.Result().Cookies(); len(c) != 0 {
			t.Errorf("wrong sign-in set cookies %v", c)
		}
		page := rec.Body.String()
		assertContains(t, page,
			">Sign in</h1>", "Wrong username or password.",
			`name="username" value="`+creds[0]+`" autocomplete="username" class`,
			`name="password" type="password" autocomplete="current-password" autofocus`,
		)
		if n := strings.Count(page, "border-danger"); n != 2 {
			t.Errorf("%d fields outlined red, want both", n)
		}
		assertNotContains(t, page, creds[1], "Sign out")
		assertContains(t, out.String(), `login failed: user "`+creds[0]+`" from 203.0.113.7 (Form)`+"\n")
		assertNotContains(t, out.String(), creds[1])
	}
}

// session signs in rightly and returns the session cookie it set.
func session(t *testing.T, h http.Handler) *http.Cookie {
	t.Helper()
	rec := signIn(t, h, "admin", "s3cret")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
		t.Fatalf("POST /login = %d to %q, want 303 to /", rec.Code, rec.Header().Get("Location"))
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("sign-in set cookies %v, want one", cookies)
	}
	return cookies[0]
}

func withCookie(req *http.Request, c *http.Cookie) *http.Request {
	req.AddCookie(&http.Cookie{Name: c.Name, Value: c.Value})
	return req
}

func TestRightSignInStartsASessionAndLandsOnTheList(t *testing.T) {
	a := newApp(t, formLogin(t.TempDir()))
	out := logs(t)

	c := session(t, a)

	if !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/" || c.Secure {
		t.Errorf("cookie = %+v, want HttpOnly, SameSite=Strict, Path=/, not Secure over plain HTTP", c)
	}
	if out.Len() != 0 {
		t.Errorf("a right sign-in logged %q, want nothing", out)
	}
	rec := do(t, a, withCookie(httptest.NewRequest("GET", "/", nil), c))
	if rec.Code != 200 {
		t.Fatalf("GET / with the session = %d, want 200", rec.Code)
	}
	assertContains(t, rec.Body.String(), "Proxy Hosts", "<header", `<form method="post" action="/logout"`, ">Sign out</button>")
	if rec := do(t, a, withCookie(htmxGet("/proxy-hosts/status"), c)); rec.Code != 200 {
		t.Errorf("htmx status poll with the session = %d, want 200", rec.Code)
	}
}

func TestSessionCookieIsSecureBehindHTTPS(t *testing.T) {
	a := newApp(t, formLogin(t.TempDir()))
	req := httptest.NewRequest("POST", "/login", strings.NewReader("username=admin&password=s3cret"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Forwarded-Proto", "https")
	if c := do(t, a, req).Result().Cookies(); len(c) != 1 || !c[0].Secure {
		t.Fatalf("cookies %v, want one Secure", c)
	}
}

func TestSignInPageWithASessionStillHasNoSignOut(t *testing.T) {
	a := newApp(t, formLogin(t.TempDir()))
	rec := do(t, a, withCookie(httptest.NewRequest("GET", "/login", nil), session(t, a)))
	assertNotContains(t, rec.Body.String(), "Sign out")
}

func TestSignOutEndsTheSession(t *testing.T) {
	a := newApp(t, formLogin(t.TempDir()))
	c := session(t, a)

	rec := do(t, a, withCookie(httptest.NewRequest("POST", "/logout", nil), c))

	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
		t.Fatalf("POST /logout = %d to %q, want 303 to /login", rec.Code, rec.Header().Get("Location"))
	}
	if got := rec.Result().Cookies(); len(got) != 1 || got[0].Name != c.Name || got[0].MaxAge >= 0 {
		t.Errorf("POST /logout set cookies %v, want the session cleared", got)
	}
	assertSentToSignIn(t, do(t, a, withCookie(httptest.NewRequest("GET", "/", nil), c)), false)
	assertNotContains(t, get(t, a, "/login"), "signed out", "Signed out")
}

func TestSessionEndsSevenDaysAfterSignIn(t *testing.T) {
	now := t0
	a, err := app.New(func(k string) string { return formLogin(t.TempDir())[k] }, func() time.Time { return now })
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	c := session(t, a)

	now = t0.Add(7*24*time.Hour - time.Second)
	if rec := do(t, a, withCookie(httptest.NewRequest("GET", "/", nil), c)); rec.Code != 200 {
		t.Fatalf("GET / a second before 7 days = %d, want 200", rec.Code)
	}
	now = t0.Add(7 * 24 * time.Hour)
	assertSentToSignIn(t, do(t, a, withCookie(httptest.NewRequest("GET", "/", nil), c)), false)
	assertSentToSignIn(t, do(t, a, withCookie(htmxGet("/proxy-hosts/status"), c)), true)
}

func TestRestartSignsEveryoneOut(t *testing.T) {
	env := formLogin(t.TempDir())
	c := session(t, newApp(t, env))

	assertSentToSignIn(t, do(t, newApp(t, env), withCookie(httptest.NewRequest("GET", "/", nil), c)), false)
}

func TestTamperedSessionIsRefused(t *testing.T) {
	a := newApp(t, formLogin(t.TempDir()))
	c := session(t, a)
	for _, value := range []string{
		"9" + c.Value, // a later expiry under the same signature
		c.Value[:len(c.Value)-1] + string(c.Value[len(c.Value)-1]^1),
		"", "x", ".",
	} {
		rec := do(t, a, withCookie(httptest.NewRequest("GET", "/", nil), &http.Cookie{Name: c.Name, Value: value}))
		assertSentToSignIn(t, rec, false)
	}
}

func TestCrossSiteSignInAndOutAreRefused(t *testing.T) {
	a := newApp(t, formLogin(t.TempDir()))
	c := session(t, a)
	for _, req := range []*http.Request{
		httptest.NewRequest("POST", "/login", strings.NewReader("username=admin&password=s3cret")),
		withCookie(httptest.NewRequest("POST", "/logout", nil), c),
	} {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Sec-Fetch-Site", "cross-site")
		rec := do(t, a, req)
		if rec.Code != http.StatusForbidden || len(rec.Result().Cookies()) != 0 {
			t.Errorf("cross-site %s %s = %d, cookies %v; want 403 and none", req.Method, req.URL.Path, rec.Code, rec.Result().Cookies())
		}
	}
	if rec := do(t, a, withCookie(httptest.NewRequest("GET", "/", nil), c)); rec.Code != 200 {
		t.Fatalf("GET / after a cross-site sign out = %d, want the session to last", rec.Code)
	}
}

func TestSignInAndOutAreA404InBasicStyle(t *testing.T) {
	a := newApp(t, basicLogin(t.TempDir()))
	for _, req := range []*http.Request{
		httptest.NewRequest("GET", "/login", nil),
		httptest.NewRequest("POST", "/login", nil),
		httptest.NewRequest("POST", "/logout", nil),
	} {
		if rec := do(t, a, withBasic(req, "admin", "s3cret")); rec.Code != http.StatusNotFound {
			t.Errorf("%s %s = %d, want 404", req.Method, req.URL.Path, rec.Code)
		}
	}
}
