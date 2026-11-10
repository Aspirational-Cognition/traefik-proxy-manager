package app_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Aspirational-Cognition/traefik-proxy-manager/internal/app"
)

// router is how Traefik reports a loaded router with a rule.
func router(name, rule string, entrypoints ...string) fakeRouter {
	return fakeRouter{Name: name, Status: "enabled", Rule: rule, EntryPoints: entrypoints}
}

const stillSave = "You can still save."

func TestModalWarnsWhenALabelRouteUsesTheDomain(t *testing.T) {
	a, traefik, _ := statusApp(t)
	traefik.set(router("whoami@docker", "Host(`nas.example.com`)", "websecure-int"))
	addHost(t, a, hostForm("nas.example.com"))

	modal, _ := editForm(t, a, 1)

	assertContains(t, modal, "<b>nas.example.com</b> is also used by <b>whoami</b> (Docker labels), on Internal", stillSave)
}

func TestEveryHostLiteralInALabelRouteRuleCounts(t *testing.T) {
	a, traefik, _ := statusApp(t)
	traefik.set(
		router("app@docker", "(Host(`One.example.com`) || Host(\"two.example.com\")) && !Host(`three.example.com`)", "websecure-int"),
		fakeRouter{Name: "broken@file", Status: "disabled", Rule: "Host(`four.example.com`) && PathPrefix(`/x`)", EntryPoints: []string{"web-int"}},
		fakeRouter{Name: "flaky@docker", Status: "warning", Rule: "Host(`five.example.com`)", EntryPoints: []string{"web-int"}},
	)
	addHost(t, a, hostForm("one.example.com two.example.com three.example.com four.example.com five.example.com"))

	modal, _ := editForm(t, a, 1)

	assertContains(t, modal,
		"<b>one.example.com</b> is also used by <b>app</b>",
		"<b>two.example.com</b> is also used by <b>app</b>",
		"<b>three.example.com</b> is also used by <b>app</b>",
		"<b>four.example.com</b> is also used by <b>broken</b> (Traefik&#39;s config files)",
		"<b>five.example.com</b> is also used by <b>flaky</b> (Docker labels)")
}

func TestOnlyExactHostLiteralsOfOtherProvidersCount(t *testing.T) {
	a, traefik, _ := statusApp(t)
	traefik.set(
		router("regexp@docker", "HostRegexp(`^nas\\.example\\.com$`)", "websecure-int"),
		router("wider@docker", "Host(`www.nas.example.com`)", "websecure-int"),
		router("host-2-internal-https@http", "Host(`nas.example.com`)", "websecure-int"),
		router("dashboard@internal", "Host(`nas.example.com`)", "websecure-int"),
	)
	addHost(t, a, hostForm("nas.example.com"))

	modal, _ := editForm(t, a, 1)

	assertNotContains(t, modal, "is also used by", stillSave)
}

func TestPerEntrypointCopiesOfALabelRouteAreOneRoute(t *testing.T) {
	a, traefik, _ := statusApp(t)
	traefik.set(
		router("web-int-whoami@docker", "Host(`nas.example.com`)", "web-int"),
		router("websecure-int-whoami@docker", "Host(`nas.example.com`)", "websecure-int"),
	)
	addHost(t, a, hostForm("nas.example.com"))

	modal, _ := editForm(t, a, 1)

	if n := strings.Count(modal, "<b>nas.example.com</b> is also used by <b>whoami</b> (Docker labels), on Internal"); n != 1 {
		t.Errorf("clash shown %d times, want once:\n%s", n, modal)
	}
}

// twoSidedClashApp is tpm on an Internal and External install talking to a
// fake Traefik.
func twoSidedClashApp(t *testing.T) (*app.App, *fakeTraefik) {
	t.Helper()
	traefik := newFakeTraefik(t)
	env := internalAndExternal(t.TempDir())
	env["TPM_TRAEFIK_API_URL"] = traefik.srv.URL
	a, err := app.New(func(k string) string { return env[k] }, (&clock{now: t0}).Now)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return a, traefik
}

func TestLabelRoutesClashOnlyOnASharedEntrypoint(t *testing.T) {
	a, traefik := twoSidedClashApp(t)
	traefik.set(
		router("public@docker", "Host(`ext.example.com`)", "websecure"),
		router("anywhere@docker", "Host(`any.example.com`)"), // no entryPoints: all of them
	)
	internal := hostForm("ext.example.com any.example.com")
	internal.Set("exposure", "internal")
	addHost(t, a, internal)
	both := hostForm("ext.example.com any.example.com")
	both.Set("exposure", "both")
	addHost(t, a, both)

	internalModal, _ := editForm(t, a, 1)
	bothModal, _ := editForm(t, a, 2)

	assertContains(t, internalModal, "<b>any.example.com</b> is also used by <b>anywhere</b> (Docker labels), on Internal")
	assertNotContains(t, internalModal, "<b>public</b>")
	assertContains(t, bothModal,
		"<b>ext.example.com</b> is also used by <b>public</b> (Docker labels), on External",
		"<b>any.example.com</b> is also used by <b>anywhere</b> (Docker labels), on Internal and External")
}

func TestEnabledProxyHostsSharingADomainAndSideClashEvenWithoutTraefik(t *testing.T) {
	a := newApp(t, internalAndExternal(t.TempDir())) // Traefik is unreachable
	for _, exposure := range []string{"internal", "both", "external"} {
		form := hostForm("nas.example.com")
		form.Set("exposure", exposure)
		addHost(t, a, form)
	}
	off := hostForm("NAS.example.com")
	off.Set("exposure", "internal")
	off.Del("enabled")
	addHost(t, a, off)

	first, _ := editForm(t, a, 1)
	second, _ := editForm(t, a, 2)

	clashWithHost := `<b>nas.example.com</b> is also used by Proxy Host <button type="button" hx-get="/proxy-hosts/%d/edit" hx-target="#modal" class="underline"><b>nas.example.com</b></button>, on %s`
	assertContains(t, first, fmt.Sprintf(clashWithHost, 2, "Internal"), stillSave)
	assertNotContains(t, first, `hx-get="/proxy-hosts/3/edit"`, `hx-get="/proxy-hosts/4/edit"`)
	assertContains(t, second, fmt.Sprintf(clashWithHost, 1, "Internal"), fmt.Sprintf(clashWithHost, 3, "External"))
	if disabled, _ := editForm(t, a, 4); strings.Contains(disabled, "is also used by") {
		t.Errorf("disabled host warns of a clash:\n%s", disabled)
	}
}

func TestModalRechecksClashesAsDomainsAndExposureChange(t *testing.T) {
	a, traefik := twoSidedClashApp(t)
	traefik.set(router("public@docker", "Host(`blog.example.com`)", "websecure"))
	nas := hostForm("nas.example.com")
	nas.Set("exposure", "internal")
	addHost(t, a, nas)

	newModal := get(t, a, "/proxy-hosts/new")
	edit, _ := editForm(t, a, 1)
	assertContains(t, newModal, `hx-get="/proxy-hosts/clashes"`, `hx-target="this"`, `hx-include="closest form"`, "from:[name=domains]", "from:[name=exposure]")
	assertContains(t, edit, `hx-get="/proxy-hosts/clashes?id=1"`)

	// What the modal sends as the user types.
	internal := get(t, a, "/proxy-hosts/clashes?domains=blog.example.com,+nas.example.com&exposure=internal&enabled=on")
	external := get(t, a, "/proxy-hosts/clashes?domains=blog.example.com,+nas.example.com&exposure=external&enabled=on")
	itself := get(t, a, "/proxy-hosts/clashes?id=1&domains=nas.example.com&exposure=internal&enabled=on")

	assertContains(t, internal, "<b>nas.example.com</b> is also used by Proxy Host", stillSave)
	assertNotContains(t, internal, "<b>public</b>")
	assertContains(t, external, "<b>blog.example.com</b> is also used by <b>public</b> (Docker labels), on External")
	assertNotContains(t, external, "Proxy Host")
	assertNotContains(t, itself, "is also used by", stillSave)
}

func TestSingleSideModalSendsItsExposureToTheRecheck(t *testing.T) {
	a, _, _ := statusApp(t)

	assertContains(t, get(t, a, "/proxy-hosts/new"), `<input type="hidden" name="exposure" value="internal">`)
}

func TestListMarksClashingHostsBesideTheirStatusAndSaveIsNeverBlocked(t *testing.T) {
	a, traefik, _ := statusApp(t)
	traefik.set(router("whoami@docker", "Host(`photos.example.com`)", "websecure-int"))
	addHost(t, a, hostForm("nas.example.com"))
	addHost(t, a, hostForm("files.example.com"))

	if rec := addHost(t, a, hostForm("nas.example.com, photos.example.com")); rec.Code != 200 {
		t.Fatalf("Save of a clashing host = %d, want 200: %s", rec.Code, rec.Body)
	}

	assertContains(t, statusOf(t, a, 1), "Pending", "⚠ clash")
	assertNotContains(t, statusOf(t, a, 2), "clash")
	assertContains(t, statusOf(t, a, 3), "Pending", "⚠ clash")
	refresh := get(t, a, "/proxy-hosts/status")
	assertContains(t, refresh, `<div id="status-3" hx-swap-oob="true">`)
	if n := strings.Count(refresh, "⚠ clash"); n != 2 {
		t.Errorf("refresh shows %d clash markers, want 2:\n%s", n, refresh)
	}
}
