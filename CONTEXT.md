# Traefik Proxy Manager (tpm)

A simple, Nginx-Proxy-Manager-style UI for adding proxy routes to a single running Traefik instance, aimed at people who know NPM but not Traefik's concepts.

## Language

**Proxy Host**:
One or more domain names forwarded to a single Forward Target, created and owned by tpm. The only kind of route tpm manages.
_Avoid_: Router, service, route (in user-facing text)

**Forward Target**:
The scheme, hostname/IP, and port a Proxy Host sends traffic to.
_Avoid_: Backend, upstream, server

**Exposure**:
Which networks a Proxy Host is reachable from: **Internal**, **External**, or **Both**. Set once per Proxy Host; tpm maps it to the matching Traefik entrypoints. An install may provide only Internal or only External; then there is no choice to make.
_Avoid_: Zone, network, visibility, scope

**Label Route**:
A route defined in Traefik by Docker labels or other config outside tpm. tpm never edits these.
_Avoid_: External route, docker route

**Force SSL**:
A Proxy Host option that redirects plain HTTP requests to HTTPS.
_Avoid_: HTTPS redirect, redirect scheme

**Response timeout**:
How long Traefik waits for a Forward Target's response headers before returning a 504. Optional per Proxy Host: empty keeps the default, and `0` means no limit.
_Avoid_: Response header timeout (in user-facing text), upstream timeout, gateway timeout

**Certificate Resolver**:
A certificate source already configured in Traefik that a Proxy Host can use for its HTTPS certificate. tpm selects one; it never issues or stores certificates.
_Avoid_: SSL certificate, cert

**Middleware**:
A request-handling rule already defined in Traefik (e.g. Authentik forward-auth) that a Proxy Host can opt into. tpm selects existing ones; it never defines them. Users see each one as an **Access rule**, picked in the form's Access section.
_Avoid_: Access list, plugin

**Local Login**:
Signing in to tpm's own UI with the single username and password tpm is configured with, checked by tpm itself. Comes in one of two styles: **Basic** (the browser's own prompt) or **Form** (a tpm sign-in page).
_Avoid_: Basic auth (as a general term), local auth, password login

**Proxy Login**:
Signing in to tpm's own UI through an identity provider (Authentik, Authelia, Auth0 via oauth2-proxy, …) that Traefik consults before the request reaches tpm.
_Avoid_: SSO, forward auth (in user-facing text), OIDC

**Fallback**:
Local Login used as the way in to tpm when Proxy Login is configured but unavailable, e.g. the identity provider is down or the internet link is. Reached at its own address rather than tpm's usual one.
_Avoid_: Break-glass, backup login

**Domain Clash**:
A domain on an enabled Proxy Host that another enabled Proxy Host, or a Label Route, also answers on a shared entrypoint (the same Exposure side). tpm warns but never blocks.
_Avoid_: Conflict, duplicate, collision

**Status**:
Whether a Proxy Host is actually live, as Traefik reports it: **Online**, **Warning**, **Error**, **Pending** (not loaded by Traefik yet), **Unknown** (Traefik can't be reached), or **Disabled** (switched off in tpm). A Domain Clash is flagged alongside the Status, not as one of its values.
_Avoid_: Health, state
