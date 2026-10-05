package app

import (
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"
)

// staleAfter is how long Traefik can go without fetching tpm's config
// before tpm warns that changes aren't reaching it. Fixed, not a setting.
const staleAfter = 60 * time.Second

// pendingFor is how long after Traefik first fetches a host's current
// version that host stays Pending, giving Traefik time to load it.
const pendingFor = 10 * time.Second

// hostStatus is whether a Proxy Host is live in Traefik, with the reasons
// shown next to it.
type hostStatus struct {
	Value    string // Online, Warning, Error, Pending, Unknown or Disabled
	Messages []string
}

// statuses answers the list's 5-second refresh: every Status cell and the
// global banners, swapped in out of band.
func (w *web) statuses(rw http.ResponseWriter, r *http.Request) {
	render(rw, http.StatusOK, "statuses", listView{Hosts: w.rows(), Banners: w.banners(), OOB: true})
}

// banners are the global warnings that changes aren't reaching Traefik, or
// that the install's settings don't match it. Each clears with its cause.
func (w *web) banners() []string {
	var bs []string
	last, polled := w.polls.lastAt()
	stale := polled && w.now().Sub(last) > staleAfter
	switch {
	case !polled:
		bs = append(bs, "Traefik hasn't loaded tpm's config yet; check providers.http in Traefik's static config.")
	case stale:
		bs = append(bs, "Traefik hasn't fetched tpm's config in the last minute, so changes aren't reaching it.")
	}
	live, err := w.traefik.state()
	if err != nil {
		return append(bs, "tpm can't reach Traefik's API, so it can't tell which Proxy Hosts are live. Check TPM_TRAEFIK_API_URL.")
	}
	// Hosts Traefik has had time to load, yet none of them loaded: Traefik
	// threw the whole config out.
	expected := slices.ContainsFunc(w.store.all(), func(h proxyHost) bool {
		return h.Enabled && len(w.settings.sidesOf(h)) > 0 && !w.pending(h)
	})
	if polled && !stale && expected && !live.loadedAnyHost() {
		bs = append(bs, "Traefik is fetching tpm's config but loaded none of its Proxy Hosts, so it rejected the whole update. Check Traefik's logs.")
	}
	for _, sd := range w.settings.sides {
		for _, ep := range [][2]string{{"HTTP", sd.http}, {"HTTPS", sd.https}} {
			if !live.entrypoints[ep[1]] {
				bs = append(bs, fmt.Sprintf("Traefik has no entrypoint '%s' (TPM_ENTRYPOINT_%s_%s).", ep[1], strings.ToUpper(sd.name), ep[0]))
			}
		}
	}
	for _, sd := range []string{"internal", "external"} {
		for _, m := range w.settings.defaults[sd] {
			if _, ok := live.middlewares[m]; !ok {
				bs = append(bs, fmt.Sprintf("Traefik has no Access rule '%s' (TPM_DEFAULT_MIDDLEWARES_%s).", m, strings.ToUpper(sd)))
			}
		}
	}
	return bs
}

// loadedAnyHost is whether Traefik lists any of tpm's routers.
func (l liveTraefik) loadedAnyHost() bool {
	for name := range l.routers {
		if strings.HasPrefix(name, "host-") && strings.HasSuffix(name, "@http") {
			return true
		}
	}
	return false
}

// rows pairs every stored Proxy Host with its Status.
func (w *web) rows() []hostRow {
	live, err := w.traefik.state()
	var rows []hostRow
	for _, h := range w.store.all() {
		rows = append(rows, hostRow{h, w.status(h, live, err)})
	}
	return rows
}

// status is h's Status given what Traefik reports (or the error reaching
// it), in priority order: Disabled, Unknown, Pending, Error, Warning, Online.
func (w *web) status(h proxyHost, live liveTraefik, liveErr error) hostStatus {
	switch {
	case !h.Enabled:
		return hostStatus{Value: "Disabled"}
	case liveErr != nil:
		return hostStatus{Value: "Unknown"}
	}
	if w.pending(h) {
		return hostStatus{Value: "Pending"}
	}
	// The worst of Error and Warning, with every reason given once.
	st := hostStatus{Value: "Online"}
	flag := func(value, msg string) {
		if st.Value != "Error" {
			st.Value = value
		}
		if msg != "" && !slices.Contains(st.Messages, msg) {
			st.Messages = append(st.Messages, msg)
		}
	}
	sides := w.settings.sidesOf(h)
	unconfigured := "Warning"
	if len(sides) == 0 {
		unconfigured = "Error" // no routers at all
	}
	for _, e := range exposures {
		if e.Value != "both" && (h.Exposure == e.Value || h.Exposure == "both") && !slices.ContainsFunc(sides, func(sd side) bool { return sd.name == e.Value }) {
			flag(unconfigured, e.Label+" isn't configured")
		}
	}
	if !slices.Contains(w.settings.certResolvers, h.CertResolver) {
		flag("Warning", fmt.Sprintf("Certificate Resolver '%s' isn't configured", h.CertResolver))
	}
	for _, sd := range sides {
		for _, ep := range []string{"http", "https"} {
			r, ok := live.routers[routerName(h, sd, ep)+"@http"]
			switch {
			case !ok:
				flag("Error", "Traefik didn't load this host. Check Traefik's logs.")
			case r.Status == "disabled" || r.Status == "warning":
				value := "Error"
				if r.Status == "warning" {
					value = "Warning"
				}
				flag(value, "") // even when Traefik gives no reason
				for _, e := range r.Error {
					flag(value, reword(e))
				}
			}
		}
	}
	for _, d := range h.Domains {
		if len(sides) > 0 && live.missingCert(d) {
			flag("Warning", "No certificate yet for "+d)
		}
	}
	return st
}

// pending is whether Traefik may not have loaded h's current version yet.
func (w *web) pending(h proxyHost) bool {
	at, ok := w.polls.servedAt(h.UpdatedAt)
	return !ok || w.now().Before(at.Add(pendingFor))
}

// missingCert is whether Traefik has no certificate for domain d, exactly
// or by a wildcard one label up. Never when Traefik can't list them.
func (l liveTraefik) missingCert(d string) bool {
	d = strings.ToLower(d)
	_, parent, _ := strings.Cut(d, ".")
	return l.sans != nil && !l.sans[d] && !l.sans["*."+parent]
}

var (
	missingEntrypoint = regexp.MustCompile(`^entryPoint "(.+)" doesn't exist$`)
	missingMiddleware = regexp.MustCompile(`^middleware "(.+)" does not exist$`)
)

// reword puts a Traefik error in plain words, or shows it raw.
func reword(err string) string {
	if m := missingEntrypoint.FindStringSubmatch(err); m != nil {
		return fmt.Sprintf("Traefik has no entrypoint '%s'. Check tpm's settings.", m[1])
	}
	if m := missingMiddleware.FindStringSubmatch(err); m != nil {
		return fmt.Sprintf("Access rule '%s' no longer exists in Traefik", accessRuleName(m[1]))
	}
	return "Traefik said: " + err
}
