package app_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"gitlab.example.com/home/devops/traefik-proxy-manager/internal/app"
)

var t0 = time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)

// internalOnly is an install that configures only the Internal side.
func internalOnly(dataDir string) map[string]string {
	return map[string]string{
		"TPM_TRAEFIK_API_URL":           "http://127.0.0.1:1", // refused: Traefik is unreachable
		"TPM_ENTRYPOINT_INTERNAL_HTTP":  "web-int",
		"TPM_ENTRYPOINT_INTERNAL_HTTPS": "websecure-int",
		"TPM_CERT_RESOLVERS":            "le-dns,le-http",
		"TPM_DATA_DIR":                  dataDir,
	}
}

func newApp(t *testing.T, env map[string]string) *app.App {
	t.Helper()
	// The clock ticks a second per reading, so each save gets a new updatedAt.
	now := t0.Add(-time.Second)
	a, err := app.New(func(k string) string { return env[k] }, func() time.Time { now = now.Add(time.Second); return now })
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return a
}

func do(t *testing.T, h http.Handler, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// traefikConfig fetches the snapshot the way Traefik's HTTP provider does.
func traefikConfig(t *testing.T, h http.Handler) map[string]any {
	t.Helper()
	rec := do(t, h, httptest.NewRequest("GET", "/traefik/config", nil))
	if rec.Code != 200 {
		t.Fatalf("GET /traefik/config = %d, want 200: %s", rec.Code, rec.Body)
	}
	var snap map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &snap); err != nil {
		t.Fatalf("snapshot isn't JSON: %v\n%s", err, rec.Body)
	}
	return snap
}

// placeholder is the whole snapshot when no Proxy Host is enabled: Traefik
// rejects an empty routers or services map and would keep serving stale hosts.
func placeholderSnapshot(entrypoint string) string {
	return `{"http":{"routers":{"tpm-placeholder":{"entryPoints":["` + entrypoint + `"],"rule":"Host(` + "`tpm-placeholder.invalid`" + `)","service":"noop@internal"}}}}`
}

func assertSnapshot(t *testing.T, h http.Handler, want string) {
	t.Helper()
	var wantSnap map[string]any
	if err := json.Unmarshal([]byte(want), &wantSnap); err != nil {
		t.Fatal(err)
	}
	if got := traefikConfig(t, h); !reflect.DeepEqual(got, wantSnap) {
		gotJSON, _ := json.MarshalIndent(got, "", "  ")
		t.Fatalf("snapshot mismatch, got:\n%s\nwant:\n%s", gotJSON, want)
	}
}

func TestFreshInstallServesOnlyThePlaceholder(t *testing.T) {
	a := newApp(t, internalOnly(t.TempDir()))

	assertSnapshot(t, a, placeholderSnapshot("web-int"))
}

// addHost submits the Add Proxy Host form.
func addHost(t *testing.T, h http.Handler, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	return post(t, h, "/proxy-hosts", form)
}

func hostForm(domains string) url.Values {
	return url.Values{
		"domains":      {domains},
		"scheme":       {"http"},
		"host":         {"192.168.1.10"},
		"port":         {"5000"},
		"certResolver": {"le-http"},
		"enabled":      {"on"},
	}
}

func TestAddedProxyHostIsServedToTraefik(t *testing.T) {
	a := newApp(t, internalOnly(t.TempDir()))

	if rec := addHost(t, a, hostForm("nas.example.com, NAS.home.example.com")); rec.Code != 200 {
		t.Fatalf("POST /proxy-hosts = %d: %s", rec.Code, rec.Body)
	}

	want := `{"http":{
		"routers":{
			"host-1-internal-http":{"entryPoints":["web-int"],"rule":"Host(` + "`nas.example.com`" + `) || Host(` + "`nas.home.example.com`" + `)","service":"host-1"},
			"host-1-internal-https":{"entryPoints":["websecure-int"],"rule":"Host(` + "`nas.example.com`" + `) || Host(` + "`nas.home.example.com`" + `)","service":"host-1","tls":{"certResolver":"le-http"}}
		},
		"services":{"host-1":{"loadBalancer":{"servers":[{"url":"http://192.168.1.10:5000"}]}}}
	}}`
	assertSnapshot(t, a, want)
}

func TestExternalOnlyInstallServesExternalRouters(t *testing.T) {
	env := internalOnly(t.TempDir())
	delete(env, "TPM_ENTRYPOINT_INTERNAL_HTTP")
	delete(env, "TPM_ENTRYPOINT_INTERNAL_HTTPS")
	env["TPM_ENTRYPOINT_EXTERNAL_HTTP"] = "web"
	env["TPM_ENTRYPOINT_EXTERNAL_HTTPS"] = "websecure"
	a := newApp(t, env)

	addHost(t, a, hostForm("blog.example.com"))

	routers := traefikConfig(t, a)["http"].(map[string]any)["routers"].(map[string]any)
	if len(routers) != 2 {
		t.Fatalf("want 2 routers, got %v", routers)
	}
	for name, ep := range map[string]string{"host-1-external-http": "web", "host-1-external-https": "websecure"} {
		r, ok := routers[name].(map[string]any)
		if !ok || !reflect.DeepEqual(r["entryPoints"], []any{ep}) {
			t.Errorf("router %s = %v, want entryPoints [%s]", name, routers[name], ep)
		}
	}
}

func TestExternalOnlyInstallServesThePlaceholderOnItsEntrypoint(t *testing.T) {
	env := internalOnly(t.TempDir())
	delete(env, "TPM_ENTRYPOINT_INTERNAL_HTTP")
	delete(env, "TPM_ENTRYPOINT_INTERNAL_HTTPS")
	env["TPM_ENTRYPOINT_EXTERNAL_HTTP"] = "web"
	env["TPM_ENTRYPOINT_EXTERNAL_HTTPS"] = "websecure"
	a := newApp(t, env)

	assertSnapshot(t, a, placeholderSnapshot("web"))
}

func routerNames(t *testing.T, h http.Handler) []string {
	t.Helper()
	var names []string
	for name := range traefikConfig(t, h)["http"].(map[string]any)["routers"].(map[string]any) {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func TestProxyHostsSurviveRestartAndIDsKeepIncreasing(t *testing.T) {
	env := internalOnly(t.TempDir())
	a := newApp(t, env)
	addHost(t, a, hostForm("one.example.com"))
	addHost(t, a, hostForm("two.example.com"))

	restarted := newApp(t, env)
	addHost(t, restarted, hostForm("three.example.com"))

	want := []string{
		"host-1-internal-http", "host-1-internal-https",
		"host-2-internal-http", "host-2-internal-https",
		"host-3-internal-http", "host-3-internal-https",
	}
	if got := routerNames(t, restarted); !reflect.DeepEqual(got, want) {
		t.Fatalf("routers = %v, want %v", got, want)
	}
}

func TestRestoredDataFileIsServedAndNewHostsContinueFromNextID(t *testing.T) {
	dir := t.TempDir()
	restored := `{ "version": 1, "nextId": 13, "proxyHosts": [ {
	  "id": 12, "enabled": true,
	  "domains": ["nas.example.com"],
	  "forward": { "scheme": "http", "host": "192.168.1.10", "port": 5001 },
	  "exposure": "internal",
	  "forceSsl": false, "certResolver": "le-dns",
	  "middlewares": [],
	  "skipCertCheck": false,
	  "createdAt": "2026-01-01T00:00:00Z", "updatedAt": "2026-01-01T00:00:00Z" } ] }`
	if err := os.WriteFile(filepath.Join(dir, "proxy-hosts.json"), []byte(restored), 0o644); err != nil {
		t.Fatal(err)
	}
	a := newApp(t, internalOnly(dir))

	addHost(t, a, hostForm("new.example.com"))

	want := []string{"host-12-internal-http", "host-12-internal-https", "host-13-internal-http", "host-13-internal-https"}
	if got := routerNames(t, a); !reflect.DeepEqual(got, want) {
		t.Fatalf("routers = %v, want %v", got, want)
	}
	var file struct {
		Version    int              `json:"version"`
		NextID     int              `json:"nextId"`
		ProxyHosts []map[string]any `json:"proxyHosts"`
	}
	b, _ := os.ReadFile(filepath.Join(dir, "proxy-hosts.json"))
	if err := json.Unmarshal(b, &file); err != nil {
		t.Fatal(err)
	}
	if file.Version != 1 || file.NextID != 14 || len(file.ProxyHosts) != 2 {
		t.Fatalf("data file = %s", b)
	}
	saved := file.ProxyHosts[1]
	for k, v := range map[string]any{"id": 13.0, "enabled": true, "exposure": "internal", "certResolver": "le-http", "createdAt": "2026-10-04T12:00:00Z", "updatedAt": "2026-10-04T12:00:00Z"} {
		if saved[k] != v {
			t.Errorf("saved %s = %v, want %v", k, saved[k], v)
		}
	}
	if !reflect.DeepEqual(saved["forward"], map[string]any{"scheme": "http", "host": "192.168.1.10", "port": 5000.0}) {
		t.Errorf("saved forward = %v", saved["forward"])
	}
}

func TestUnparseableDataFileFailsStartup(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "proxy-hosts.json"), []byte(`{"version": 1, "proxyHosts": [`), 0o644)

	env := internalOnly(dir)
	if _, err := app.New(func(k string) string { return env[k] }, time.Now); err == nil || !strings.Contains(err.Error(), "proxy-hosts.json") {
		t.Fatalf("New error = %v, want one naming proxy-hosts.json", err)
	}
}

func TestMissingOrMalformedSettingsFailStartup(t *testing.T) {
	for name, tc := range map[string]struct {
		change  func(env map[string]string)
		wantErr string
	}{
		"no Traefik API URL":  {func(e map[string]string) { delete(e, "TPM_TRAEFIK_API_URL") }, "TPM_TRAEFIK_API_URL"},
		"bad Traefik API URL": {func(e map[string]string) { e["TPM_TRAEFIK_API_URL"] = "traefik:8080" }, "TPM_TRAEFIK_API_URL"},
		"no resolvers":        {func(e map[string]string) { delete(e, "TPM_CERT_RESOLVERS") }, "TPM_CERT_RESOLVERS"},
		"empty resolver list": {func(e map[string]string) { e["TPM_CERT_RESOLVERS"] = " , " }, "TPM_CERT_RESOLVERS"},
		"no side": {func(e map[string]string) {
			delete(e, "TPM_ENTRYPOINT_INTERNAL_HTTP")
			delete(e, "TPM_ENTRYPOINT_INTERNAL_HTTPS")
		}, "TPM_ENTRYPOINT_"},
		"half a side":                      {func(e map[string]string) { e["TPM_ENTRYPOINT_EXTERNAL_HTTP"] = "web" }, "TPM_ENTRYPOINT_EXTERNAL_HTTPS"},
		"bad port":                         {func(e map[string]string) { e["TPM_PORT"] = "eighty" }, "TPM_PORT"},
		"port out of range":                {func(e map[string]string) { e["TPM_PORT"] = "70000" }, "TPM_PORT"},
		"bad response header timeout":      {func(e map[string]string) { e["TPM_RESPONSE_HEADER_TIMEOUT"] = "abc" }, "TPM_RESPONSE_HEADER_TIMEOUT"},
		"zero response header timeout":     {func(e map[string]string) { e["TPM_RESPONSE_HEADER_TIMEOUT"] = "0s" }, "TPM_RESPONSE_HEADER_TIMEOUT"},
		"negative response header timeout": {func(e map[string]string) { e["TPM_RESPONSE_HEADER_TIMEOUT"] = "-5s" }, "TPM_RESPONSE_HEADER_TIMEOUT"},
	} {
		t.Run(name, func(t *testing.T) {
			env := internalOnly(t.TempDir())
			tc.change(env)
			_, err := app.New(func(k string) string { return env[k] }, time.Now)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("New error = %v, want one naming %s", err, tc.wantErr)
			}
		})
	}
}

func TestPortDefaultsTo8080(t *testing.T) {
	env := internalOnly(t.TempDir())
	if port := newApp(t, env).Port; port != 8080 {
		t.Fatalf("Port = %d, want 8080", port)
	}
	env["TPM_PORT"] = "9090"
	if port := newApp(t, env).Port; port != 9090 {
		t.Fatalf("Port = %d, want 9090", port)
	}
}

func get(t *testing.T, h http.Handler, path string) string {
	t.Helper()
	rec := do(t, h, httptest.NewRequest("GET", path, nil))
	if rec.Code != 200 {
		t.Fatalf("GET %s = %d: %s", path, rec.Code, rec.Body)
	}
	return rec.Body.String()
}

func assertContains(t *testing.T, body string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(body, w) {
			t.Errorf("missing %q in:\n%s", w, body)
		}
	}
}

func assertNotContains(t *testing.T, body string, unwanted ...string) {
	t.Helper()
	for _, u := range unwanted {
		if strings.Contains(body, u) {
			t.Errorf("unexpected %q in:\n%s", u, body)
		}
	}
}

func TestListPageUsesEmbeddedAssetsOnly(t *testing.T) {
	a := newApp(t, internalOnly(t.TempDir()))

	page := get(t, a, "/")

	assertContains(t, page, "Proxy Hosts", "Add Proxy Host", `hx-get="/proxy-hosts/new"`, `src="/static/htmx.min.js"`, `src="/static/app.js"`, `href="/static/app.css"`)
	if strings.Contains(page, "://cdn") || strings.Contains(page, "unpkg") {
		t.Errorf("page loads from a CDN:\n%s", page)
	}
	if !strings.HasPrefix(get(t, a, "/static/htmx.min.js"), "var htmx=") {
		t.Error("htmx isn't served from /static")
	}
	assertContains(t, get(t, a, "/static/app.js"), "data-access")
}

func TestListPageHasAppBarWithoutSignOut(t *testing.T) {
	a := newApp(t, internalOnly(t.TempDir()))

	page := get(t, a, "/")

	assertContains(t, page, "<header", ">tpm<", "Traefik Proxy Manager")
	if strings.Contains(page, "Sign out") {
		t.Errorf("app bar offers Sign out without a Form session:\n%s", page)
	}
}

func TestAddModalOffersDomainsTargetAndResolver(t *testing.T) {
	a := newApp(t, internalOnly(t.TempDir()))

	modal := get(t, a, "/proxy-hosts/new")

	assertContains(t, modal, "New Proxy Host", "Domain Names", `name="domains"`, "Send traffic to",
		`name="scheme"`, `name="host"`, `name="port"`, "Certificate Resolver",
		`<option value="le-dns" selected>le-dns</option>`, `<option value="le-http">le-http</option>`,
		`hx-post="/proxy-hosts"`)
}

func TestSavingClosesModalAndListShowsHost(t *testing.T) {
	a := newApp(t, internalOnly(t.TempDir()))

	saved := addHost(t, a, hostForm("nas.example.com, nas.home.example.com")).Body.String()

	assertContains(t, saved, "nas.example.com")
	if strings.Contains(saved, "Send traffic to") {
		t.Errorf("modal still open after save:\n%s", saved)
	}
	assertContains(t, get(t, a, "/"), "nas.example.com", "nas.home.example.com", "http://192.168.1.10:5000")
}

func TestSaveIsBlockedForHostsTraefikWouldReject(t *testing.T) {
	for name, tc := range map[string]struct {
		change  func(f url.Values)
		wantMsg string
	}{
		"no domain":                       {func(f url.Values) { f.Set("domains", " , ") }, "Enter at least one domain name"},
		"bad domain syntax":               {func(f url.Values) { f.Set("domains", "ok.example.com, bad_domain!.com") }, "&#39;bad_domain!.com&#39; isn&#39;t a valid domain name"},
		"backtick domain":                 {func(f url.Values) { f.Set("domains", "a`.example.com") }, "isn&#39;t a valid domain name"},
		"wildcard domain":                 {func(f url.Values) { f.Set("domains", "*.example.com") }, "Wildcard domains like &#39;*.example.com&#39; aren&#39;t supported"},
		"port too high":                   {func(f url.Values) { f.Set("port", "65536") }, "Port must be a number from 1 to 65535"},
		"port zero":                       {func(f url.Values) { f.Set("port", "0") }, "Port must be a number from 1 to 65535"},
		"port not a number":               {func(f url.Values) { f.Set("port", "http") }, "Port must be a number from 1 to 65535"},
		"no forward host":                 {func(f url.Values) { f.Set("host", "") }, "Enter the hostname or IP address to send traffic to"},
		"bad forward host":                {func(f url.Values) { f.Set("host", "10.0.0.1/admin") }, "Enter the hostname or IP address to send traffic to"},
		"unknown scheme":                  {func(f url.Values) { f.Set("scheme", "ftp") }, "Scheme must be http or https"},
		"unlisted resolver":               {func(f url.Values) { f.Set("certResolver", "staging") }, "Pick a Certificate Resolver from the list"},
		"negative response timeout":       {func(f url.Values) { f.Set("responseTimeout", "-5s") }, "Response timeout &#39;-5s&#39; isn&#39;t valid. Use a duration such as 10m, or 0 for no limit"},
		"response timeout not a duration": {func(f url.Values) { f.Set("responseTimeout", "abc") }, "Response timeout &#39;abc&#39; isn&#39;t valid"},
		"response timeout without a unit": {func(f url.Values) { f.Set("responseTimeout", "30") }, "Response timeout &#39;30&#39; isn&#39;t valid"},
	} {
		t.Run(name, func(t *testing.T) {
			a := newApp(t, internalOnly(t.TempDir()))
			form := hostForm("nas.example.com")
			tc.change(form)

			rec := addHost(t, a, form)

			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("POST /proxy-hosts = %d, want 422", rec.Code)
			}
			assertContains(t, rec.Body.String(), tc.wantMsg, "New Proxy Host")
			assertSnapshot(t, a, placeholderSnapshot("web-int"))
		})
	}
}

func TestBlockedSaveKeepsWhatWasTyped(t *testing.T) {
	a := newApp(t, internalOnly(t.TempDir()))
	form := hostForm("nas.example.com *.example.com")
	form.Set("certResolver", "le-http")

	body := addHost(t, a, form).Body.String()

	assertContains(t, body, `value="nas.example.com *.example.com"`, `value="192.168.1.10"`, `value="5000"`, `<option value="le-http" selected>`)
}

func TestConfigIs503WhenAStoredHostCantBeRendered(t *testing.T) {
	dir := t.TempDir()
	// A restored file with a host tpm's own Save would have refused.
	os.WriteFile(filepath.Join(dir, "proxy-hosts.json"), []byte(`{"version": 1, "nextId": 3, "proxyHosts": [
	  {"id": 1, "enabled": true, "domains": ["ok.example.com"], "forward": {"scheme": "http", "host": "10.0.0.1", "port": 80}, "exposure": "internal", "certResolver": "le-dns", "middlewares": []},
	  {"id": 2, "enabled": true, "domains": ["evil`+"`"+`) || Host(`+"`"+`x.com"], "forward": {"scheme": "http", "host": "10.0.0.2", "port": 80}, "exposure": "internal", "certResolver": "le-dns", "middlewares": []}
	]}`), 0o644)
	a := newApp(t, internalOnly(dir))

	rec := do(t, a, httptest.NewRequest("GET", "/traefik/config", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET /traefik/config = %d, want 503: %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "routers") {
		t.Fatalf("503 carries a partial snapshot: %s", rec.Body)
	}
}

func TestStaleNextIDInRestoredFileNeverDuplicatesAnID(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "proxy-hosts.json"), []byte(`{"version": 1, "nextId": 1, "proxyHosts": [
	  {"id": 1, "enabled": true, "domains": ["Old.Example.com"], "forward": {"scheme": "http", "host": "10.0.0.1", "port": 80}, "exposure": "internal", "certResolver": "le-dns", "middlewares": []}
	]}`), 0o644)
	a := newApp(t, internalOnly(dir))

	addHost(t, a, hostForm("new.example.com"))

	want := []string{"host-1-internal-http", "host-1-internal-https", "host-2-internal-http", "host-2-internal-https"}
	if got := routerNames(t, a); !reflect.DeepEqual(got, want) {
		t.Fatalf("routers = %v, want %v", got, want)
	}
}

func restoreHosts(t *testing.T, dir, hosts string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "proxy-hosts.json"), []byte(`{"version": 1, "nextId": 10, "proxyHosts": [`+hosts+`]}`), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRestoredHostsAreServedByTheirStoredSettings(t *testing.T) {
	dir := t.TempDir()
	restoreHosts(t, dir, `
	  {"id": 1, "enabled": true, "domains": ["both.example.com"], "forward": {"scheme": "http", "host": "10.0.0.1", "port": 80}, "exposure": "both", "certResolver": "le-dns", "middlewares": []},
	  {"id": 2, "enabled": false, "domains": ["off.example.com"], "forward": {"scheme": "http", "host": "10.0.0.2", "port": 80}, "exposure": "external", "certResolver": "le-dns", "middlewares": []},
	  {"id": 3, "enabled": true, "domains": ["nas.example.com"], "forward": {"scheme": "https", "host": "10.0.0.3", "port": 5001}, "exposure": "internal", "certResolver": "le-dns", "middlewares": []}`)
	env := internalOnly(dir)
	env["TPM_ENTRYPOINT_EXTERNAL_HTTP"] = "web"
	env["TPM_ENTRYPOINT_EXTERNAL_HTTPS"] = "websecure"
	a := newApp(t, env)

	want := []string{
		"host-1-external-http", "host-1-external-https", "host-1-internal-http", "host-1-internal-https",
		"host-3-internal-http", "host-3-internal-https",
	}
	if got := routerNames(t, a); !reflect.DeepEqual(got, want) {
		t.Fatalf("routers = %v, want %v", got, want)
	}
	svc := traefikConfig(t, a)["http"].(map[string]any)["services"].(map[string]any)["host-3"]
	if !strings.Contains(fmt.Sprint(svc), "https://10.0.0.3:5001") {
		t.Fatalf("host-3 service = %v", svc)
	}
}

func TestConfigIs503ForAStoredHostWithAnInvalidResponseTimeout(t *testing.T) {
	dir := t.TempDir()
	restoreHosts(t, dir, `{"id": 1, "enabled": true, "domains": ["a.example.com"], "forward": {"scheme": "http", "host": "10.0.0.1", "port": 80}, "exposure": "internal", "certResolver": "le-dns", "middlewares": [], "responseTimeout": "soon"}`)
	a := newApp(t, internalOnly(dir))

	if rec := do(t, a, httptest.NewRequest("GET", "/traefik/config", nil)); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET /traefik/config = %d, want 503: %s", rec.Code, rec.Body)
	}
}

func TestConfigIs503ForAStoredHostWithUnknownExposure(t *testing.T) {
	dir := t.TempDir()
	restoreHosts(t, dir, `{"id": 1, "enabled": true, "domains": ["a.example.com"], "forward": {"scheme": "http", "host": "10.0.0.1", "port": 80}, "exposure": "everyone", "certResolver": "le-dns", "middlewares": []}`)
	a := newApp(t, internalOnly(dir))

	if rec := do(t, a, httptest.NewRequest("GET", "/traefik/config", nil)); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET /traefik/config = %d, want 503: %s", rec.Code, rec.Body)
	}
}

func TestConfigIs404ThroughAnyTraefikRouter(t *testing.T) {
	a := newApp(t, internalOnly(t.TempDir()))
	for _, h := range []string{"X-Forwarded-For", "X-Forwarded-Proto", "X-Forwarded-Port", "X-Forwarded-Server", "X-Real-Ip"} {
		req := httptest.NewRequest("GET", "/traefik/config", nil)
		req.Header.Set(h, "x")
		if rec := do(t, a, req); rec.Code != http.StatusNotFound {
			t.Errorf("GET /traefik/config with %s = %d, want 404: %s", h, rec.Code, rec.Body)
		}
		req = httptest.NewRequest("GET", "/", nil)
		req.Header.Set(h, "x")
		if rec := do(t, a, req); rec.Code != 200 {
			t.Errorf("GET / with %s = %d, want 200", h, rec.Code)
		}
	}
	traefikConfig(t, a) // Traefik's own poll carries none of them
}

func TestStylesheetIsEmbedded(t *testing.T) {
	a := newApp(t, internalOnly(t.TempDir()))
	assertContains(t, get(t, a, "/static/app.css"), "tailwindcss")
}

func TestCrossSiteFormPostIsRefused(t *testing.T) {
	a := newApp(t, internalOnly(t.TempDir()))
	req := httptest.NewRequest("POST", "/proxy-hosts", strings.NewReader(hostForm("evil.example.com").Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Sec-Fetch-Site", "cross-site")

	if rec := do(t, a, req); rec.Code != http.StatusForbidden {
		t.Fatalf("cross-site POST /proxy-hosts = %d, want 403", rec.Code)
	}
	assertSnapshot(t, a, placeholderSnapshot("web-int"))
}

func post(t *testing.T, h http.Handler, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return do(t, h, req)
}

var hiddenUpdatedAt = regexp.MustCompile(`name="updatedAt" value="([^"]+)"`)

// editForm opens a host's edit modal and returns it with the updatedAt it carries.
func editForm(t *testing.T, h http.Handler, id int) (string, string) {
	t.Helper()
	modal := get(t, h, fmt.Sprintf("/proxy-hosts/%d/edit", id))
	m := hiddenUpdatedAt.FindStringSubmatch(modal)
	if m == nil {
		t.Fatalf("edit modal carries no updatedAt:\n%s", modal)
	}
	return modal, m[1]
}

func TestEditingAHostKeepsItsID(t *testing.T) {
	a := newApp(t, internalOnly(t.TempDir()))
	addHost(t, a, hostForm("nas.example.com"))

	modal, updatedAt := editForm(t, a, 1)
	assertContains(t, modal, "Edit Proxy Host", `value="nas.example.com"`, `value="192.168.1.10"`, `value="5000"`,
		`<option value="le-http" selected>`, `hx-post="/proxy-hosts/1"`)

	form := hostForm("files.example.com")
	form.Set("port", "6000")
	form.Set("updatedAt", updatedAt)
	if rec := post(t, a, "/proxy-hosts/1", form); rec.Code != 200 {
		t.Fatalf("POST /proxy-hosts/1 = %d: %s", rec.Code, rec.Body)
	}

	snap := traefikConfig(t, a)["http"].(map[string]any)
	if got := routerNames(t, a); !reflect.DeepEqual(got, []string{"host-1-internal-http", "host-1-internal-https"}) {
		t.Fatalf("routers = %v", got)
	}
	assertContains(t, fmt.Sprint(snap["routers"]), "Host(`files.example.com`)")
	assertContains(t, fmt.Sprint(snap["services"]), "http://192.168.1.10:6000")
	assertContains(t, get(t, a, "/"), "files.example.com")
}

func TestSaveIsRefusedWhenSomeoneElseChangedTheHost(t *testing.T) {
	a := newApp(t, internalOnly(t.TempDir()))
	addHost(t, a, hostForm("nas.example.com"))
	_, mine := editForm(t, a, 1)
	_, theirs := editForm(t, a, 1)
	form := hostForm("theirs.example.com")
	form.Set("updatedAt", theirs)
	post(t, a, "/proxy-hosts/1", form)

	form = hostForm("mine.example.com")
	form.Set("updatedAt", mine)
	rec := post(t, a, "/proxy-hosts/1", form)

	if rec.Code != http.StatusConflict {
		t.Fatalf("stale save = %d, want 409", rec.Code)
	}
	assertContains(t, rec.Body.String(), "This host was changed by someone else; reload.", `value="mine.example.com"`)
	assertContains(t, fmt.Sprint(traefikConfig(t, a)), "Host(`theirs.example.com`)")
}

func TestEditIsBlockedForHostsTraefikWouldReject(t *testing.T) {
	a := newApp(t, internalOnly(t.TempDir()))
	addHost(t, a, hostForm("nas.example.com"))
	_, updatedAt := editForm(t, a, 1)
	form := hostForm("*.example.com")
	form.Set("updatedAt", updatedAt)

	rec := post(t, a, "/proxy-hosts/1", form)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("POST /proxy-hosts/1 = %d, want 422", rec.Code)
	}
	assertContains(t, rec.Body.String(), "Wildcard domains", `hx-post="/proxy-hosts/1"`, "Edit Proxy Host")
	assertContains(t, fmt.Sprint(traefikConfig(t, a)), "Host(`nas.example.com`)")
}

func TestUnknownHostIsNotFound(t *testing.T) {
	a := newApp(t, internalOnly(t.TempDir()))
	for _, req := range []*http.Request{
		httptest.NewRequest("GET", "/proxy-hosts/7/edit", nil),
		httptest.NewRequest("POST", "/proxy-hosts/7", strings.NewReader(hostForm("a.example.com").Encode())),
		httptest.NewRequest("DELETE", "/proxy-hosts/7", nil),
		httptest.NewRequest("POST", "/proxy-hosts/7/disable", nil),
	} {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if rec := do(t, a, req); rec.Code != http.StatusNotFound {
			t.Errorf("%s %s = %d, want 404", req.Method, req.URL, rec.Code)
		}
	}
}

func TestDeletingAHostRemovesItFromTraefik(t *testing.T) {
	a := newApp(t, internalOnly(t.TempDir()))
	addHost(t, a, hostForm("one.example.com"))
	addHost(t, a, hostForm("two.example.com"))

	rec := do(t, a, httptest.NewRequest("DELETE", "/proxy-hosts/1", nil))

	if rec.Code != 200 {
		t.Fatalf("DELETE /proxy-hosts/1 = %d: %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "one.example.com") {
		t.Errorf("list still shows the deleted host:\n%s", rec.Body)
	}
	if got := routerNames(t, a); !reflect.DeepEqual(got, []string{"host-2-internal-http", "host-2-internal-https"}) {
		t.Fatalf("routers = %v", got)
	}
}

func TestDeletingTheLastHostLeavesOnlyThePlaceholder(t *testing.T) {
	a := newApp(t, internalOnly(t.TempDir()))
	addHost(t, a, hostForm("one.example.com"))

	do(t, a, httptest.NewRequest("DELETE", "/proxy-hosts/1", nil))

	assertSnapshot(t, a, placeholderSnapshot("web-int"))
}

func TestIDsAreNeverReusedAfterDeletingTheHighestAndRestarting(t *testing.T) {
	env := internalOnly(t.TempDir())
	a := newApp(t, env)
	addHost(t, a, hostForm("one.example.com"))
	addHost(t, a, hostForm("two.example.com"))
	do(t, a, httptest.NewRequest("DELETE", "/proxy-hosts/2", nil))

	restarted := newApp(t, env)
	addHost(t, restarted, hostForm("three.example.com"))

	want := []string{"host-1-internal-http", "host-1-internal-https", "host-3-internal-http", "host-3-internal-https"}
	if got := routerNames(t, restarted); !reflect.DeepEqual(got, want) {
		t.Fatalf("routers = %v, want %v", got, want)
	}
}

func TestDisablingAHostFromItsRowTakesItOutOfTraefik(t *testing.T) {
	a := newApp(t, internalOnly(t.TempDir()))
	addHost(t, a, hostForm("nas.example.com"))
	assertContains(t, get(t, a, "/"), `hx-get="/proxy-hosts/1/edit"`, `hx-post="/proxy-hosts/1/disable"`, `hx-delete="/proxy-hosts/1"`)

	rec := post(t, a, "/proxy-hosts/1/disable", nil)

	if rec.Code != 200 {
		t.Fatalf("disable = %d: %s", rec.Code, rec.Body)
	}
	assertContains(t, rec.Body.String(), "Disabled", `hx-post="/proxy-hosts/1/enable"`)
	assertSnapshot(t, a, placeholderSnapshot("web-int"))

	post(t, a, "/proxy-hosts/1/enable", nil)

	if got := routerNames(t, a); len(got) != 2 {
		t.Fatalf("re-enabled host not served: %v", got)
	}
	if strings.Contains(get(t, a, "/"), "Disabled") {
		t.Error("re-enabled host still shows Disabled")
	}
}

func TestEnabledToggleInTheModal(t *testing.T) {
	a := newApp(t, internalOnly(t.TempDir()))
	assertContains(t, get(t, a, "/proxy-hosts/new"), `name="enabled" checked`)
	form := hostForm("nas.example.com")
	form.Del("enabled")

	addHost(t, a, form)

	assertSnapshot(t, a, placeholderSnapshot("web-int"))
	modal, updatedAt := editForm(t, a, 1)
	if strings.Contains(modal, `name="enabled" checked`) {
		t.Fatal("edit modal shows a disabled host as enabled")
	}
	form = hostForm("nas.example.com")
	form.Set("updatedAt", updatedAt)
	post(t, a, "/proxy-hosts/1", form)

	if got := routerNames(t, a); len(got) != 2 {
		t.Fatalf("host switched on in the modal isn't served: %v", got)
	}
}

func TestEditNeverSwapsAnUnconfiguredResolverBehindTheUsersBack(t *testing.T) {
	dir := t.TempDir()
	restoreHosts(t, dir, `{"id": 1, "enabled": true, "domains": ["a.example.com"], "forward": {"scheme": "http", "host": "10.0.0.1", "port": 80}, "exposure": "internal", "certResolver": "old-le", "middlewares": [], "updatedAt": "2026-01-01T00:00:00Z"}`)
	a := newApp(t, internalOnly(dir))

	modal, updatedAt := editForm(t, a, 1)

	assertContains(t, modal, `<option value="old-le" selected>old-le (not configured)</option>`)
	if strings.Contains(modal, `value="le-dns" selected`) {
		t.Fatal("edit form pre-selects a different resolver")
	}
	form := hostForm("a.example.com")
	form.Set("certResolver", "old-le")
	form.Set("updatedAt", updatedAt)
	if rec := post(t, a, "/proxy-hosts/1", form); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("save with unconfigured resolver = %d, want 422", rec.Code)
	}
}

func TestInvalidEditOfUnknownHostIsNotFound(t *testing.T) {
	a := newApp(t, internalOnly(t.TempDir()))
	if rec := post(t, a, "/proxy-hosts/7", hostForm("*.example.com")); rec.Code != http.StatusNotFound {
		t.Fatalf("POST /proxy-hosts/7 = %d, want 404", rec.Code)
	}
}

// internalAndExternal is an install that configures Internal and External.
func internalAndExternal(dataDir string) map[string]string {
	env := internalOnly(dataDir)
	env["TPM_ENTRYPOINT_EXTERNAL_HTTP"] = "web"
	env["TPM_ENTRYPOINT_EXTERNAL_HTTPS"] = "websecure"
	return env
}

func TestBothSidesInstallServesThePlaceholderOnInternal(t *testing.T) {
	a := newApp(t, internalAndExternal(t.TempDir()))

	assertSnapshot(t, a, placeholderSnapshot("web-int"))
}

func TestBothSidesInstallOffersExposureCardsDefaultingToInternal(t *testing.T) {
	a := newApp(t, internalAndExternal(t.TempDir()))

	modal := get(t, a, "/proxy-hosts/new")

	assertContains(t, modal, "Who can reach it",
		`value="internal" checked`, "Internal", "Reachable from your network only",
		`value="external"`, "External", "Reachable from the internet only",
		`value="both"`, "Both", "Reachable from your network and the internet")
	if strings.Contains(modal, `value="external" checked`) || strings.Contains(modal, `value="both" checked`) {
		t.Errorf("want only Internal checked:\n%s", modal)
	}
}

func TestExposureDecidesWhichSidesServeTheHost(t *testing.T) {
	for exposure, want := range map[string]map[string]string{
		"internal": {"host-1-internal-http": "web-int", "host-1-internal-https": "websecure-int"},
		"external": {"host-1-external-http": "web", "host-1-external-https": "websecure"},
		"both": {
			"host-1-internal-http": "web-int", "host-1-internal-https": "websecure-int",
			"host-1-external-http": "web", "host-1-external-https": "websecure",
		},
	} {
		t.Run(exposure, func(t *testing.T) {
			a := newApp(t, internalAndExternal(t.TempDir()))
			form := hostForm("nas.example.com")
			form.Set("exposure", exposure)

			if rec := addHost(t, a, form); rec.Code != 200 {
				t.Fatalf("POST /proxy-hosts = %d: %s", rec.Code, rec.Body)
			}

			routers := traefikConfig(t, a)["http"].(map[string]any)["routers"].(map[string]any)
			if len(routers) != len(want) {
				t.Fatalf("routers = %v, want %v", routers, want)
			}
			for name, ep := range want {
				r, _ := routers[name].(map[string]any)
				if !reflect.DeepEqual(r["entryPoints"], []any{ep}) || r["service"] != "host-1" {
					t.Errorf("router %s = %v, want entryPoints [%s] and service host-1", name, routers[name], ep)
				}
			}
			modal, _ := editForm(t, a, 1)
			assertContains(t, modal, `value="`+exposure+`" checked`)
		})
	}
}

func TestSingleSideInstallHidesExposureAndUsesThatSide(t *testing.T) {
	a := newApp(t, internalOnly(t.TempDir()))

	if modal := get(t, a, "/proxy-hosts/new"); strings.Contains(modal, "Who can reach it") || strings.Contains(modal, `type="radio" name="exposure"`) {
		t.Fatalf("single-side install offers an Exposure choice:\n%s", modal)
	}
	form := hostForm("nas.example.com")
	form.Set("exposure", "external")
	addHost(t, a, form)

	if got := routerNames(t, a); !reflect.DeepEqual(got, []string{"host-1-internal-http", "host-1-internal-https"}) {
		t.Fatalf("routers = %v", got)
	}
}

func TestHostKeepsItsExposureWhenASideIsRemovedAndReAdded(t *testing.T) {
	dir := t.TempDir()
	restoreHosts(t, dir, `{"id": 1, "enabled": true, "domains": ["photos.example.com"], "forward": {"scheme": "http", "host": "10.0.0.1", "port": 80}, "exposure": "both", "certResolver": "le-dns", "middlewares": [], "updatedAt": "2026-01-01T00:00:00Z"}`)
	a := newApp(t, internalOnly(dir))

	if got := routerNames(t, a); !reflect.DeepEqual(got, []string{"host-1-internal-http", "host-1-internal-https"}) {
		t.Fatalf("routers with External removed = %v", got)
	}
	_, updatedAt := editForm(t, a, 1)
	form := hostForm("photos.example.com")
	form.Set("updatedAt", updatedAt)
	if rec := post(t, a, "/proxy-hosts/1", form); rec.Code != 200 {
		t.Fatalf("POST /proxy-hosts/1 = %d: %s", rec.Code, rec.Body)
	}

	readded := newApp(t, internalAndExternal(dir))

	want := []string{"host-1-external-http", "host-1-external-https", "host-1-internal-http", "host-1-internal-https"}
	if got := routerNames(t, readded); !reflect.DeepEqual(got, want) {
		t.Fatalf("routers with External re-added = %v, want %v", got, want)
	}
}

func TestListShowsExposureBadgesInPlainWords(t *testing.T) {
	a := newApp(t, internalAndExternal(t.TempDir()))
	for domain, exposure := range map[string]string{"in.example.com": "internal", "out.example.com": "external", "all.example.com": "both"} {
		form := hostForm(domain)
		form.Set("exposure", exposure)
		addHost(t, a, form)
	}

	list := get(t, a, "/")

	assertContains(t, list, ">Exposure</th>", ">Internal</span>", ">External</span>", ">Both</span>")
	for _, page := range []string{list, get(t, a, "/proxy-hosts/new"), get(t, a, "/proxy-hosts/1/edit")} {
		if lower := strings.ToLower(page); strings.Contains(lower, "entrypoint") || strings.Contains(lower, "router") {
			t.Errorf("user-facing page mentions Traefik internals:\n%s", page)
		}
	}
}

func TestModalOffersForceSSLAndSkipCertificateCheck(t *testing.T) {
	a := newApp(t, internalOnly(t.TempDir()))

	modal := get(t, a, "/proxy-hosts/new")

	assertContains(t, modal, "Force SSL", `name="forceSsl" checked`, "Skip certificate check", `name="skipCertCheck"`)
	if strings.Contains(modal, `name="skipCertCheck" checked`) {
		t.Errorf("new host skips the certificate check:\n%s", modal)
	}

	form := hostForm("nas.example.com")
	form.Set("scheme", "https")
	form.Set("skipCertCheck", "on")
	addHost(t, a, form)
	modal, _ = editForm(t, a, 1)

	assertContains(t, modal, `name="skipCertCheck" checked`, `<option value="https" selected>`)
	if strings.Contains(modal, `name="forceSsl" checked`) {
		t.Errorf("host saved without Force SSL shows it on:\n%s", modal)
	}
}

func TestForceSSLRedirectsPlainHTTPThroughOneSharedMiddleware(t *testing.T) {
	a := newApp(t, internalAndExternal(t.TempDir()))
	for _, d := range []string{"one.example.com", "two.example.com"} {
		form := hostForm(d)
		form.Set("exposure", "both")
		form.Set("forceSsl", "on")
		addHost(t, a, form)
	}
	legacy := hostForm("legacy.example.com") // Force SSL off
	legacy.Set("exposure", "internal")
	addHost(t, a, legacy)

	snap := traefikConfig(t, a)["http"].(map[string]any)
	routers := snap["routers"].(map[string]any)

	for _, name := range []string{"host-1-internal-http", "host-1-external-http", "host-2-internal-http", "host-2-external-http"} {
		if mw := routers[name].(map[string]any)["middlewares"]; !reflect.DeepEqual(mw, []any{"tpm-force-ssl"}) {
			t.Errorf("router %s middlewares = %v, want [tpm-force-ssl]", name, mw)
		}
	}
	for name, svc := range map[string]string{"host-1-internal-https": "host-1", "host-3-internal-http": "host-3"} {
		r := routers[name].(map[string]any)
		if r["middlewares"] != nil || r["service"] != svc {
			t.Errorf("router %s = %v, want it forwarding to its service with no redirect", name, r)
		}
	}
	want := map[string]any{"tpm-force-ssl": map[string]any{"redirectScheme": map[string]any{"scheme": "https", "permanent": true}}}
	if !reflect.DeepEqual(snap["middlewares"], want) {
		t.Errorf("middlewares = %v, want %v", snap["middlewares"], want)
	}
}

func TestSkipCertificateCheckUsesOneSharedTransportForHTTPSTargets(t *testing.T) {
	a := newApp(t, internalOnly(t.TempDir()))
	for _, tc := range []struct{ domain, scheme string }{
		{"nas.example.com", "https"},
		{"proxmox.example.com", "https"},
		{"plain.example.com", "http"}, // the toggle is hidden for http
	} {
		form := hostForm(tc.domain)
		form.Set("scheme", tc.scheme)
		form.Set("skipCertCheck", "on")
		addHost(t, a, form)
	}
	checked := hostForm("checked.example.com")
	checked.Set("scheme", "https")
	addHost(t, a, checked)

	snap := traefikConfig(t, a)["http"].(map[string]any)
	services := snap["services"].(map[string]any)

	for svc, want := range map[string]any{"host-1": "tpm-skip-cert-check", "host-2": "tpm-skip-cert-check", "host-3": nil, "host-4": nil} {
		if got := services[svc].(map[string]any)["loadBalancer"].(map[string]any)["serversTransport"]; got != want {
			t.Errorf("service %s serversTransport = %v, want %v", svc, got, want)
		}
	}
	want := map[string]any{"tpm-skip-cert-check": map[string]any{"insecureSkipVerify": true}}
	if !reflect.DeepEqual(snap["serversTransports"], want) {
		t.Errorf("serversTransports = %v, want %v", snap["serversTransports"], want)
	}
	if modal, _ := editForm(t, a, 3); strings.Contains(modal, `name="skipCertCheck" checked`) {
		t.Error("http host was saved skipping the certificate check")
	}
}

func TestResponseHeaderTimeoutGoesOnTheSkipCertCheckTransport(t *testing.T) {
	env := internalOnly(t.TempDir())
	env["TPM_RESPONSE_HEADER_TIMEOUT"] = "30s"
	a := newApp(t, env)
	form := hostForm("nas.example.com")
	form.Set("scheme", "https")
	form.Set("skipCertCheck", "on")

	addHost(t, a, form)

	want := map[string]any{"tpm-skip-cert-check": map[string]any{"insecureSkipVerify": true, "forwardingTimeouts": map[string]any{"responseHeaderTimeout": "30s"}}}
	if got := traefikConfig(t, a)["http"].(map[string]any)["serversTransports"]; !reflect.DeepEqual(got, want) {
		t.Errorf("serversTransports = %v, want %v", got, want)
	}
}

func TestResponseHeaderTimeoutAloneAddsNoTransport(t *testing.T) {
	env := internalOnly(t.TempDir())
	env["TPM_RESPONSE_HEADER_TIMEOUT"] = "30s"
	a := newApp(t, env)

	addHost(t, a, hostForm("nas.example.com"))

	if got, ok := traefikConfig(t, a)["http"].(map[string]any)["serversTransports"]; ok {
		t.Errorf("serversTransports = %v, want none", got)
	}
}

func TestResponseTimeoutGivesAProxyHostItsOwnTransport(t *testing.T) {
	a := newApp(t, internalOnly(t.TempDir()))
	form := hostForm("ai.example.com")
	form.Set("responseTimeout", "10m")

	addHost(t, a, form)

	snap := traefikConfig(t, a)["http"].(map[string]any)
	want := map[string]any{"host-1": map[string]any{"insecureSkipVerify": false, "forwardingTimeouts": map[string]any{"responseHeaderTimeout": "10m"}}}
	if !reflect.DeepEqual(snap["serversTransports"], want) {
		t.Errorf("serversTransports = %v, want %v", snap["serversTransports"], want)
	}
	if got := snap["services"].(map[string]any)["host-1"].(map[string]any)["loadBalancer"].(map[string]any)["serversTransport"]; got != "host-1" {
		t.Errorf("service host-1 serversTransport = %v, want host-1", got)
	}
}

func TestResponseTimeoutReplacesTheSharedSkipCertCheckTransport(t *testing.T) {
	env := internalOnly(t.TempDir())
	env["TPM_RESPONSE_HEADER_TIMEOUT"] = "30s"
	a := newApp(t, env)
	form := hostForm("nas.example.com")
	form.Set("scheme", "https")
	form.Set("skipCertCheck", "on")
	form.Set("responseTimeout", "10m")

	addHost(t, a, form)

	snap := traefikConfig(t, a)["http"].(map[string]any)
	want := map[string]any{"host-1": map[string]any{"insecureSkipVerify": true, "forwardingTimeouts": map[string]any{"responseHeaderTimeout": "10m"}}}
	if !reflect.DeepEqual(snap["serversTransports"], want) {
		t.Errorf("serversTransports = %v, want %v", snap["serversTransports"], want)
	}
	if got := snap["services"].(map[string]any)["host-1"].(map[string]any)["loadBalancer"].(map[string]any)["serversTransport"]; got != "host-1" {
		t.Errorf("service host-1 serversTransport = %v, want host-1", got)
	}
}

func TestAnyZeroResponseTimeoutGenerates0s(t *testing.T) {
	for _, value := range []string{"0", "0s", "0m", "-0s"} {
		t.Run(value, func(t *testing.T) {
			a := newApp(t, internalOnly(t.TempDir()))
			form := hostForm("ai.example.com")
			form.Set("responseTimeout", value)

			addHost(t, a, form)

			want := map[string]any{"host-1": map[string]any{"insecureSkipVerify": false, "forwardingTimeouts": map[string]any{"responseHeaderTimeout": "0s"}}}
			if got := traefikConfig(t, a)["http"].(map[string]any)["serversTransports"]; !reflect.DeepEqual(got, want) {
				t.Errorf("serversTransports = %v, want %v", got, want)
			}
		})
	}
}

func TestSavedResponseTimeoutShowsAgainWhenEditing(t *testing.T) {
	a := newApp(t, internalOnly(t.TempDir()))
	form := hostForm("ai.example.com")
	form.Set("responseTimeout", "10m")
	addHost(t, a, form)

	modal, _ := editForm(t, a, 1)

	assertContains(t, modal, "Response timeout", `name="responseTimeout" value="10m"`)
}

func TestBlockedSaveKeepsTheResponseTimeoutAsTyped(t *testing.T) {
	a := newApp(t, internalOnly(t.TempDir()))
	form := hostForm("ai.example.com")
	form.Set("responseTimeout", "soon")

	body := addHost(t, a, form).Body.String()

	assertContains(t, body, `name="responseTimeout" value="soon"`)
}

func TestClearingAResponseTimeoutGeneratesNoTransport(t *testing.T) {
	a := newApp(t, internalOnly(t.TempDir()))
	form := hostForm("ai.example.com")
	form.Set("responseTimeout", "10m")
	addHost(t, a, form)
	_, updatedAt := editForm(t, a, 1)

	form.Set("responseTimeout", "")
	form.Set("updatedAt", updatedAt)
	if rec := post(t, a, "/proxy-hosts/1", form); rec.Code != 200 {
		t.Fatalf("POST /proxy-hosts/1 = %d: %s", rec.Code, rec.Body)
	}

	snap := traefikConfig(t, a)["http"].(map[string]any)
	if got, ok := snap["serversTransports"]; ok {
		t.Errorf("serversTransports = %v, want none", got)
	}
	if got := snap["services"].(map[string]any)["host-1"].(map[string]any)["loadBalancer"].(map[string]any)["serversTransport"]; got != nil {
		t.Errorf("service host-1 serversTransport = %v, want none", got)
	}
}

func TestResponseTimeoutIsInTheDataFileOnlyWhenSet(t *testing.T) {
	dir := t.TempDir()
	a := newApp(t, internalOnly(dir))
	addHost(t, a, hostForm("plain.example.com"))
	timed := hostForm("ai.example.com")
	timed.Set("responseTimeout", "10m")
	addHost(t, a, timed)

	var file struct {
		ProxyHosts []map[string]any `json:"proxyHosts"`
	}
	b, _ := os.ReadFile(filepath.Join(dir, "proxy-hosts.json"))
	if err := json.Unmarshal(b, &file); err != nil {
		t.Fatal(err)
	}

	if got, ok := file.ProxyHosts[0]["responseTimeout"]; ok {
		t.Errorf("host with no Response timeout saved %v", got)
	}
	if got := file.ProxyHosts[1]["responseTimeout"]; got != "10m" {
		t.Errorf("saved responseTimeout = %v, want 10m", got)
	}
}

func TestDataFileFromBeforeResponseTimeoutsGeneratesNoTransport(t *testing.T) {
	dir := t.TempDir()
	restoreHosts(t, dir, `{"id": 1, "enabled": true, "domains": ["a.example.com"], "forward": {"scheme": "http", "host": "10.0.0.1", "port": 80}, "exposure": "internal", "forceSsl": false, "certResolver": "le-dns", "middlewares": [], "skipCertCheck": false, "createdAt": "2026-01-01T00:00:00Z", "updatedAt": "2026-01-01T00:00:00Z"}`)
	a := newApp(t, internalOnly(dir))

	if got, ok := traefikConfig(t, a)["http"].(map[string]any)["serversTransports"]; ok {
		t.Errorf("serversTransports = %v, want none", got)
	}
}

func TestRefusedEditKeepsTheSavedResponseTimeout(t *testing.T) {
	a := newApp(t, internalOnly(t.TempDir()))
	form := hostForm("ai.example.com")
	form.Set("responseTimeout", "10m")
	addHost(t, a, form)
	_, updatedAt := editForm(t, a, 1)

	form.Set("responseTimeout", "-5s")
	form.Set("updatedAt", updatedAt)
	if rec := post(t, a, "/proxy-hosts/1", form); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("POST /proxy-hosts/1 = %d, want 422", rec.Code)
	}

	modal, _ := editForm(t, a, 1)
	assertContains(t, modal, `name="responseTimeout" value="10m"`)
}

func TestDisabledProxyHostsGenerateNoTransport(t *testing.T) {
	a := newApp(t, internalOnly(t.TempDir()))
	for domain, timeout := range map[string]string{"shared.example.com": "", "own.example.com": "10m"} {
		form := hostForm(domain)
		form.Set("scheme", "https")
		form.Set("skipCertCheck", "on")
		form.Set("responseTimeout", timeout)
		form.Del("enabled")
		addHost(t, a, form)
	}

	if got, ok := traefikConfig(t, a)["http"].(map[string]any)["serversTransports"]; ok {
		t.Errorf("serversTransports = %v, want none", got)
	}
}

func TestListShowsSSLSetupWithoutTpmsHelperNames(t *testing.T) {
	a := newApp(t, internalOnly(t.TempDir()))
	secure := hostForm("secure.example.com")
	secure.Set("scheme", "https")
	secure.Set("forceSsl", "on")
	secure.Set("skipCertCheck", "on")
	addHost(t, a, secure)
	legacy := hostForm("legacy.example.com")
	legacy.Set("certResolver", "le-dns")
	addHost(t, a, legacy)

	list := get(t, a, "/")

	assertContains(t, list, ">SSL</th>", "Force SSL", "le-http", "HTTP allowed", "le-dns", "Certificate check skipped")
	for _, page := range []string{list, get(t, a, "/proxy-hosts/1/edit")} {
		if strings.Contains(page, "tpm-") {
			t.Errorf("user-facing page shows a tpm helper name:\n%s", page)
		}
	}
}
