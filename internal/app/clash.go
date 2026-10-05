package app

import (
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// labelRoute is a route Traefik serves from outside tpm, e.g. Docker labels.
type labelRoute struct {
	name, provider string   // e.g. whoami and docker, for whoami@docker
	entrypoints    []string // none means every entrypoint
	domains        []string // its rule's Host literals, lowercased
}

// domainClash is a domain on a Proxy Host that another enabled Proxy Host,
// or a Label Route, also answers on a shared side.
type domainClash struct {
	Domain   string
	By       string // the Label Route's name, or the other Proxy Host's first domain
	Provider string // the Label Route's provider in plain words
	HostID   int    // the other Proxy Host; 0 for a Label Route
	Sides    string // where both answer, e.g. Internal or Internal and External
}

// hostMatcher finds each Host(...) in a rule, whatever surrounds it, and
// quotedDomain each domain in one; \b keeps out HostRegexp, HostSNI and
// the like.
var (
	hostMatcher  = regexp.MustCompile(`\bHost\(([^)]*)\)`)
	quotedDomain = regexp.MustCompile("[`\"]([^`\"]*)[`\"]")
)

// labelRoutes are the routers Traefik lists that aren't tpm's (@http) or
// Traefik's own (@internal). Per-entrypoint copies Traefik renames
// <ep>-<name>@provider collapse back into one.
func labelRoutes(routers []apiRouter) []labelRoute {
	var routes []labelRoute
	for _, r := range routers {
		name, provider, _ := strings.Cut(r.Name, "@")
		if provider == "http" || provider == "internal" {
			continue
		}
		for _, ep := range r.EntryPoints {
			name = strings.TrimPrefix(name, ep+"-")
		}
		var domains []string
		for _, m := range hostMatcher.FindAllStringSubmatch(r.Rule, -1) {
			for _, d := range quotedDomain.FindAllStringSubmatch(m[1], -1) {
				domains = append(domains, strings.ToLower(d[1]))
			}
		}
		i := slices.IndexFunc(routes, func(l labelRoute) bool { return l.name == name && l.provider == provider })
		if i < 0 {
			routes = append(routes, labelRoute{name: name, provider: provider, entrypoints: r.EntryPoints, domains: domains})
			continue
		}
		if len(routes[i].entrypoints) > 0 && len(r.EntryPoints) > 0 {
			routes[i].entrypoints = append(routes[i].entrypoints, r.EntryPoints...)
		} else {
			routes[i].entrypoints = nil // on every entrypoint
		}
		routes[i].domains = append(routes[i].domains, domains...)
	}
	return routes
}

// providerWords puts a Traefik provider in plain words.
func providerWords(provider string) string {
	switch provider {
	case "docker":
		return "Docker labels"
	case "file":
		return "Traefik's config files"
	}
	return provider
}

// clashes are h's Domain Clashes with the other Proxy Hosts and the Label
// Routes. A disabled host has none, and disabled hosts clash with nothing.
func (w *web) clashes(h proxyHost, hosts []proxyHost, routes []labelRoute) []domainClash {
	if !h.Enabled {
		return nil
	}
	sides := w.settings.sidesOf(h)
	var cs []domainClash
	for _, d := range h.Domains {
		for _, o := range hosts {
			if o.ID == h.ID || !o.Enabled || !slices.Contains(o.Domains, d) {
				continue
			}
			if shared := sharedSides(sides, func(sd side) bool { return slices.Contains(w.settings.sidesOf(o), sd) }); shared != "" {
				cs = append(cs, domainClash{Domain: d, By: o.Domains[0], HostID: o.ID, Sides: shared})
			}
		}
		for _, l := range routes {
			if !slices.Contains(l.domains, d) {
				continue
			}
			on := func(sd side) bool {
				return len(l.entrypoints) == 0 || slices.Contains(l.entrypoints, sd.http) || slices.Contains(l.entrypoints, sd.https)
			}
			if shared := sharedSides(sides, on); shared != "" {
				cs = append(cs, domainClash{Domain: d, By: l.name, Provider: providerWords(l.provider), Sides: shared})
			}
		}
	}
	return cs
}

// sharedSides names the sides for which shared is true, e.g. "Internal and
// External", or "" when there are none.
func sharedSides(sides []side, shared func(side) bool) string {
	var names []string
	for _, sd := range sides {
		if shared(sd) {
			names = append(names, sideLabel(sd.name))
		}
	}
	return strings.Join(names, " and ")
}

// sideLabel is a side's name as users see it, e.g. Internal.
func sideLabel(name string) string {
	i := slices.IndexFunc(exposures, func(e exposureOption) bool { return e.Value == name })
	return exposures[i].Label
}

// formClashes are the Domain Clashes of the host as typed in the modal.
func (w *web) formClashes(f formView) []domainClash {
	live, _ := w.traefik.state() // without Traefik, Proxy Hosts still clash
	h := proxyHost{ID: f.ID, Enabled: f.Enabled, Domains: splitDomains(f.Domains), Exposure: f.Exposure}
	return w.clashes(h, w.store.all(), live.labelRoutes)
}

// clashCheck answers the modal's re-check as the user edits: the Domain
// Clash banner for the host as typed. ?id= is the host being edited.
func (w *web) clashCheck(rw http.ResponseWriter, r *http.Request) {
	f := readForm(r)
	f.ID, _ = strconv.Atoi(r.FormValue("id"))
	render(rw, http.StatusOK, "clash-banner", w.formClashes(f))
}
