package app_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"gitlab.example.com/home/devops/traefik-proxy-manager/internal/app"
)

// clock is a test clock that only moves when told to.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }

func (c *clock) advance(d time.Duration) { c.mu.Lock(); c.now = c.now.Add(d); c.mu.Unlock() }

// fakeRouter is one router as Traefik's API reports it.
type fakeRouter struct {
	Name   string   `json:"name"`
	Status string   `json:"status"`
	Error  []string `json:"error,omitempty"`
}

// fakeMiddleware is one Middleware as Traefik's API reports it, config included.
type fakeMiddleware map[string]any

// forwardAuth is how Traefik reports a forwardAuth Middleware: its type
// lowercased, its config under the camelCase key.
func forwardAuth(name string) fakeMiddleware {
	return fakeMiddleware{"name": name, "type": "forwardauth", "status": "enabled", "forwardAuth": map[string]any{"address": "http://auth:9000"}}
}

// fakeEntrypoint is one entrypoint as Traefik's API reports it.
type fakeEntrypoint struct {
	Name string `json:"name"`
}

// fakeCert is one certificate as Traefik's API reports it.
type fakeCert struct {
	SANs []string `json:"sans"`
}

// fakeTraefik is Traefik's API as each test sets it up.
type fakeTraefik struct {
	mu           sync.Mutex
	routers      []fakeRouter
	middlewares  []fakeMiddleware
	entrypoints  []fakeEntrypoint
	certificates []fakeCert // nil: Traefik older than 3.7, with no /api/certificates
	pageSize     int        // 0: everything on one page
	password     string     // if set, basic auth as admin is required
	requests     int
	srv          *httptest.Server
}

func newFakeTraefik(t *testing.T) *fakeTraefik {
	f := &fakeTraefik{}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeTraefik) set(routers ...fakeRouter) { f.mu.Lock(); f.routers = routers; f.mu.Unlock() }

func (f *fakeTraefik) setMiddlewares(ms ...fakeMiddleware) {
	f.mu.Lock()
	f.middlewares = ms
	f.mu.Unlock()
}

func (f *fakeTraefik) setEntrypoints(names ...string) {
	f.mu.Lock()
	f.entrypoints = nil
	for _, n := range names {
		f.entrypoints = append(f.entrypoints, fakeEntrypoint{n})
	}
	f.mu.Unlock()
}

func (f *fakeTraefik) setCertificates(cs ...fakeCert) {
	f.mu.Lock()
	f.certificates = append([]fakeCert{}, cs...)
	f.mu.Unlock()
}

func (f *fakeTraefik) requestCount() int { f.mu.Lock(); defer f.mu.Unlock(); return f.requests }

func (f *fakeTraefik) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch r.URL.Path {
	case "/api/http/routers":
		serveList(f, w, r, f.routers)
	case "/api/http/middlewares":
		serveList(f, w, r, f.middlewares)
	case "/api/entrypoints":
		serveList(f, w, r, f.entrypoints)
	case "/api/certificates":
		if f.certificates == nil {
			http.NotFound(w, r)
			return
		}
		serveList(f, w, r, f.certificates)
	default:
		http.NotFound(w, r)
	}
}

// serveList answers one page of an API list. The caller holds f.mu.
func serveList[T any](f *fakeTraefik, w http.ResponseWriter, r *http.Request, items []T) {
	f.requests++
	if user, pass, _ := r.BasicAuth(); f.password != "" && (user != "admin" || pass != f.password) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	size := f.pageSize
	if size == 0 {
		size = max(len(items), 1)
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	page = max(page, 1)
	start, end := min((page-1)*size, len(items)), min(page*size, len(items))
	next := 1 // Traefik's way of saying "last page"
	if end < len(items) {
		next = page + 1
	}
	w.Header().Set("X-Next-Page", strconv.Itoa(next))
	json.NewEncoder(w).Encode(append([]T{}, items[start:end]...))
}

// loaded is how Traefik reports routers it loaded fine.
func loaded(names ...string) []fakeRouter {
	var rs []fakeRouter
	for _, n := range names {
		rs = append(rs, fakeRouter{Name: n, Status: "enabled"})
	}
	return rs
}

// statusApp is tpm on an internal-only install talking to a fake Traefik,
// on a clock that only moves when the test says so.
func statusApp(t *testing.T) (*app.App, *fakeTraefik, *clock) {
	t.Helper()
	return statusAppAt(t, newFakeTraefik(t), "", t.TempDir())
}

// statusAppAt is statusApp with Traefik's API URL carrying userinfo, e.g.
// "admin:s3cret@", and the data in dir.
func statusAppAt(t *testing.T, traefik *fakeTraefik, userinfo, dir string) (*app.App, *fakeTraefik, *clock) {
	t.Helper()
	c := &clock{now: t0}
	env := internalOnly(dir)
	env["TPM_TRAEFIK_API_URL"] = strings.Replace(traefik.srv.URL, "://", "://"+userinfo, 1)
	a, err := app.New(func(k string) string { return env[k] }, c.Now)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return a, traefik, c
}

func TestSavedHostShowsPendingUntilTraefikLoadsIt(t *testing.T) {
	a, _, _ := statusApp(t)

	saved := addHost(t, a, hostForm("nas.example.com")).Body.String()

	assertContains(t, saved, ">Status</th>", `id="status-1"`, "Pending")
}

// statusOf reads host id's Status cell from the list page.
func statusOf(t *testing.T, a http.Handler, id int) string {
	t.Helper()
	page := get(t, a, "/")
	m := regexp.MustCompile(`(?s)<div id="status-` + strconv.Itoa(id) + `">(.*?)</div>\s*</td>`).FindStringSubmatch(page)
	if m == nil {
		t.Fatalf("no Status cell for host %d in:\n%s", id, page)
	}
	return m[1]
}

func TestLoadedHostGoesOnlineAboutTenSecondsAfterTraefikFetchedIt(t *testing.T) {
	a, traefik, clk := statusApp(t)
	addHost(t, a, hostForm("nas.example.com"))
	traefik.set(loaded("host-1-internal-http@http", "host-1-internal-https@http")...)
	clk.advance(30 * time.Second) // time passing alone doesn't make it live
	assertContains(t, statusOf(t, a, 1), "Pending")

	traefikConfig(t, a) // Traefik fetches the config
	clk.advance(9 * time.Second)
	assertContains(t, statusOf(t, a, 1), "Pending")

	clk.advance(2 * time.Second)
	assertContains(t, statusOf(t, a, 1), "Online")
}

// servedLongAgo has Traefik fetch the config, then lets Pending run out.
func servedLongAgo(t *testing.T, a http.Handler, clk *clock) {
	t.Helper()
	traefikConfig(t, a)
	clk.advance(11 * time.Second)
}

func TestHostTraefikNeverLoadedShowsError(t *testing.T) {
	a, traefik, clk := statusApp(t)
	addHost(t, a, hostForm("one.example.com"))
	addHost(t, a, hostForm("two.example.com"))
	// Traefik loaded only host 2's http router.
	traefik.set(loaded("host-2-internal-http@http", "whoami@docker")...)

	servedLongAgo(t, a, clk)

	for _, id := range []int{1, 2} {
		status := statusOf(t, a, id)
		assertContains(t, status, "Error", "Traefik didn&#39;t load this host. Check Traefik&#39;s logs.")
		if strings.Count(status, "Traefik didn") != 1 {
			t.Errorf("host %d repeats its message:\n%s", id, status)
		}
	}
}

func TestRouterTraefikDisabledShowsErrorInPlainWords(t *testing.T) {
	a, traefik, clk := statusApp(t)
	addHost(t, a, hostForm("nas.example.com"))
	traefik.set(
		fakeRouter{Name: "host-1-internal-http@http", Status: "disabled", Error: []string{`entryPoint "web-int" doesn't exist`, "no valid entryPoint for this router"}},
		fakeRouter{Name: "host-1-internal-https@http", Status: "disabled", Error: []string{`entryPoint "websecure-int" doesn't exist`, "no valid entryPoint for this router"}},
	)

	servedLongAgo(t, a, clk)
	status := statusOf(t, a, 1)

	assertContains(t, status, "Error",
		"Traefik has no entrypoint &#39;web-int&#39;. Check tpm&#39;s settings.",
		"Traefik has no entrypoint &#39;websecure-int&#39;. Check tpm&#39;s settings.",
		"Traefik said:", "no valid entryPoint for this router")
	if n := strings.Count(status, "no valid entryPoint"); n != 1 {
		t.Errorf("message shown %d times, want once:\n%s", n, status)
	}
	if strings.Contains(status, "host-1-") {
		t.Errorf("Status shows router names:\n%s", status)
	}
}

func TestRouterTraefikWarnsAboutShowsWarning(t *testing.T) {
	a, traefik, clk := statusApp(t)
	addHost(t, a, hostForm("nas.example.com"))
	traefik.set(
		fakeRouter{Name: "host-1-internal-http@http", Status: "enabled"},
		fakeRouter{Name: "host-1-internal-https@http", Status: "warning", Error: []string{"TLS options conflict"}},
	)

	servedLongAgo(t, a, clk)

	assertContains(t, statusOf(t, a, 1), "bg-warn", "Warning", `<div class="text-warn">Traefik said: TLS options conflict</div>`)
}

// restoredStatusApp is statusApp on an internal-only install whose data
// file holds one restored host, given as its JSON fields after the
// Forward Target.
func restoredStatusApp(t *testing.T, fields string) (*app.App, *fakeTraefik, *clock) {
	t.Helper()
	dir := t.TempDir()
	restoreHosts(t, dir, `{"id": 1, "enabled": true, "domains": ["photos.example.com"], "forward": {"scheme": "http", "host": "10.0.0.1", "port": 80}, `+fields+`, "middlewares": [], "updatedAt": "2026-01-01T00:00:00Z"}`)
	return statusAppAt(t, newFakeTraefik(t), "", dir)
}

func TestHostExposedOnASideTheInstallNoLongerConfiguresShowsWarning(t *testing.T) {
	a, traefik, clk := restoredStatusApp(t, `"exposure": "both", "certResolver": "le-dns"`)
	traefik.set(loaded("host-1-internal-http@http", "host-1-internal-https@http")...)

	servedLongAgo(t, a, clk)

	assertContains(t, statusOf(t, a, 1), "Warning", "External isn&#39;t configured")
}

func TestHostWithNoConfiguredSideLeftShowsErrorAndGetsNoRouters(t *testing.T) {
	a, _, clk := restoredStatusApp(t, `"exposure": "external", "certResolver": "le-dns"`)

	servedLongAgo(t, a, clk)
	status := statusOf(t, a, 1)

	assertContains(t, status, "Error", "External isn&#39;t configured")
	if strings.Contains(status, "Traefik didn") {
		t.Errorf("Status blames Traefik for a host tpm gave it no routers for:\n%s", status)
	}
	if got := routerNames(t, a); len(got) != 0 {
		t.Errorf("routers = %v, want none", got)
	}
}

func TestUnconfiguredResolverShowsWarningAndTpmKeepsServingIt(t *testing.T) {
	a, traefik, clk := restoredStatusApp(t, `"exposure": "internal", "certResolver": "old-le"`)
	traefik.set(loaded("host-1-internal-http@http", "host-1-internal-https@http")...)

	servedLongAgo(t, a, clk)

	assertContains(t, statusOf(t, a, 1), "Warning", "Certificate Resolver &#39;old-le&#39; isn&#39;t configured")
	https := traefikConfig(t, a)["http"].(map[string]any)["routers"].(map[string]any)["host-1-internal-https"]
	if !strings.Contains(fmt.Sprint(https), "certResolver:old-le") {
		t.Errorf("https router = %v, want certResolver old-le", https)
	}
}

func TestDomainWithoutACertificateShowsWarning(t *testing.T) {
	a, traefik, clk := statusApp(t)
	addHost(t, a, hostForm("nas.example.com, files.home.example.com, deep.a.example.com"))
	traefik.set(loaded("host-1-internal-http@http", "host-1-internal-https@http")...)
	traefik.setCertificates(
		fakeCert{SANs: []string{"NAS.example.com"}},
		fakeCert{SANs: []string{"*.home.example.com", "*.example.com"}}, // a wildcard covers one label
	)

	servedLongAgo(t, a, clk)
	status := statusOf(t, a, 1)

	assertContains(t, status, "Warning", "No certificate yet for deep.a.example.com")
	for _, covered := range []string{"for nas.example.com", "for files.home.example.com"} {
		if strings.Contains(status, covered) {
			t.Errorf("Status flags a covered domain (%s):\n%s", covered, status)
		}
	}
}

func TestHostWhoseDomainsAllHaveCertificatesIsOnline(t *testing.T) {
	a, traefik, clk := statusApp(t)
	addHost(t, a, hostForm("nas.example.com"))
	traefik.set(loaded("host-1-internal-http@http", "host-1-internal-https@http")...)
	traefik.setCertificates(fakeCert{SANs: []string{"*.example.com"}})

	servedLongAgo(t, a, clk)

	assertContains(t, statusOf(t, a, 1), "Online")
}

func TestTraefikWithoutTheCertificatesAPISkipsTheCheck(t *testing.T) {
	a, traefik, clk := statusApp(t) // the fake answers /api/certificates with 404
	addHost(t, a, hostForm("nas.example.com"))
	traefik.set(loaded("host-1-internal-http@http", "host-1-internal-https@http")...)

	servedLongAgo(t, a, clk)

	assertContains(t, statusOf(t, a, 1), "Online")
}

func TestErrorOutranksWarningAndListsEveryMessageOnce(t *testing.T) {
	a, traefik, clk := restoredStatusApp(t, `"exposure": "internal", "certResolver": "old-le"`)
	traefik.set(
		fakeRouter{Name: "host-1-internal-http@http", Status: "warning", Error: []string{"TLS options conflict"}},
		fakeRouter{Name: "host-1-internal-https@http", Status: "disabled", Error: []string{"TLS options conflict"}},
	)
	traefik.setCertificates()

	servedLongAgo(t, a, clk)
	status := statusOf(t, a, 1)

	assertContains(t, status, "Error", "TLS options conflict",
		"Certificate Resolver &#39;old-le&#39; isn&#39;t configured", "No certificate yet for photos.example.com")
	if strings.Contains(status, "Warning") || strings.Count(status, "TLS options conflict") != 1 {
		t.Errorf("want Error with each message once:\n%s", status)
	}
}

func TestDisabledRouterWithoutAReasonIsStillAnError(t *testing.T) {
	a, traefik, clk := statusApp(t)
	addHost(t, a, hostForm("nas.example.com"))
	traefik.set(fakeRouter{Name: "host-1-internal-http@http", Status: "enabled"}, fakeRouter{Name: "host-1-internal-https@http", Status: "disabled"})

	servedLongAgo(t, a, clk)

	assertContains(t, statusOf(t, a, 1), "Error")
}

func TestUnreachableTraefikShowsUnknownButSaveStillWorks(t *testing.T) {
	a, traefik, clk := statusApp(t)
	addHost(t, a, hostForm("on.example.com"))
	off := hostForm("off.example.com")
	off.Del("enabled")
	addHost(t, a, off)
	servedLongAgo(t, a, clk)
	traefik.srv.Close()

	if rec := addHost(t, a, hostForm("new.example.com")); rec.Code != 200 {
		t.Fatalf("Save with Traefik unreachable = %d: %s", rec.Code, rec.Body)
	}

	for id, want := range map[int]string{1: "Unknown", 2: "Disabled", 3: "Unknown"} {
		assertContains(t, statusOf(t, a, id), want)
	}
}

func TestStatusReadsEveryPageOfRoutersWithBasicAuth(t *testing.T) {
	traefik := newFakeTraefik(t)
	traefik.pageSize = 2
	traefik.password = "s3cret"
	a, _, clk := statusAppAt(t, traefik, "admin:s3cret@", t.TempDir())
	for _, d := range []string{"one.example.com", "two.example.com", "three.example.com"} {
		addHost(t, a, hostForm(d))
	}
	traefik.set(append(loaded("whoami@docker"), loaded(
		"host-1-internal-http@http", "host-1-internal-https@http",
		"host-2-internal-http@http", "host-2-internal-https@http",
		"host-3-internal-http@http", "host-3-internal-https@http")...)...)

	servedLongAgo(t, a, clk)

	for id := 1; id <= 3; id++ {
		assertContains(t, statusOf(t, a, id), "Online")
	}
}

func TestEveryViewerSharesOneBatchOfTraefikCalls(t *testing.T) {
	a, traefik, clk := statusApp(t)
	for _, d := range []string{"one.example.com", "two.example.com", "three.example.com"} {
		addHost(t, a, hostForm(d))
	}
	clk.advance(time.Minute)
	before := traefik.requestCount()

	for range 3 { // three viewers at once
		get(t, a, "/")
	}

	// One batch is one call each for routers, Middlewares and entrypoints.
	if n := traefik.requestCount() - before; n != 3 {
		t.Fatalf("Traefik API calls for three viewers of three hosts = %d, want 3", n)
	}
	clk.advance(5 * time.Second)
	get(t, a, "/")
	if n := traefik.requestCount() - before; n != 6 {
		t.Fatalf("Traefik API calls after the cache expired = %d, want 6", n)
	}
}

func TestStatusColumnRefreshesItselfEveryFiveSeconds(t *testing.T) {
	a, traefik, clk := statusApp(t)
	addHost(t, a, hostForm("nas.example.com"))
	assertContains(t, get(t, a, "/"), `hx-get="/proxy-hosts/status"`, `hx-trigger="every 5s"`)
	traefik.set(loaded("host-1-internal-http@http", "host-1-internal-https@http")...)

	servedLongAgo(t, a, clk)
	fragment := get(t, a, "/proxy-hosts/status")

	assertContains(t, fragment, `<div id="status-1" hx-swap-oob="true">`, "Online")
	if strings.Contains(fragment, "nas.example.com") {
		t.Errorf("status refresh re-sends the whole list:\n%s", fragment)
	}
}

func TestEditModalBannersTraefiksProblemsWithTheHost(t *testing.T) {
	a, traefik, clk := statusApp(t)
	addHost(t, a, hostForm("broken.example.com"))
	addHost(t, a, hostForm("fine.example.com"))
	traefik.set(
		fakeRouter{Name: "host-1-internal-http@http", Status: "disabled", Error: []string{`entryPoint "web-int" doesn't exist`}},
		fakeRouter{Name: "host-1-internal-https@http", Status: "enabled"},
		fakeRouter{Name: "host-2-internal-http@http", Status: "enabled"},
		fakeRouter{Name: "host-2-internal-https@http", Status: "enabled"},
	)
	servedLongAgo(t, a, clk)

	broken, _ := editForm(t, a, 1)
	fine, _ := editForm(t, a, 2)

	assertContains(t, broken, "Traefik reported a problem:", "Traefik has no entrypoint &#39;web-int&#39;. Check tpm&#39;s settings.")
	if strings.Contains(fine, "Traefik reported a problem") {
		t.Errorf("healthy host's modal shows a problem banner:\n%s", fine)
	}
}

func TestEditedHostIsPendingAgainUntilTraefikFetchesTheChange(t *testing.T) {
	a, traefik, clk := statusApp(t)
	addHost(t, a, hostForm("nas.example.com"))
	traefik.set(loaded("host-1-internal-http@http", "host-1-internal-https@http")...)
	servedLongAgo(t, a, clk)
	assertContains(t, statusOf(t, a, 1), "Online")

	_, updatedAt := editForm(t, a, 1)
	form := hostForm("files.example.com")
	form.Set("updatedAt", updatedAt)
	saved := post(t, a, "/proxy-hosts/1", form).Body.String()

	assertContains(t, saved, "Pending")
	clk.advance(time.Minute)
	assertContains(t, statusOf(t, a, 1), "Pending")
	servedLongAgo(t, a, clk)
	assertContains(t, statusOf(t, a, 1), "Online")
}

func TestHostSavedAfterTheClockWentBackIsStillPending(t *testing.T) {
	dir := t.TempDir()
	// Restored from a machine whose clock ran ahead.
	restoreHosts(t, dir, `{"id": 1, "enabled": true, "domains": ["old.example.com"], "forward": {"scheme": "http", "host": "10.0.0.1", "port": 80}, "exposure": "internal", "certResolver": "le-dns", "middlewares": [], "updatedAt": "2030-01-01T00:00:00Z"}`)
	a, traefik, clk := statusAppAt(t, newFakeTraefik(t), "", dir)
	traefikConfig(t, a)

	addHost(t, a, hostForm("new.example.com"))
	traefik.set(loaded("host-10-internal-http@http", "host-10-internal-https@http")...)
	clk.advance(time.Minute)

	assertContains(t, statusOf(t, a, 10), "Pending")
}
