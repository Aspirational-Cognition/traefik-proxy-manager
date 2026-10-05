package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// cacheFor is how long one batch of Traefik API reads serves every viewer.
const cacheFor = 2 * time.Second

// traefikAPI reads Traefik's API. Each refresh is one batch of calls,
// cached and shared by every viewer, never a call per host.
type traefikAPI struct {
	base   *url.URL // basic auth, if any, rides in the URL
	client *http.Client
	now    func() time.Time

	mu        sync.Mutex
	fetchedAt time.Time
	live      liveTraefik
	err       error
}

// liveTraefik is what Traefik reports, as of one refresh.
type liveTraefik struct {
	routers     map[string]apiRouter // by name, e.g. host-1-internal-http@http
	middlewares map[string]string    // type by name, e.g. authentik@docker: forwardAuth
	entrypoints map[string]bool      // by name, e.g. websecure
	sans        map[string]bool      // every certificate's SANs, lowercased; nil when Traefik can't list them
}

type apiRouter struct {
	Name   string   `json:"name"`
	Status string   `json:"status"` // enabled, warning or disabled
	Error  []string `json:"error"`
}

type apiCertificate struct {
	SANs []string `json:"sans"`
}

// apiMiddleware is one Middleware: its name and type.
type apiMiddleware struct {
	Name, Type string
}

// UnmarshalJSON reads a Middleware's name and type. Traefik lowercases the
// type (forwardauth), so it's taken from the config's key (forwardAuth).
func (m *apiMiddleware) UnmarshalJSON(b []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(b, &fields); err != nil {
		return err
	}
	json.Unmarshal(fields["name"], &m.Name)
	json.Unmarshal(fields["type"], &m.Type)
	for key := range fields {
		if strings.EqualFold(key, m.Type) {
			m.Type = key
		}
	}
	return nil
}

func newTraefikAPI(base *url.URL, now func() time.Time) *traefikAPI {
	return &traefikAPI{base: base, client: &http.Client{Timeout: 2 * time.Second}, now: now}
}

// state is Traefik's latest reported state, refreshed when the cached one
// is older than cacheFor. A failed refresh is cached too.
func (t *traefikAPI) state() (liveTraefik, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.now().Sub(t.fetchedAt) >= cacheFor { // a zero fetchedAt is long ago
		t.live, t.err = t.fetch()
		t.fetchedAt = t.now()
	}
	return t.live, t.err
}

func (t *traefikAPI) fetch() (liveTraefik, error) {
	routers, err := getAll[apiRouter](t, "api/http/routers")
	if err != nil {
		return liveTraefik{}, err
	}
	middlewares, err := getAll[apiMiddleware](t, "api/http/middlewares")
	if err != nil {
		return liveTraefik{}, err
	}
	entrypoints, err := getAll[struct{ Name string }](t, "api/entrypoints")
	if err != nil {
		return liveTraefik{}, err
	}
	// Traefik older than 3.7 has no /api/certificates (404); without it, or
	// on any failure reading it, the certificate check is skipped.
	certs, err := getAll[apiCertificate](t, "api/certificates")
	live := liveTraefik{routers: map[string]apiRouter{}, middlewares: map[string]string{}, entrypoints: map[string]bool{}}
	if err == nil {
		live.sans = map[string]bool{}
	}
	for _, c := range certs {
		for _, san := range c.SANs {
			live.sans[strings.ToLower(san)] = true
		}
	}
	for _, r := range routers {
		live.routers[r.Name] = r
	}
	for _, m := range middlewares {
		live.middlewares[m.Name] = m.Type
	}
	for _, e := range entrypoints {
		live.entrypoints[e.Name] = true
	}
	return live, nil
}

// getAll reads every page of an API list. X-Next-Page names the next page,
// and is 1 on the last.
func getAll[T any](t *traefikAPI, path string) ([]T, error) {
	var all []T
	for page := 1; ; {
		u := t.base.JoinPath(path)
		u.RawQuery = url.Values{"per_page": {"100"}, "page": {strconv.Itoa(page)}}.Encode()
		var items []T
		next, err := t.get(u, &items)
		if err != nil {
			return nil, err
		}
		all = append(all, items...)
		if next <= page {
			return all, nil
		}
		page = next
	}
}

// get reads one API URL into v, returning its X-Next-Page.
func (t *traefikAPI) get(u *url.URL, v any) (int, error) {
	resp, err := t.client.Get(u.String())
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("Traefik API %s: %s", u.Path, resp.Status)
	}
	next, _ := strconv.Atoi(resp.Header.Get("X-Next-Page"))
	return next, json.NewDecoder(resp.Body).Decode(v)
}
