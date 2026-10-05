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

**Certificate Resolver**:
A certificate source already configured in Traefik that a Proxy Host can use for its HTTPS certificate. tpm selects one; it never issues or stores certificates.
_Avoid_: SSL certificate, cert

**Middleware**:
A request-handling rule already defined in Traefik (e.g. Authentik forward-auth) that a Proxy Host can opt into. tpm selects existing ones; it never defines them.
_Avoid_: Access list, plugin

**Domain Clash**:
A domain on an enabled Proxy Host that another enabled Proxy Host, or a Label Route, also answers on a shared entrypoint (the same Exposure side). tpm warns but never blocks.
_Avoid_: Conflict, duplicate, collision
