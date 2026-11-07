package app

import (
	"embed"
	"encoding/json"
	"errors"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

// app.css must exist: `make css` builds it before any go build or test.
//
//go:embed web/templates web/static/htmx.min.js web/static/app.js web/static/app.css
var assets embed.FS

var templates = template.Must(template.ParseFS(assets, "web/templates/*.html"))

// staticFiles serves the vendored htmx, tpm's script and the Tailwind-built CSS.
var staticFiles = func() http.Handler {
	sub, _ := fs.Sub(assets, "web/static")
	return http.StripPrefix("/static/", http.FileServerFS(sub))
}()

// web is the Proxy Hosts UI.
type web struct {
	settings settings
	store    *store
	polls    *pollLog
	traefik  *traefikAPI
	now      func() time.Time
}

// listView is the Proxy Hosts list under its global banners; OOB marks an
// htmx out-of-band refresh.
type listView struct {
	Hosts   []hostRow
	Banners []string
	OOB     bool
	SignOut bool // a Form session, which the app bar offers to end
}

// hostRow is one row of the list: the stored host, its live Status and its
// Domain Clashes.
type hostRow struct {
	proxyHost
	Status  hostStatus
	Clashes []domainClash
}

// formView is the add/edit modal: the values as typed, plus what blocks Save.
// ID is 0 for a new host; UpdatedAt is the version the form was opened on.
type formView struct {
	ID                                        int
	UpdatedAt                                 string
	Domains, Scheme, Host, Port, CertResolver string
	ResponseTimeout                           string
	Exposure                                  string
	Access                                    []string // the picked Access rules, fully qualified
	ForceSSL, SkipCertCheck, Enabled          bool
	Exposures                                 []exposureOption // none when the install has one side
	Resolvers                                 []resolverOption
	AccessRules                               []accessOption
	Defaults                                  string   // each Exposure's default Access rules, as JSON
	AccessUnknown                             bool     // Traefik can't be reached to list Access rules
	Problems                                  []string // Traefik's, for a host in Error
	Clashes                                   []domainClash
	Errors                                    []string
}

type exposureOption struct {
	Value, Label, Hint string
	Checked            bool
}

// exposures are the Exposure choices, in the words the user sees.
var exposures = []exposureOption{
	{Value: "internal", Label: "Internal", Hint: "Reachable from your network only"},
	{Value: "external", Label: "External", Hint: "Reachable from the internet only"},
	{Value: "both", Label: "Both", Hint: "Reachable from your network and the internet"},
}

// accessOption is one Access rule in the modal's checklist.
type accessOption struct {
	Value, Name, Type         string // fully qualified; without @provider; e.g. forwardAuth
	Checked, Default, Missing bool
}

type resolverOption struct {
	Name               string
	Selected, Unlisted bool
}

func (w *web) list(rw http.ResponseWriter, r *http.Request) {
	render(rw, http.StatusOK, "list.html", listView{Hosts: w.rows(), Banners: w.banners(), SignOut: w.settings.login.signOut(r)})
}

// newForm opens a blank modal. Sides list Internal first, so a new host
// defaults to Internal and nothing is exposed to the internet by accident.
func (w *web) newForm(rw http.ResponseWriter, r *http.Request) {
	w.modal(rw, http.StatusOK, formView{Scheme: "http", Exposure: w.settings.sides[0].name, Access: w.settings.defaultsFor(w.settings.sides[0].name), ForceSSL: true, CertResolver: w.settings.certResolvers[0], Enabled: true})
}

func (w *web) editForm(rw http.ResponseWriter, r *http.Request) {
	h, ok := w.store.get(pathID(r))
	if !ok {
		http.NotFound(rw, r)
		return
	}
	var problems []string
	live, err := w.traefik.state()
	if st := w.status(h, live, err); st.Value == "Error" {
		problems = st.Messages
	}
	w.modal(rw, http.StatusOK, formView{
		Problems:        problems,
		ID:              h.ID,
		UpdatedAt:       h.UpdatedAt.Format(time.RFC3339Nano),
		Domains:         strings.Join(h.Domains, ", "),
		Scheme:          h.Forward.Scheme,
		Host:            h.Forward.Host,
		Port:            strconv.Itoa(h.Forward.Port),
		Exposure:        h.Exposure,
		Access:          h.Middlewares,
		ForceSSL:        h.ForceSSL,
		SkipCertCheck:   h.SkipCertCheck,
		ResponseTimeout: h.ResponseTimeout,
		CertResolver:    h.CertResolver,
		Enabled:         h.Enabled,
	})
}

// modal renders the add/edit modal, filling in the Exposure cards (only
// when there's a choice to make) and the Certificate Resolver dropdown. A
// host's resolver that's no longer configured stays selected, flagged, so
// Save is blocked rather than quietly switching resolvers.
func (w *web) modal(rw http.ResponseWriter, status int, f formView) {
	if len(w.settings.sides) > 1 {
		for _, e := range exposures {
			e.Checked = e.Value == f.Exposure
			f.Exposures = append(f.Exposures, e)
		}
	}
	for _, name := range w.settings.certResolvers {
		f.Resolvers = append(f.Resolvers, resolverOption{Name: name, Selected: name == f.CertResolver})
	}
	if f.CertResolver != "" && !slices.Contains(w.settings.certResolvers, f.CertResolver) {
		f.Resolvers = append(f.Resolvers, resolverOption{f.CertResolver, true, true})
	}
	w.accessPicker(&f)
	f.Clashes = w.formClashes(f)
	render(rw, status, "modal", f)
}

// accessPicker fills in the Access checklist: the picked rules in order, then
// every Middleware Traefik lists and, when there's an Exposure to change,
// the other Exposures' defaults for the script to swap in. A picked rule
// Traefik doesn't list stays ticked, flagged missing, so the user can untick
// it on purpose. Without Traefik, only those picks and defaults show.
func (w *web) accessPicker(f *formView) {
	live, err := w.traefik.state()
	f.AccessUnknown = err != nil
	byExposure := map[string][]string{f.Exposure: w.settings.defaultsFor(f.Exposure)}
	var others []string
	for _, e := range f.Exposures {
		byExposure[e.Value] = w.settings.defaultsFor(e.Value)
		others = append(others, byExposure[e.Value]...)
	}
	for name := range live.middlewares {
		// tpm's own and Traefik's own (e.g. its dashboard's) aren't Access rules.
		if name != forceSSLMiddleware+"@http" && !strings.HasSuffix(name, "@internal") {
			others = append(others, name)
		}
	}
	slices.Sort(others)
	defaults, _ := json.Marshal(byExposure)
	f.Defaults = string(defaults)
	var seen []string
	for _, m := range append(slices.Clone(f.Access), others...) {
		if slices.Contains(seen, m) {
			continue
		}
		seen = append(seen, m)
		typ, listed := live.middlewares[m]
		f.AccessRules = append(f.AccessRules, accessOption{
			Value: m, Name: accessRuleName(m), Type: typ,
			Checked: slices.Contains(f.Access, m), Default: slices.Contains(byExposure[f.Exposure], m),
		})
		last := &f.AccessRules[len(f.AccessRules)-1]
		last.Missing = last.Checked && err == nil && !listed
	}
}

func (w *web) create(rw http.ResponseWriter, r *http.Request) {
	f := readForm(r)
	if len(w.settings.sides) == 1 {
		f.Exposure = w.settings.sides[0].name // no choice to make
	}
	h, errs := w.parse(f)
	if len(errs) > 0 {
		f.Errors = errs
		w.modal(rw, http.StatusUnprocessableEntity, f)
		return
	}
	if err := w.store.add(h); err != nil {
		http.Error(rw, "Couldn't save the Proxy Host: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.saved(rw)
}

func (w *web) update(rw http.ResponseWriter, r *http.Request) {
	f := readForm(r)
	f.ID = pathID(r)
	old, ok := w.store.get(f.ID)
	if !ok {
		http.NotFound(rw, r)
		return
	}
	if len(w.settings.sides) == 1 {
		f.Exposure = old.Exposure // no choice offered, so keep a removed side's Exposure
	}
	edited, errs := w.parse(f)
	if len(errs) > 0 {
		f.Errors = errs
		w.modal(rw, http.StatusUnprocessableEntity, f)
		return
	}
	seen, _ := time.Parse(time.RFC3339Nano, f.UpdatedAt)
	err := w.store.update(f.ID, &seen, func(h *proxyHost) {
		// The form carries every other field; the store stamps updatedAt.
		edited.ID, edited.CreatedAt = h.ID, h.CreatedAt
		*h = edited
	})
	switch {
	case errors.Is(err, errNotFound):
		http.NotFound(rw, r)
	case errors.Is(err, errStale):
		f.Errors = []string{"This host was changed by someone else; reload."}
		w.modal(rw, http.StatusConflict, f)
	case err != nil:
		http.Error(rw, "Couldn't save the Proxy Host: "+err.Error(), http.StatusInternalServerError)
	default:
		w.saved(rw)
	}
}

func (w *web) delete(rw http.ResponseWriter, r *http.Request) {
	w.respondWithList(rw, r, w.store.delete(pathID(r)))
}

// setEnabled switches a host on or off from its row.
func (w *web) setEnabled(enabled bool) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		w.respondWithList(rw, r, w.store.update(pathID(r), nil, func(h *proxyHost) { h.Enabled = enabled }))
	}
}

// respondWithList answers a row action with the refreshed list.
func (w *web) respondWithList(rw http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, errNotFound):
		http.NotFound(rw, r)
	case err != nil:
		http.Error(rw, "Couldn't save the change: "+err.Error(), http.StatusInternalServerError)
	default:
		render(rw, http.StatusOK, "hosts", listView{Hosts: w.rows()})
	}
}

// saved answers a successful Save: an empty #modal closes it, and the list
// refreshes out of band.
func (w *web) saved(rw http.ResponseWriter) {
	render(rw, http.StatusOK, "hosts", listView{Hosts: w.rows(), OOB: true})
}

// pathID is the Proxy Host ID in the URL; 0, which no host has, if malformed.
func pathID(r *http.Request) int {
	id, _ := strconv.Atoi(r.PathValue("id"))
	return id
}

// readForm reads the add/edit modal's fields as typed.
func readForm(r *http.Request) formView {
	r.ParseForm() // so r.Form is filled however the fields below are evaluated
	return formView{
		UpdatedAt:       r.FormValue("updatedAt"),
		Domains:         r.FormValue("domains"),
		Scheme:          r.FormValue("scheme"),
		Host:            r.FormValue("host"),
		Port:            r.FormValue("port"),
		Exposure:        r.FormValue("exposure"),
		ForceSSL:        r.FormValue("forceSsl") != "",
		SkipCertCheck:   r.FormValue("skipCertCheck") != "",
		ResponseTimeout: r.FormValue("responseTimeout"),
		CertResolver:    r.FormValue("certResolver"),
		Enabled:         r.FormValue("enabled") != "",
		Access:          r.Form["access"],
	}
}

// parse reads the form into a Proxy Host and returns the messages that
// block its Save.
func (w *web) parse(f formView) (proxyHost, []string) {
	port, err := strconv.Atoi(f.Port)
	if err != nil {
		port = 0 // reported as out of range
	}
	h := proxyHost{
		Enabled:         f.Enabled,
		Domains:         splitDomains(f.Domains),
		Forward:         forward{f.Scheme, f.Host, port},
		Exposure:        f.Exposure,
		ForceSSL:        f.ForceSSL,
		SkipCertCheck:   f.SkipCertCheck && f.Scheme == "https", // the toggle only shows for https
		ResponseTimeout: f.ResponseTimeout,
		CertResolver:    f.CertResolver,
		Middlewares:     w.settings.accessList(f.Exposure, f.Access),
	}
	errs := h.problems()
	if !slices.Contains(w.settings.certResolvers, f.CertResolver) {
		errs = append(errs, "Pick a Certificate Resolver from the list.")
	}
	return h, errs
}

// splitDomains reads the Domain Names field: comma- or space-separated, lowercased.
func splitDomains(field string) []string {
	return strings.Fields(strings.ToLower(strings.ReplaceAll(field, ",", " ")))
}

// AccessRules are h's Access rules as users see them, in the order they apply.
func (h proxyHost) AccessRules() []string {
	var names []string
	for _, m := range h.Middlewares {
		names = append(names, accessRuleName(m))
	}
	return names
}

// accessRuleName is a Middleware's Traefik name without @provider, as users
// see it, e.g. authentik for authentik@docker.
func accessRuleName(middleware string) string {
	name, _, _ := strings.Cut(middleware, "@")
	return name
}

func render(rw http.ResponseWriter, status int, name string, data any) {
	rw.Header().Set("Content-Type", "text/html; charset=utf-8")
	rw.WriteHeader(status)
	if err := templates.ExecuteTemplate(rw, name, data); err != nil {
		log.Printf("render %s: %v", name, err)
	}
}
