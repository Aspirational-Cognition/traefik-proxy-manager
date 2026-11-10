# Traefik Proxy Manager (tpm)

[![main](https://img.shields.io/github/actions/workflow/status/Aspirational-Cognition/traefik-proxy-manager/ci.yml?branch=main&label=main&logo=github)](https://github.com/Aspirational-Cognition/traefik-proxy-manager/actions/workflows/ci.yml?query=branch%3Amain)
[![dev](https://img.shields.io/github/actions/workflow/status/Aspirational-Cognition/traefik-proxy-manager/ci.yml?branch=dev&label=dev&logo=github)](https://github.com/Aspirational-Cognition/traefik-proxy-manager/actions/workflows/ci.yml?query=branch%3Adev)
[![release](https://img.shields.io/github/v/tag/Aspirational-Cognition/traefik-proxy-manager?sort=semver&label=release)](https://github.com/Aspirational-Cognition/traefik-proxy-manager/tags)
[![image](https://img.shields.io/badge/image-ghcr.io-2496ed?logo=docker&logoColor=white)](https://github.com/Aspirational-Cognition/traefik-proxy-manager/pkgs/container/traefik-proxy-manager)
[![license](https://img.shields.io/github/license/Aspirational-Cognition/traefik-proxy-manager)](LICENSE)

A simple, Nginx-Proxy-Manager-style UI for adding Proxy Hosts to a running Traefik v3.

## Description

Traefik proxy manager is an homage to [nginx proxy manager](https://github.com/NginxProxyManager/nginx-proxy-manager)
designed for use on all the systems where we had been using Traefik on docker as the primary proxy configured by labels
but with a low priority route catch-all that went to NginxProxyManager for easily adding other network resources for
proxying.

The idea came up when we wanted to start adding tracing and metrics through the stack for increased observability using
on otel collector stack. NginxProxyManager became a black box, and we didn't want to try hacking modules into the base
container. So we tried the very complete and full-featured [Traefik Manager](https://github.com/chr0nzz/traefik-manager)
GUI to keep everything in one proxy system and get the otel stuff for free. However, after presenting the UI for approvals
it was deemed too complicated and confusing to the people already using the existing system.

This project was the compromise. It hooks into traefik and does the things we needed it to do. This implementation is very
opinionated, we have a specific internal/external hosts layout that we wanted to maintain. It is optional to include both
types of ingress for those with simpler needs, but not actively tested. The timeout options were added when some of our
local ai responses went long compared to the defaults. Waiting for a local image or video generation can take a while.

## Visuals

The Proxy Hosts list, with each host's live Status and a Domain Clash flagged:

![The Proxy Hosts list](docs/screenshots/proxy-hosts.png)

Editing a Proxy Host whose domain a Docker-labelled route also serves:

![Editing a Proxy Host](docs/screenshots/edit-proxy-host.png)

## Installation

tpm runs as one more service in the compose project that already runs Traefik v3. It needs three things: the `tpm`
service, a `providers.http` entry in Traefik's static config, and the `TPM_*` settings.

### 1. Add the tpm service

Copy the `tpm` service from [`examples/compose.yml`](examples/compose.yml) into your compose project, merge its
`tpm-data` volume and `tpm` network into your project's top-level keys, and add `tpm` to your Traefik service's
networks. The example shows Proxy Login with Local Login as Fallback; for the other setups, see
[Signing in](#signing-in).

```yaml
# The tpm service, to copy into the compose project that runs Traefik. It
# shows Proxy Login with Local Login as Fallback; the README's Signing in
# section says what to delete for the other setups. Replace the example
# entrypoints, Certificate Resolvers, domains and Access rule with your own;
# see the README for every TPM_* setting.
services:
  tpm:
    image: ghcr.io/aspirational-cognition/traefik-proxy-manager:latest
    restart: unless-stopped
    networks:
      # Shared only with Traefik: Traefik polls tpm, and tpm reads Traefik's
      # API; nothing else may reach tpm's port.
      - tpm
    volumes:
      - tpm-data:/data
    environment:
      TPM_TRAEFIK_API_URL: http://traefik:8080
      TPM_ENTRYPOINT_INTERNAL_HTTP: web-int
      TPM_ENTRYPOINT_INTERNAL_HTTPS: websecure-int
      TPM_ENTRYPOINT_EXTERNAL_HTTP: web
      TPM_ENTRYPOINT_EXTERNAL_HTTPS: websecure
      TPM_CERT_RESOLVERS: le-dns,le-http
      TPM_DEFAULT_MIDDLEWARES_EXTERNAL: authentik@docker
      TPM_LOCAL_AUTH: form
      TPM_AUTH_USERNAME: admin
      TPM_AUTH_PASSWORD: ${TPM_AUTH_PASSWORD} # from .env, keep it out of this file
      TPM_PROXY_LOGIN_HOST: tpm.example.com   # must match the tpm router's Host
    labels:
      - traefik.enable=true
      - traefik.docker.network=tpm
      # Proxy Login: tpm's usual address, behind Authentik.
      - traefik.http.routers.tpm.rule=Host(`tpm.example.com`)
      - traefik.http.routers.tpm.entrypoints=websecure-int
      - traefik.http.routers.tpm.tls.certresolver=le-dns
      - traefik.http.routers.tpm.middlewares=authentik@docker
      # Fallback: Local Login for when Authentik is down. Internal entrypoints only.
      - traefik.http.routers.tpm-fallback.rule=Host(`tpm-fallback.example.com`)
      - traefik.http.routers.tpm-fallback.entrypoints=websecure-int
      - traefik.http.routers.tpm-fallback.tls.certresolver=le-dns
      - traefik.http.services.tpm.loadbalancer.server.port=8080

# Merge these into your project's own top-level keys.
volumes:
  tpm-data:

networks:
  tpm:
    name: tpm # add tpm to your Traefik service's networks too
```

> **Only Traefik may reach tpm's port.** Anything else that reaches it gets past Proxy Login, and can claim the Proxy
> Login hostname to skip Local Login too. Never publish tpm's port or put tpm on a network other containers share.

tpm publishes no ports and needs no `depends_on`: you reach its UI through Traefik, at the domains its labels give it.
Replace the example entrypoints, Certificate Resolvers, domains and Access rule with your own, and put
`TPM_AUTH_PASSWORD=…` in the `.env` file next to your compose file.

The image is published for `linux/amd64` and `linux/arm64`, tagged by release (`1.2.3`, `1.2`, `1`, `latest`) and
`dev` for the development branch.

### 2. Point Traefik at tpm

Add tpm as an HTTP provider in Traefik's static config (`traefik.yml`):

```yaml
providers:
  http:
    endpoint: http://tpm:8080/traefik/config
    pollInterval: 2s
```

Use these values as they are. Change the host or port only if your service isn't named `tpm` or you set `TPM_PORT`.
Traefik reads its static config only at startup, so restart Traefik after adding this.

`/traefik/config` answers only Traefik's own poll: it lists every Proxy Host's Forward Target, so it's a 404 to any
request that came through a Traefik router, including `curl https://tpm.example.com/traefik/config`. The endpoint must
stay `http://tpm:8080/…`. tpm tells the two apart by the `X-Forwarded-For`, `X-Forwarded-Proto`, `X-Forwarded-Port`,
`X-Forwarded-Server` and `X-Real-Ip` headers Traefik adds to every routed request, so a headers Middleware blanking
them, or a TCP router to tpm, opens it up. An `HTTP_PROXY` or `providers.http.headers` on Traefik that adds one of
them makes the poll 404, and Traefik keeps serving its last config. To view the config yourself:

```sh
docker compose exec traefik wget -qO- http://tpm:8080/traefik/config
```

### 3. Let tpm read Traefik's API

tpm reads Traefik's API to show each Proxy Host's Status, list Access rules and warn about Domain Clashes. The simplest
setup turns on Traefik's API on its built-in `traefik` entrypoint (port 8080):

```yaml
api:
  insecure: true
```

with `TPM_TRAEFIK_API_URL=http://traefik:8080`.

> **Never publish port 8080** on the Traefik service. `api.insecure` serves the API without authentication; it must
> stay reachable only from inside Docker's network.

Alternatively, route `api@internal` through a router with a basicauth Middleware and put the credentials in the URL,
e.g. `TPM_TRAEFIK_API_URL=https://admin:secret@traefik.example.com`.

If tpm can't reach the API, Proxy Hosts still save and Traefik still serves them; tpm just shows their Status as
Unknown.

### Bind mounts

tpm runs as uid 65532 (distroless `nonroot`). A named volume like `tpm-data` is writable out of the box. If you
bind-mount a host directory at `/data` instead, give it to that user first, or saving fails:

```sh
sudo chown 65532:65532 ./tpm-data
```

## Signing in

tpm supports four login setups. The example in [step 1](#1-add-the-tpm-service) shows the last one, Proxy Login with
Fallback; for the others, change the example as their subsection says. The four login settings are `TPM_LOCAL_AUTH`,
`TPM_AUTH_USERNAME`, `TPM_AUTH_PASSWORD` and `TPM_PROXY_LOGIN_HOST`.

### No login

tpm and Traefik ask for nothing. Delete the four login settings, the `tpm-fallback` router's labels and
the `tpm` router's `middlewares` label.

> **Anyone who can reach tpm's hostname can change your proxy.** Keep the `tpm` router on Internal entrypoints only,
> or add another Access rule to it.

### Local Login

tpm itself asks for `TPM_AUTH_USERNAME` and `TPM_AUTH_PASSWORD`. Delete `TPM_PROXY_LOGIN_HOST`, the `tpm-fallback`
router's labels and the `tpm` router's `middlewares` label, and set `TPM_LOCAL_AUTH` to the style you want:

- `basic`: the browser's own prompt. There's no Sign out.
- `form`: a tpm sign-in page. A session lasts 7 days. Sign out, in the bar at the top, ends every Form session, not
  just this browser's; so does a tpm restart.

> **Internal entrypoints only.** tpm doesn't limit sign-in attempts, so never put a router that asks for Local Login
> on External entrypoints.

### Proxy Login

An identity provider signs you in, consulted by Traefik before the request reaches tpm. Delete the four login settings
and the `tpm-fallback` router's labels. Proxy Login needs no tpm setting: the `tpm` router's `middlewares` label does
it all, and any forwardAuth Middleware works (Authentik, Authelia, oauth2-proxy).

If the identity provider or your internet link is down, so is your way in to tpm. Proxy Login with Fallback fixes that.

### Proxy Login with Fallback

The example as it is. tpm's usual address, `tpm.example.com`, goes through Proxy Login, and tpm skips Local Login
there because it's `TPM_PROXY_LOGIN_HOST`. The Fallback address, `tpm-fallback.example.com`, has no Access rule, so tpm
asks for Local Login instead.

When the identity provider or your internet link is down, Proxy Login can't let you in. Go to the Fallback address,
sign in with `TPM_AUTH_USERNAME` and `TPM_AUTH_PASSWORD`, and fix what you need. Once Proxy Login is back, go back to
tpm's usual address.

The `tpm` router must match exactly the one hostname in `TPM_PROXY_LOGIN_HOST`, never `Host(a) || Host(b)`. tpm asks
for Local Login on every other hostname, and the `tpm-fallback` router must never answer on that one, or it would skip
both logins.

> **Internal entrypoints only.** tpm doesn't limit sign-in attempts, so keep the `tpm-fallback` router off External
> entrypoints.

## Configuration

tpm is configured only through `TPM_*` environment variables. It checks them at startup and refuses to start,
saying what's wrong, if any are. For which login settings each setup needs, see [Signing in](#signing-in).

| Variable | Required | Default | Meaning |
| --- | --- | --- | --- |
| `TPM_TRAEFIK_API_URL` | yes | | Traefik's API, e.g. `http://traefik:8080`. Credentials, if any, go in the URL. |
| `TPM_ENTRYPOINT_INTERNAL_HTTP` | one side | | Traefik entrypoint for Internal plain HTTP, e.g. `web-int`. |
| `TPM_ENTRYPOINT_INTERNAL_HTTPS` | one side | | Traefik entrypoint for Internal HTTPS, e.g. `websecure-int`. |
| `TPM_ENTRYPOINT_EXTERNAL_HTTP` | one side | | Traefik entrypoint for External plain HTTP, e.g. `web`. |
| `TPM_ENTRYPOINT_EXTERNAL_HTTPS` | one side | | Traefik entrypoint for External HTTPS, e.g. `websecure`. |
| `TPM_CERT_RESOLVERS` | yes | | Comma-separated Certificate Resolvers from Traefik's static config. The first is the default for new Proxy Hosts. |
| `TPM_DEFAULT_MIDDLEWARES_INTERNAL` | no | none | Comma-separated Access rules ticked by default for Internal Proxy Hosts, fully qualified (`authentik@docker`). |
| `TPM_DEFAULT_MIDDLEWARES_EXTERNAL` | no | none | The same for External Proxy Hosts. Proxy Hosts exposed on Both get External's defaults. |
| `TPM_PORT` | no | `8080` | Port tpm listens on inside the container. If you change it, change the `providers.http` endpoint and the `loadbalancer.server.port` label to match. |
| `TPM_DATA_DIR` | no | `/data` | Where tpm keeps `proxy-hosts.json`. |
| `TPM_RESPONSE_HEADER_TIMEOUT` | no | none | How long Proxy Hosts with "Skip certificate check" and no [Response timeout](#response-timeout) of their own wait for the Forward Target's response headers before Traefik returns a 504: a positive duration, e.g. `30s`. Traefik doesn't apply its static `serversTransport.forwardingTimeouts.responseHeaderTimeout` to them, so if you set that, set this to the same value. |
| `TPM_LOCAL_AUTH` | no | none | Turns on Local Login: tpm itself asks for `TPM_AUTH_USERNAME` and `TPM_AUTH_PASSWORD` on every page. Exactly `basic` (the browser's own prompt) or `form` (a tpm sign-in page; a session lasts 7 days, and Sign out or a tpm restart ends every session); anything else refuses to start. Unset, tpm asks for nothing, and any protection is your Access rule on tpm's router. |
| `TPM_AUTH_USERNAME` | with `TPM_LOCAL_AUTH` | | The Local Login username, taken exactly as set (no trimming). Can't contain `:`. Setting it without `TPM_LOCAL_AUTH` refuses to start. |
| `TPM_AUTH_PASSWORD` | with `TPM_LOCAL_AUTH` | | The Local Login password, taken exactly as set (no trimming, no strength rule). Setting it without `TPM_LOCAL_AUTH` refuses to start. |
| `TPM_PROXY_LOGIN_HOST` | no | none | For Proxy Login with Local Login as Fallback: the one hostname of tpm's Proxy Login router, e.g. `tpm.example.com`, which must match that router's `Host` rule. Requests for it skip Local Login, since Traefik's forwardAuth has already let them through; every other hostname (the Fallback's, a bare IP, a catch-all) is asked for it. Matched against the request's `Host` only, ignoring case and port, never `X-Forwarded-Host`. A bare hostname only: no scheme, port, path, `@` or whitespace. Setting it without `TPM_LOCAL_AUTH` refuses to start; Proxy Login alone needs no tpm setting. |

### Exposure sides

Each Proxy Host's Exposure decides which Traefik entrypoints serve it. Internal and External are each a **side**, and
Both uses both sides:

- A side is a pair of entrypoints, HTTP and HTTPS. Set both of a side's variables or neither.
- At least one side is required.
- With only one side configured, the form hides the Exposure choice and new Proxy Hosts use that side.

### Certificate Resolvers

Traefik's API doesn't list Certificate Resolvers, so tpm can't check `TPM_CERT_RESOLVERS`. A typo there shows up only
in Traefik's logs.

tpm warns about domains with no certificate yet only on Traefik v3.7 or newer, which can list certificates. Older
Traefik v3 works fine without that warning.

### Response timeout

A Proxy Host's Response timeout is how long Traefik waits for the Forward Target's response headers before returning a
504. Use it for a Forward Target that takes minutes to answer. The value is a duration such as `10m`, or `0` for no
limit. A number with no unit, such as `30`, is refused.

Left empty, the Proxy Host keeps the default: Traefik's static `responseHeaderTimeout`, or `TPM_RESPONSE_HEADER_TIMEOUT`
with Skip certificate check.

Two things still limit a Proxy Host with a Response timeout:

- **The entrypoint's `writeTimeout` still applies.** A response can't outlast it, whatever the Response timeout says.
  Past that deadline the client gets no 504: HTTP/2 is reset at the deadline, and HTTP/1.1 is dropped with no response
  when the Forward Target finally answers.
- **Traefik's static `serversTransport` settings don't apply.** The Proxy Host gets a transport of its own, which doesn't
  use settings such as `rootCAs`. An https Forward Target that relies on them fails with a 500 unless Skip certificate
  check is on.

## Backup and restore

Everything tpm knows is in one file, `proxy-hosts.json`, in its `/data` volume. tpm replaces that file atomically on
every save, so copying it while tpm is running is safe.

Back up:

```sh
docker compose cp tpm:/data/proxy-hosts.json ./proxy-hosts.json
```

Restore: stop tpm, replace the file, start tpm.

```sh
docker compose stop tpm
docker compose cp ./proxy-hosts.json tpm:/data/proxy-hosts.json
docker compose start tpm
```

`docker compose cp` keeps the file's permissions, so make sure it's readable by tpm's user (e.g. `chmod 644`).

A restored file may carry a lower `nextId` than the Proxy Hosts it holds. That's fine: tpm never hands out an ID
already in the file.

## Contributing

I can't guarantee anything other than major bugs will be merged. We want the UI to stay simple and manageable
for the people here who use it.

## Donations?

Unlikely, but with a company called ~~Wishful Thinking~~ Aspirational Cognition, we may as well try:
Send some ETH to 0x8B69c14417F84366F302bE7E00Df4F67FD5742Ed

## License

[MIT](LICENSE)

