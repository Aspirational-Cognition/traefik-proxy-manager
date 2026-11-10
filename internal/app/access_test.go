package app_test

import (
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Aspirational-Cognition/traefik-proxy-manager/internal/app"
)

// withDefaults is a two-sided install whose admin set default Access rules.
func withDefaults(dataDir string) map[string]string {
	env := internalAndExternal(dataDir)
	env["TPM_DEFAULT_MIDDLEWARES_INTERNAL"] = "lan-only@file"
	env["TPM_DEFAULT_MIDDLEWARES_EXTERNAL"] = " authentik@docker, crowdsec@docker "
	return env
}

func TestEveryForwardingRouterGetsTheAccessListDefaultsFirst(t *testing.T) {
	a := newApp(t, withDefaults(t.TempDir()))
	form := hostForm("nas.example.com")
	form.Set("exposure", "both")
	form["access"] = []string{"ratelimit@file", "crowdsec@docker", "geoblock@file", "authentik@docker"}
	addHost(t, a, form)
	forced := hostForm("forced.example.com")
	forced.Set("exposure", "external")
	forced.Set("forceSsl", "on")
	forced["access"] = []string{"authentik@docker"}
	addHost(t, a, forced)

	routers := traefikConfig(t, a)["http"].(map[string]any)["routers"].(map[string]any)

	want := []any{"authentik@docker", "crowdsec@docker", "ratelimit@file", "geoblock@file"}
	for _, name := range []string{"host-1-internal-http", "host-1-internal-https", "host-1-external-http", "host-1-external-https"} {
		if got := routers[name].(map[string]any)["middlewares"]; !reflect.DeepEqual(got, want) {
			t.Errorf("router %s middlewares = %v, want %v", name, got, want)
		}
	}
	for name, want := range map[string][]any{"host-2-external-http": {"tpm-force-ssl"}, "host-2-external-https": {"authentik@docker"}} {
		if got := routers[name].(map[string]any)["middlewares"]; !reflect.DeepEqual(got, want) {
			t.Errorf("router %s middlewares = %v, want %v", name, got, want)
		}
	}
}

func TestDefaultAccessRulesMustBeFullyQualified(t *testing.T) {
	env := withDefaults(t.TempDir())
	env["TPM_DEFAULT_MIDDLEWARES_EXTERNAL"] = "authentik@docker,crowdsec"

	_, err := app.New(func(k string) string { return env[k] }, time.Now)

	if err == nil || !strings.Contains(err.Error(), `TPM_DEFAULT_MIDDLEWARES_EXTERNAL: "crowdsec" isn't fully qualified, e.g. authentik@docker`) {
		t.Fatalf("New = %v, want a fully-qualified error", err)
	}
}

// accessApp is tpm on a two-sided install with default Access rules,
// talking to a fake Traefik that lists Middlewares.
func accessApp(t *testing.T) (*app.App, *fakeTraefik, *clock) {
	t.Helper()
	traefik := newFakeTraefik(t)
	traefik.setMiddlewares(
		forwardAuth("authentik@docker"),
		fakeMiddleware{"name": "lan-only@file", "type": "ipallowlist", "ipAllowList": map[string]any{}},
		fakeMiddleware{"name": "crowdsec@docker", "type": "crowdsec-bouncer", "plugin": map[string]any{"crowdsec-bouncer": map[string]any{}}},
		fakeMiddleware{"name": "tpm-force-ssl@http", "type": "redirectscheme", "redirectScheme": map[string]any{}},
		fakeMiddleware{"name": "dashboard_redirect@internal", "type": "redirectregex", "redirectRegex": map[string]any{}},
	)
	c := &clock{now: t0}
	env := withDefaults(t.TempDir())
	env["TPM_TRAEFIK_API_URL"] = traefik.srv.URL
	a, err := app.New(func(k string) string { return env[k] }, c.Now)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return a, traefik, c
}

// accessItem finds an Access rule's row in the modal's Access section.
func accessItem(t *testing.T, modal, value string) string {
	t.Helper()
	m := regexp.MustCompile(`(?s)<label[^>]*>\s*<input type="checkbox" name="access" value="` + regexp.QuoteMeta(value) + `".*?</label>`).FindString(modal)
	if m == "" {
		t.Fatalf("no Access rule %s in:\n%s", value, modal)
	}
	return m
}

func TestAccessSectionListsTraefiksMiddlewaresByNameAndType(t *testing.T) {
	a, _, _ := accessApp(t)

	modal := get(t, a, "/proxy-hosts/new")

	assertContains(t, modal, ">Access</h3>")
	assertContains(t, accessItem(t, modal, "authentik@docker"), "authentik · forwardAuth")
	assertContains(t, accessItem(t, modal, "lan-only@file"), "lan-only · ipAllowList")
	assertContains(t, accessItem(t, modal, "crowdsec@docker"), "crowdsec · crowdsec-bouncer")
	if strings.Contains(modal, "tpm-force-ssl") {
		t.Errorf("modal offers tpm's own helper as an Access rule:\n%s", modal)
	}
	if strings.Contains(modal, "dashboard_redirect") {
		t.Errorf("modal offers Traefik's own dashboard Middleware as an Access rule:\n%s", modal)
	}
}

// assertTicked checks an Access rule's tick and its "default" tag.
func assertTicked(t *testing.T, modal, value string, ticked, isDefault bool) {
	t.Helper()
	item := accessItem(t, modal, value)
	if got := strings.Contains(item, " checked"); got != ticked {
		t.Errorf("%s ticked = %v, want %v:\n%s", value, got, ticked, item)
	}
	if got := strings.Contains(item, "<span data-default class"); got != isDefault {
		t.Errorf("%s tagged default = %v, want %v:\n%s", value, got, isDefault, item)
	}
}

func TestNewHostHasItsExposuresDefaultsTickedAndTagged(t *testing.T) {
	a, _, _ := accessApp(t)

	modal := get(t, a, "/proxy-hosts/new")

	assertContains(t, modal, ">default</span>")
	assertTicked(t, modal, "lan-only@file", true, true)
	assertTicked(t, modal, "authentik@docker", false, false)
	assertTicked(t, modal, "crowdsec@docker", false, false)
}

func TestBothHostGetsExternalsDefaults(t *testing.T) {
	a, _, _ := accessApp(t)
	form := hostForm("nas.example.com")
	form.Set("exposure", "both")
	form["access"] = []string{"authentik@docker", "crowdsec@docker"}
	addHost(t, a, form)

	modal, _ := editForm(t, a, 1)

	assertTicked(t, modal, "authentik@docker", true, true)
	assertTicked(t, modal, "crowdsec@docker", true, true)
	assertTicked(t, modal, "lan-only@file", false, false)
	// What the modal's script swaps in when Exposure changes.
	assertContains(t, modal, `data-exposure="both"`, `&#34;both&#34;:[&#34;authentik@docker&#34;,&#34;crowdsec@docker&#34;]`)
}

func TestMissingAccessRuleFailsClosedUntilTheUserUnticksIt(t *testing.T) {
	a, traefik, clk := accessApp(t)
	form := hostForm("nas.example.com")
	form.Set("exposure", "external")
	form["access"] = []string{"authentik@docker"}
	addHost(t, a, form)
	// Authentik's container is gone, so Traefik turns the host's routers off.
	traefik.setMiddlewares()
	gone := []string{`middleware "authentik@docker" does not exist`}
	traefik.set(
		fakeRouter{Name: "host-1-external-http@http", Status: "disabled", Error: gone},
		fakeRouter{Name: "host-1-external-https@http", Status: "disabled", Error: gone},
	)
	servedLongAgo(t, a, clk)

	for name, r := range traefikConfig(t, a)["http"].(map[string]any)["routers"].(map[string]any) {
		if mw := r.(map[string]any)["middlewares"]; !reflect.DeepEqual(mw, []any{"authentik@docker"}) {
			t.Errorf("router %s middlewares = %v, want the missing Access rule kept", name, mw)
		}
	}
	status := statusOf(t, a, 1)
	assertContains(t, status, "Error", "Access rule &#39;authentik&#39; no longer exists in Traefik")
	if strings.Count(status, "no longer exists") != 1 {
		t.Errorf("message repeated:\n%s", status)
	}
	modal, updatedAt := editForm(t, a, 1)
	assertTicked(t, modal, "authentik@docker", true, true)
	assertContains(t, accessItem(t, modal, "authentik@docker"), ">missing</span>")

	untick := hostForm("nas.example.com")
	untick.Set("exposure", "external")
	untick.Set("updatedAt", updatedAt)
	post(t, a, "/proxy-hosts/1", untick)
	traefik.set(loaded("host-1-external-http@http", "host-1-external-https@http")...)
	servedLongAgo(t, a, clk)

	assertContains(t, statusOf(t, a, 1), "Online")
	modal, _ = editForm(t, a, 1)
	assertTicked(t, modal, "authentik@docker", false, true)
}

func TestUnreachableTraefikLimitsThePickerToTheStoredChoicesAndSaveStillWorks(t *testing.T) {
	a := newApp(t, withDefaults(t.TempDir())) // Traefik is unreachable
	form := hostForm("nas.example.com")
	form.Set("exposure", "external")
	form["access"] = []string{"ratelimit@file", "authentik@docker"}
	if rec := addHost(t, a, form); rec.Code != 200 {
		t.Fatalf("Save with Traefik unreachable = %d: %s", rec.Code, rec.Body)
	}

	modal, _ := editForm(t, a, 1)

	assertContains(t, modal, "Can't reach Traefik to list Access rules")
	assertTicked(t, modal, "authentik@docker", true, true)
	assertTicked(t, modal, "ratelimit@file", true, false)
	if strings.Contains(modal, ">missing</span>") {
		t.Errorf("Access rules flagged missing while Traefik can't be asked:\n%s", modal)
	}
	if strings.Index(modal, `value="authentik@docker"`) > strings.Index(modal, `value="ratelimit@file"`) {
		t.Errorf("Access rules not shown in the order they apply:\n%s", modal)
	}
	assertContains(t, get(t, a, "/proxy-hosts/new"), "Can't reach Traefik to list Access rules")
}

func TestListShowsEachHostsAccessRulesByName(t *testing.T) {
	a, _, _ := accessApp(t)
	form := hostForm("nas.example.com")
	form.Set("exposure", "external")
	form["access"] = []string{"authentik@docker", "crowdsec@docker"}
	addHost(t, a, form)

	list := get(t, a, "/")

	assertContains(t, list, ">Access</th>", ">authentik</span>", ">crowdsec</span>")
	for _, page := range []string{list, get(t, a, "/proxy-hosts/new"), get(t, a, "/proxy-hosts/1/edit")} {
		if strings.Contains(strings.ToLower(page), "middleware") || strings.Contains(page, "@docker<") {
			t.Errorf("user-facing page says Middleware or shows a provider:\n%s", page)
		}
	}
}
