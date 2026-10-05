package app

import (
	"fmt"
	"strings"
)

// Traefik dynamic config, hand-written: only the fields tpm emits.
type snapshot struct {
	HTTP httpConfig `json:"http"`
}

type httpConfig struct {
	Routers           map[string]router           `json:"routers"`
	Services          map[string]service          `json:"services"`
	Middlewares       map[string]middleware       `json:"middlewares,omitempty"`
	ServersTransports map[string]serversTransport `json:"serversTransports,omitempty"`
}

type router struct {
	EntryPoints []string   `json:"entryPoints"`
	Rule        string     `json:"rule"`
	Middlewares []string   `json:"middlewares,omitempty"`
	Service     string     `json:"service"`
	TLS         *routerTLS `json:"tls,omitempty"`
}

type routerTLS struct {
	CertResolver string `json:"certResolver"`
}

type middleware struct {
	RedirectScheme redirectScheme `json:"redirectScheme"`
}

type redirectScheme struct {
	Scheme    string `json:"scheme"`
	Permanent bool   `json:"permanent"`
}

// forceSSLMiddleware is the shared middleware every Force SSL host's http routers use.
const forceSSLMiddleware = "tpm-force-ssl"

type service struct {
	LoadBalancer loadBalancer `json:"loadBalancer"`
}

type loadBalancer struct {
	Servers          []server `json:"servers"`
	ServersTransport string   `json:"serversTransport,omitempty"`
}

type serversTransport struct {
	InsecureSkipVerify bool `json:"insecureSkipVerify"`
}

// skipCertCheckTransport is the shared transport for Forward Targets whose
// certificate isn't checked.
const skipCertCheckTransport = "tpm-skip-cert-check"

type server struct {
	URL string `json:"url"`
}

// sidesOf is the configured sides h's Exposure covers. Sides the install
// doesn't configure get no routers.
func (s settings) sidesOf(h proxyHost) []side {
	var sides []side
	for _, sd := range s.sides {
		if h.Exposure == sd.name || h.Exposure == "both" {
			sides = append(sides, sd)
		}
	}
	return sides
}

// routerName is the name of h's router on one entrypoint (http or https) of
// a side. Each router has a single entrypoint, so Traefik never renames it.
func routerName(h proxyHost, sd side, entrypoint string) string {
	return fmt.Sprintf("host-%d-%s-%s", h.ID, sd.name, entrypoint)
}

// generate maps the Proxy Hosts to the Traefik dynamic-config snapshot. It
// fails rather than leave out or mangle a host it can't render safely.
func generate(hosts []proxyHost, s settings) (snapshot, error) {
	snap := snapshot{httpConfig{
		Routers:           map[string]router{},
		Services:          map[string]service{},
		Middlewares:       map[string]middleware{},
		ServersTransports: map[string]serversTransport{},
	}}
	for _, h := range hosts {
		if errs := h.problems(); len(errs) > 0 {
			return snapshot{}, fmt.Errorf("Proxy Host %d: %s", h.ID, strings.Join(errs, " "))
		}
		if !h.Enabled {
			continue
		}
		name := fmt.Sprintf("host-%d", h.ID)
		rules := make([]string, len(h.Domains))
		for i, d := range h.Domains {
			rules[i] = "Host(`" + d + "`)"
		}
		rule := strings.Join(rules, " || ")
		lb := loadBalancer{Servers: []server{{h.Forward.URL()}}}
		if h.SkipCertCheck {
			lb.ServersTransport = skipCertCheckTransport
			snap.HTTP.ServersTransports[skipCertCheckTransport] = serversTransport{true}
		}
		var httpMiddlewares []string
		if h.ForceSSL {
			httpMiddlewares = []string{forceSSLMiddleware}
			snap.HTTP.Middlewares[forceSSLMiddleware] = middleware{redirectScheme{"https", true}}
		}
		for _, sd := range s.sidesOf(h) {
			snap.HTTP.Routers[routerName(h, sd, "http")] = router{EntryPoints: []string{sd.http}, Rule: rule, Middlewares: httpMiddlewares, Service: name}
			snap.HTTP.Routers[routerName(h, sd, "https")] = router{EntryPoints: []string{sd.https}, Rule: rule, Service: name, TLS: &routerTLS{h.CertResolver}}
			snap.HTTP.Services[name] = service{lb}
		}
	}
	return snap, nil
}
