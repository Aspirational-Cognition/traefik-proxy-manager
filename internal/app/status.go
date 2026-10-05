package app

import (
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"
)

// pendingFor is how long after Traefik first fetches a host's current
// version that host stays Pending, giving Traefik time to load it.
const pendingFor = 10 * time.Second

// hostStatus is whether a Proxy Host is live in Traefik, with the reasons
// shown next to it.
type hostStatus struct {
	Value    string // Online, Warning, Error, Pending, Unknown or Disabled
	Messages []string
}

// statuses answers the list's 5-second refresh: every Status cell, swapped
// in out of band.
func (w *web) statuses(rw http.ResponseWriter, r *http.Request) {
	render(rw, http.StatusOK, "statuses", w.rows())
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
	if at, ok := w.polls.servedAt(h.UpdatedAt); !ok || w.now().Before(at.Add(pendingFor)) {
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
