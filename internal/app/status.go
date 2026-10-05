package app

import (
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"time"
)

// pendingFor is how long after Traefik first fetches a host's current
// version that host stays Pending, giving Traefik time to load it.
const pendingFor = 10 * time.Second

// hostStatus is whether a Proxy Host is live in Traefik, with the reasons
// shown next to it.
type hostStatus struct {
	Value    string // Online, Pending, Error, Unknown or Disabled
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
// it), in priority order: Disabled, Unknown, Pending, Error, Online.
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
	// The worst across the host's routers, with every reason given once.
	st := hostStatus{Value: "Online"}
	fail := func(msg string) {
		st.Value = "Error"
		if !slices.Contains(st.Messages, msg) {
			st.Messages = append(st.Messages, msg)
		}
	}
	for _, sd := range w.settings.sidesOf(h) {
		for _, ep := range []string{"http", "https"} {
			r, ok := live.routers[routerName(h, sd, ep)+"@http"]
			switch {
			case !ok:
				fail("Traefik didn't load this host. Check Traefik's logs.")
			case r.Status == "disabled":
				st.Value = "Error" // even when Traefik gives no reason
				for _, e := range r.Error {
					fail(reword(e))
				}
			}
		}
	}
	return st
}

var missingEntrypoint = regexp.MustCompile(`^entryPoint "(.+)" doesn't exist$`)

// reword puts a Traefik error in plain words, or shows it raw.
func reword(err string) string {
	if m := missingEntrypoint.FindStringSubmatch(err); m != nil {
		return fmt.Sprintf("Traefik has no entrypoint '%s'. Check tpm's settings.", m[1])
	}
	return "Traefik said: " + err
}
