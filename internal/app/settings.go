package app

import (
	"cmp"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
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
	certResolvers []string // the first is the default
	port          int
	dataDir       string
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

	for _, name := range []string{"internal", "external"} {
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

	for r := range strings.SplitSeq(getenv("TPM_CERT_RESOLVERS"), ",") {
		if r = strings.TrimSpace(r); r != "" {
			s.certResolvers = append(s.certResolvers, r)
		}
	}
	if len(s.certResolvers) == 0 {
		errs = append(errs, errors.New("TPM_CERT_RESOLVERS is required: a comma-separated list of Traefik certificate resolvers, the first being the default"))
	}

	port := cmp.Or(getenv("TPM_PORT"), "8080")
	if s.port, err = strconv.Atoi(port); err != nil || s.port < 1 || s.port > 65535 {
		errs = append(errs, fmt.Errorf("TPM_PORT %q isn't a port number (1-65535)", port))
	}

	s.dataDir = cmp.Or(getenv("TPM_DATA_DIR"), "/data")
	return s, errors.Join(errs...)
}
