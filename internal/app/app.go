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
	st, err := openStore(s.dataDir)
	if err != nil {
		return nil, err
	}
	w := &web{settings: s, store: st, now: now}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", w.list)
	mux.HandleFunc("GET /proxy-hosts/new", w.newForm)
	mux.HandleFunc("POST /proxy-hosts", w.create)
	mux.Handle("GET /static/", staticFiles)
	mux.HandleFunc("GET /traefik/config", func(rw http.ResponseWriter, r *http.Request) {
		// Anything short of the full snapshot is a 503, so Traefik keeps its
		// last good config instead of dropping hosts.
		snap, err := generate(st.all(), s)
		var b []byte
		if err == nil {
			b, err = json.Marshal(snap)
		}
		if err != nil {
			log.Printf("can't render Traefik config: %v", err)
			http.Error(rw, "tpm can't render its config: "+err.Error(), http.StatusServiceUnavailable)
			return
		}
		rw.Header().Set("Content-Type", "application/json")
		rw.Write(b)
	})
	// Refuse cross-origin form posts (CSRF); Traefik's GET polls are unaffected.
	return &App{Handler: http.NewCrossOriginProtection().Handler(mux), Port: s.port}, nil
}
