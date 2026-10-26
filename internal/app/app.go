// Package app is tpm: one http.Handler serving the Proxy Hosts UI and
// Traefik's HTTP-provider config endpoint.
package app

import (
	"encoding/json"
	"log"
	"net/http"
	"time"
)

// App is the whole of tpm behind one HTTP surface.
type App struct {
	http.Handler
	Port int // TPM_PORT, which the caller listens on
}

// New builds tpm from its TPM_* settings (read through getenv) and a clock.
func New(getenv func(string) string, now func() time.Time) (*App, error) {
	s, err := parseSettings(getenv)
	if err != nil {
		return nil, err
	}
	l := newLogin(s.login)
	log.Print(l)
	st, err := openStore(s.dataDir, now)
	if err != nil {
		return nil, err
	}
	polls := &pollLog{}
	w := &web{settings: s, store: st, polls: polls, traefik: newTraefikAPI(s.traefikAPI, now), now: now}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", w.list)
	mux.HandleFunc("GET /proxy-hosts/new", w.newForm)
	mux.HandleFunc("GET /proxy-hosts/status", w.statuses)
	mux.HandleFunc("GET /proxy-hosts/clashes", w.clashCheck)
	mux.HandleFunc("POST /proxy-hosts", w.create)
	mux.HandleFunc("GET /proxy-hosts/{id}/edit", w.editForm)
	mux.HandleFunc("POST /proxy-hosts/{id}", w.update)
	mux.HandleFunc("DELETE /proxy-hosts/{id}", w.delete)
	mux.HandleFunc("POST /proxy-hosts/{id}/enable", w.setEnabled(true))
	mux.HandleFunc("POST /proxy-hosts/{id}/disable", w.setEnabled(false))
	mux.Handle("GET /static/", staticFiles)
	mux.HandleFunc("GET /traefik/config", func(rw http.ResponseWriter, r *http.Request) {
		// Anything short of the full snapshot is a 503, so Traefik keeps its
		// last good config instead of dropping hosts.
		at := now()
		hosts, version := st.versioned()
		snap, err := generate(hosts, s)
		var b []byte
		if err == nil {
			b, err = json.Marshal(snap)
		}
		if err != nil {
			log.Printf("can't render Traefik config: %v", err)
			http.Error(rw, "tpm can't render its config: "+err.Error(), http.StatusServiceUnavailable)
			return
		}
		polls.record(at, version)
		rw.Header().Set("Content-Type", "application/json")
		rw.Write(b)
	})
	// Refuse cross-origin form posts (CSRF); Traefik's GET polls are unaffected.
	return &App{Handler: http.NewCrossOriginProtection().Handler(l.wrap(mux)), Port: s.port}, nil
}
