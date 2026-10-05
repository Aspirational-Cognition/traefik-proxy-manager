package app

import (
	"embed"
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
//go:embed web/templates web/static/htmx.min.js web/static/app.css
var assets embed.FS

var templates = template.Must(template.ParseFS(assets, "web/templates/*.html"))

// staticFiles serves the vendored htmx and the Tailwind-built CSS.
var staticFiles = func() http.Handler {
	sub, _ := fs.Sub(assets, "web/static")
	return http.StripPrefix("/static/", http.FileServerFS(sub))
}()

// web is the Proxy Hosts UI.
type web struct {
	settings settings
	store    *store
	now      func() time.Time
}

// listView is the Proxy Hosts list; OOB marks an htmx out-of-band refresh.
type listView struct {
	Hosts []proxyHost
	OOB   bool
}

// formView is the add/edit modal: the values as typed, plus what blocks Save.
type formView struct {
	Domains, Host, Port string
	Resolvers           []resolverOption
	Errors              []string
}

type resolverOption struct {
	Name     string
	Selected bool
}

func (w *web) list(rw http.ResponseWriter, r *http.Request) {
	render(rw, http.StatusOK, "list.html", listView{Hosts: w.store.all()})
}

func (w *web) newForm(rw http.ResponseWriter, r *http.Request) {
	render(rw, http.StatusOK, "modal", withResolvers(formView{}, w.settings.certResolvers, w.settings.certResolvers[0]))
}

// withResolvers fills in the Certificate Resolver dropdown, selecting chosen.
func withResolvers(f formView, resolvers []string, chosen string) formView {
	for _, name := range resolvers {
		f.Resolvers = append(f.Resolvers, resolverOption{name, name == chosen})
	}
	return f
}

func (w *web) create(rw http.ResponseWriter, r *http.Request) {
	f := formView{Domains: r.FormValue("domains"), Host: r.FormValue("host"), Port: r.FormValue("port")}
	resolver := r.FormValue("certResolver")
	h, errs := w.parse(f, r.FormValue("scheme"), resolver)
	if len(errs) > 0 {
		f.Errors = errs
		render(rw, http.StatusUnprocessableEntity, "modal", withResolvers(f, w.settings.certResolvers, resolver))
		return
	}
	h.Enabled = true
	h.Middlewares = []string{}
	h.CreatedAt = w.now().UTC()
	h.UpdatedAt = h.CreatedAt
	if err := w.store.add(h); err != nil {
		http.Error(rw, "Couldn't save the Proxy Host: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// An empty #modal closes it; the list refreshes out of band.
	render(rw, http.StatusOK, "hosts", listView{Hosts: w.store.all(), OOB: true})
}

// parse reads the form into a Proxy Host and returns the messages that
// block its Save.
func (w *web) parse(f formView, scheme, resolver string) (proxyHost, []string) {
	port, err := strconv.Atoi(f.Port)
	if err != nil {
		port = 0 // reported as out of range
	}
	h := proxyHost{
		Domains:      strings.Fields(strings.ToLower(strings.ReplaceAll(f.Domains, ",", " "))),
		Forward:      forward{scheme, f.Host, port},
		Exposure:     w.settings.sides[0].name,
		CertResolver: resolver,
	}
	errs := h.problems()
	if !slices.Contains(w.settings.certResolvers, resolver) {
		errs = append(errs, "Pick a Certificate Resolver from the list.")
	}
	return h, errs
}

func render(rw http.ResponseWriter, status int, name string, data any) {
	rw.Header().Set("Content-Type", "text/html; charset=utf-8")
	rw.WriteHeader(status)
	if err := templates.ExecuteTemplate(rw, name, data); err != nil {
		log.Printf("render %s: %v", name, err)
	}
}
