package app

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

var (
	domainSyntax = regexp.MustCompile(`(?i)^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)*$`)
	hostSyntax   = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
)

// problems lists what makes h a Proxy Host Traefik would reject or misread,
// in the words Save shows the user. Every stored host has none.
func (h proxyHost) problems() []string {
	var errs []string
	if len(h.Domains) == 0 {
		errs = append(errs, "Enter at least one domain name.")
	}
	for _, d := range h.Domains {
		switch {
		case strings.Contains(d, "*"):
			errs = append(errs, fmt.Sprintf("Wildcard domains like '%s' aren't supported.", d))
		case !domainSyntax.MatchString(d):
			errs = append(errs, fmt.Sprintf("'%s' isn't a valid domain name.", d))
		}
	}
	if h.Forward.Scheme != "http" && h.Forward.Scheme != "https" {
		errs = append(errs, "Scheme must be http or https.")
	}
	if !hostSyntax.MatchString(h.Forward.Host) {
		errs = append(errs, "Enter the hostname or IP address to send traffic to.")
	}
	if h.Exposure != "internal" && h.Exposure != "external" && h.Exposure != "both" {
		errs = append(errs, fmt.Sprintf("Exposure '%s' isn't Internal, External or Both.", h.Exposure))
	}
	if h.Forward.Port < 1 || h.Forward.Port > 65535 {
		errs = append(errs, "Port must be a number from 1 to 65535.")
	}
	if d, err := time.ParseDuration(h.ResponseTimeout); h.ResponseTimeout != "" && (err != nil || d < 0) {
		errs = append(errs, fmt.Sprintf("Response timeout '%s' isn't valid. Use a duration such as 10m, or 0 for no limit.", h.ResponseTimeout))
	}
	return errs
}
