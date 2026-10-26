package app

import (
	"cmp"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

// side is one Exposure side the install configures: an http + https
// entrypoint pair.
type side struct {
	name, http, https string
}

// settings is tpm's install-time configuration, from TPM_* env vars.
type settings struct {
	traefikAPI    *url.URL
	sides         []side
	certResolvers []string            // the first is the default
	defaults      map[string][]string // each side's default Access rules, fully qualified, in order
	port          int
	dataDir       string
	timeouts      *forwardingTimeouts // for the skip-cert-check transport, which doesn't inherit Traefik's static ones; nil if unset
	login         loginSettings
}

// loginSettings is how tpm's own UI is signed in to.
type loginSettings struct {
	style              string // Local Login's style: "basic", "form", or "" for none by tpm
	username, password string
}

// parseSettings reads and validates the TPM_* env vars, reporting every
// problem at once.
func parseSettings(getenv func(string) string) (settings, error) {
	var s settings
	var errs []error

	api := getenv("TPM_TRAEFIK_API_URL")
	u, err := url.Parse(api)
	switch {
	case api == "":
		errs = append(errs, errors.New("TPM_TRAEFIK_API_URL is required, e.g. http://traefik:8080"))
	case err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "":
		errs = append(errs, fmt.Errorf("TPM_TRAEFIK_API_URL %q isn't an http(s) URL, e.g. http://traefik:8080", api))
	default:
		s.traefikAPI = u
	}

	s.defaults = map[string][]string{}
	for _, name := range []string{"internal", "external"} {
		key := "TPM_DEFAULT_MIDDLEWARES_" + strings.ToUpper(name)
		for _, m := range list(getenv(key)) {
			if !strings.Contains(m, "@") {
				errs = append(errs, fmt.Errorf("%s: %q isn't fully qualified, e.g. authentik@docker", key, m))
			}
			s.defaults[name] = append(s.defaults[name], m)
		}

		prefix := "TPM_ENTRYPOINT_" + strings.ToUpper(name)
		h, hs := getenv(prefix+"_HTTP"), getenv(prefix+"_HTTPS")
		switch {
		case h != "" && hs != "":
			s.sides = append(s.sides, side{name, h, hs})
		case h != "":
			errs = append(errs, fmt.Errorf("%s_HTTPS is required when %s_HTTP is set", prefix, prefix))
		case hs != "":
			errs = append(errs, fmt.Errorf("%s_HTTP is required when %s_HTTPS is set", prefix, prefix))
		}
	}
	if len(s.sides) == 0 && len(errs) == 0 {
		errs = append(errs, errors.New("configure at least one side: TPM_ENTRYPOINT_INTERNAL_HTTP and _HTTPS, or TPM_ENTRYPOINT_EXTERNAL_HTTP and _HTTPS"))
	}

	s.certResolvers = list(getenv("TPM_CERT_RESOLVERS"))
	if len(s.certResolvers) == 0 {
		errs = append(errs, errors.New("TPM_CERT_RESOLVERS is required: a comma-separated list of Traefik certificate resolvers, the first being the default"))
	}

	port := cmp.Or(getenv("TPM_PORT"), "8080")
	if s.port, err = strconv.Atoi(port); err != nil || s.port < 1 || s.port > 65535 {
		errs = append(errs, fmt.Errorf("TPM_PORT %q isn't a port number (1-65535)", port))
	}

	s.dataDir = cmp.Or(getenv("TPM_DATA_DIR"), "/data")
	if v := getenv("TPM_RESPONSE_HEADER_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err != nil || d <= 0 {
			errs = append(errs, fmt.Errorf("TPM_RESPONSE_HEADER_TIMEOUT %q isn't a positive duration, e.g. 30s", v))
		}
		s.timeouts = &forwardingTimeouts{v}
	}
	s.login, errs = parseLogin(getenv, errs)
	return s, errors.Join(errs...)
}

// parseLogin reads the Local Login settings, appending their problems to errs.
// Credentials are taken byte for byte: no trimming, no strength rule.
func parseLogin(getenv func(string) string, errs []error) (loginSettings, []error) {
	l := loginSettings{getenv("TPM_LOCAL_AUTH"), getenv("TPM_AUTH_USERNAME"), getenv("TPM_AUTH_PASSWORD")}
	if l.style == "" {
		var set []string
		for _, k := range []string{"TPM_AUTH_USERNAME", "TPM_AUTH_PASSWORD"} {
			if getenv(k) != "" {
				set = append(set, k)
			}
		}
		switch len(set) {
		case 1:
			errs = append(errs, fmt.Errorf("%s only applies with Local Login; set TPM_LOCAL_AUTH to basic or form, or remove it", set[0]))
		case 2:
			errs = append(errs, errors.New("TPM_AUTH_USERNAME and TPM_AUTH_PASSWORD only apply with Local Login; set TPM_LOCAL_AUTH to basic or form, or remove them"))
		}
		return l, errs
	}
	if l.style != "basic" && l.style != "form" {
		errs = append(errs, fmt.Errorf("TPM_LOCAL_AUTH %q isn't basic or form", l.style))
	}
	if l.username == "" {
		errs = append(errs, errors.New("TPM_AUTH_USERNAME is required when TPM_LOCAL_AUTH is set"))
	}
	if strings.Contains(l.username, ":") {
		errs = append(errs, errors.New(`TPM_AUTH_USERNAME can't contain ":" (Basic style splits on it)`))
	}
	if l.password == "" {
		errs = append(errs, errors.New("TPM_AUTH_PASSWORD is required when TPM_LOCAL_AUTH is set"))
	}
	return l, errs
}

// list splits a comma-separated setting, skipping blanks.
func list(v string) []string {
	var items []string
	for item := range strings.SplitSeq(v, ",") {
		if item = strings.TrimSpace(item); item != "" {
			items = append(items, item)
		}
	}
	return items
}

// defaultsFor is the admin's default Access rules for an Exposure. A Both
// host gets External's, so internal-only rules don't lock out the internet.
func (s settings) defaultsFor(exposure string) []string {
	if exposure == "both" {
		exposure = "external"
	}
	return s.defaults[exposure]
}

// accessList orders picked Access rules the way Traefik applies them: the
// picked defaults in their configured order, then the rest as picked.
func (s settings) accessList(exposure string, picked []string) []string {
	access := []string{}
	for _, m := range append(slices.Clone(s.defaultsFor(exposure)), picked...) {
		if slices.Contains(picked, m) && !slices.Contains(access, m) {
			access = append(access, m)
		}
	}
	return access
}
