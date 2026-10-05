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
	Routers  map[string]router  `json:"routers"`
	Services map[string]service `json:"services"`
}

type router struct {
	EntryPoints []string   `json:"entryPoints"`
	Rule        string     `json:"rule"`
	Service     string     `json:"service"`
	TLS         *routerTLS `json:"tls,omitempty"`
}

type routerTLS struct {
	CertResolver string `json:"certResolver"`
}

type service struct {
	LoadBalancer loadBalancer `json:"loadBalancer"`
}

type loadBalancer struct {
	Servers []server `json:"servers"`
}

type server struct {
	URL string `json:"url"`
}

// generate maps the Proxy Hosts to the Traefik dynamic-config snapshot. It
// fails rather than leave out or mangle a host it can't render safely.
func generate(hosts []proxyHost, s settings) (snapshot, error) {
	snap := snapshot{httpConfig{map[string]router{}, map[string]service{}}}
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
		// Sides the install doesn't configure get no routers.
		for _, sd := range s.sides {
			if h.Exposure != sd.name && h.Exposure != "both" {
				continue
			}
			snap.HTTP.Routers[name+"-"+sd.name+"-http"] = router{[]string{sd.http}, rule, name, nil}
			snap.HTTP.Routers[name+"-"+sd.name+"-https"] = router{[]string{sd.https}, rule, name, &routerTLS{h.CertResolver}}
			snap.HTTP.Services[name] = service{loadBalancer{[]server{{h.Forward.URL()}}}}
		}
	}
	return snap, nil
}
