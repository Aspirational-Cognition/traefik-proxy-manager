package app_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Aspirational-Cognition/traefik-proxy-manager/internal/app"
)

// banners reads the global banners atop the list page.
func banners(t *testing.T, a http.Handler) string {
	t.Helper()
	page := get(t, a, "/")
	_, after, ok := strings.Cut(page, `<div id="banners">`)
	if !ok {
		t.Fatalf("no banners on the list page:\n%s", page)
	}
	b, _, _ := strings.Cut(after, `<div id="proxy-hosts"`)
	return b
}

// healthyStatusApp is statusApp with Traefik listing the install's entrypoints.
func healthyStatusApp(t *testing.T) (*app.App, *fakeTraefik, *clock) {
	t.Helper()
	a, traefik, clk := statusApp(t)
	traefik.setEntrypoints("web-int", "websecure-int")
	return a, traefik, clk
}

const neverLoaded = "Traefik hasn&#39;t loaded tpm&#39;s config yet; check providers.http"

func TestBannerUntilTraefikFirstLoadsTheConfig(t *testing.T) {
	a, _, _ := healthyStatusApp(t)
	assertContains(t, banners(t, a), neverLoaded)

	traefikConfig(t, a)

	if b := banners(t, a); strings.TrimSpace(b) != "</div>" {
		t.Errorf("want no banners once Traefik polls, got:\n%s", b)
	}
}

const stopped = "Traefik hasn&#39;t fetched tpm&#39;s config in the last minute"

func TestBannerWhenTraefikStopsFetchingForAMinute(t *testing.T) {
	a, _, clk := healthyStatusApp(t)
	traefikConfig(t, a)
	clk.advance(60 * time.Second)
	assertNotContains(t, banners(t, a), stopped)

	clk.advance(time.Second)
	assertContains(t, banners(t, a), stopped)
	assertNotContains(t, banners(t, a), neverLoaded)

	traefikConfig(t, a) // polls of an unchanged config count too
	assertNotContains(t, banners(t, a), stopped)
}

const rejected = "Traefik is fetching tpm&#39;s config but loaded none of its Proxy Hosts"

func TestBannerWhenTraefikRejectsTheWholeConfig(t *testing.T) {
	a, traefik, clk := healthyStatusApp(t)
	addHost(t, a, hostForm("nas.example.com"))
	traefik.set(loaded("whoami@docker")...)
	traefikConfig(t, a)
	clk.advance(5 * time.Second) // still Pending: Traefik may not have loaded it yet
	assertNotContains(t, banners(t, a), rejected)

	clk.advance(6 * time.Second)
	traefikConfig(t, a)
	assertContains(t, banners(t, a), rejected)

	traefik.set(loaded("whoami@docker", "host-1-internal-http@http", "host-1-internal-https@http")...)
	clk.advance(2 * time.Second)
	assertNotContains(t, banners(t, a), rejected)
}

func TestNoRejectedBannerWhenThereIsNothingToLoad(t *testing.T) {
	a, traefik, clk := healthyStatusApp(t)
	off := hostForm("off.example.com")
	off.Del("enabled")
	addHost(t, a, off)
	traefik.set(loaded("whoami@docker")...)

	servedLongAgo(t, a, clk)

	assertNotContains(t, banners(t, a), rejected)
}

func TestBannerNamesSettingsTraefikDoesntKnow(t *testing.T) {
	a, traefik, clk := accessApp(t)
	traefik.setEntrypoints("web-int", "websecure-int", "web")
	traefik.setMiddlewares(forwardAuth("authentik@docker"))
	traefikConfig(t, a)

	b := banners(t, a)

	assertContains(t, b,
		"Traefik has no entrypoint &#39;websecure&#39; (TPM_ENTRYPOINT_EXTERNAL_HTTPS)",
		"Traefik has no Access rule &#39;lan-only@file&#39; (TPM_DEFAULT_MIDDLEWARES_INTERNAL)",
		"Traefik has no Access rule &#39;crowdsec@docker&#39; (TPM_DEFAULT_MIDDLEWARES_EXTERNAL)")
	assertNotContains(t, b, "&#39;web-int&#39;", "&#39;web&#39;", "authentik@docker")

	traefik.setEntrypoints("web-int", "websecure-int", "web", "websecure")
	traefik.setMiddlewares(forwardAuth("authentik@docker"), forwardAuth("lan-only@file"), forwardAuth("crowdsec@docker"))
	clk.advance(2 * time.Second)
	assertNotContains(t, banners(t, a), "Traefik has no")
}

const unreachable = "tpm can&#39;t reach Traefik&#39;s API"

func TestBannerWhenTraefiksAPIIsUnreachable(t *testing.T) {
	a, traefik, clk := healthyStatusApp(t)
	traefikConfig(t, a)
	assertNotContains(t, banners(t, a), unreachable)

	traefik.mu.Lock()
	traefik.password = "s3cret" // tpm has no credentials, so every call fails
	traefik.mu.Unlock()
	clk.advance(2 * time.Second)
	assertContains(t, banners(t, a), unreachable)

	traefik.mu.Lock()
	traefik.password = ""
	traefik.mu.Unlock()
	clk.advance(2 * time.Second)
	assertNotContains(t, banners(t, a), unreachable)
}

func TestBannersRefreshWithTheStatusColumn(t *testing.T) {
	a, _, clk := healthyStatusApp(t)
	traefikConfig(t, a)
	clk.advance(61 * time.Second)

	fragment := get(t, a, "/proxy-hosts/status")

	assertContains(t, fragment, `<div id="banners" hx-swap-oob="true">`, stopped)
}
