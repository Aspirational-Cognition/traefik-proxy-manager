# Traefik Proxy Manager (tpm)

[![main pipeline](https://gitlab.example.com/home/devops/traefik-proxy-manager/badges/main/pipeline.svg?key_text=main)](https://gitlab.example.com/home/devops/traefik-proxy-manager/-/pipelines?ref=main)
[![dev pipeline](https://gitlab.example.com/home/devops/traefik-proxy-manager/badges/dev/pipeline.svg?key_text=dev)](https://gitlab.example.com/home/devops/traefik-proxy-manager/-/pipelines?ref=dev)

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


## Visuals

The Proxy Hosts list, with each host's live Status and a Domain Clash flagged:

![The Proxy Hosts list](docs/screenshots/proxy-hosts.png)

Editing a Proxy Host whose domain a Docker-labelled route also serves:

![Editing a Proxy Host](docs/screenshots/edit-proxy-host.png)

## Installation

tpm runs as one more service in the compose project that already runs Traefik v3. It needs three things: the `tpm`
service, a `providers.http` entry in Traefik's static config, and the `TPM_*` settings.

### 1. Add the tpm service

Copy the `tpm` service from [`examples/compose.yml`](examples/compose.yml) into your compose project, and merge its
`tpm-data` volume and Traefik network into your project's top-level keys:

```yaml
services:
  tpm:
    image: registry.example.com:443/home/devops/traefik-proxy-manager:latest
    restart: unless-stopped
    networks:
      - proxy # Traefik's network: Traefik polls tpm, and tpm reads Traefik's API
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
    # tpm's own UI, internal only and behind Authentik.
    labels:
      - traefik.enable=true
      - traefik.http.routers.tpm.rule=Host(`tpm.example.com`)
      - traefik.http.routers.tpm.entrypoints=websecure-int
      - traefik.http.routers.tpm.tls.certresolver=le-dns
      - traefik.http.routers.tpm.middlewares=authentik@docker
      - traefik.http.services.tpm.loadbalancer.server.port=8080
```

tpm publishes no ports and needs no `depends_on`: you reach its UI through Traefik, at the domain its labels give it.
Replace the example network, entrypoints, Certificate Resolvers, domain and Access rule with your own.

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

## Configuration

tpm is configured only through `TPM_*` environment variables. It checks them at startup and refuses to start,
saying what's wrong, if any are.

| Variable | Required | Default | Meaning |
| --- | --- | --- | --- |
| `TPM_TRAEFIK_API_URL` | yes | | Traefik's API, e.g. `http://traefik:8080`. Basic auth, if any, goes in the URL. |
| `TPM_ENTRYPOINT_INTERNAL_HTTP` | one side | | Traefik entrypoint for Internal plain HTTP, e.g. `web-int`. |
| `TPM_ENTRYPOINT_INTERNAL_HTTPS` | one side | | Traefik entrypoint for Internal HTTPS, e.g. `websecure-int`. |
| `TPM_ENTRYPOINT_EXTERNAL_HTTP` | one side | | Traefik entrypoint for External plain HTTP, e.g. `web`. |
| `TPM_ENTRYPOINT_EXTERNAL_HTTPS` | one side | | Traefik entrypoint for External HTTPS, e.g. `websecure`. |
| `TPM_CERT_RESOLVERS` | yes | | Comma-separated Certificate Resolvers from Traefik's static config. The first is the default for new Proxy Hosts. |
| `TPM_DEFAULT_MIDDLEWARES_INTERNAL` | no | none | Comma-separated Access rules ticked by default for Internal Proxy Hosts, fully qualified (`authentik@docker`). |
| `TPM_DEFAULT_MIDDLEWARES_EXTERNAL` | no | none | The same for External Proxy Hosts. Proxy Hosts exposed on Both get External's defaults. |
| `TPM_PORT` | no | `8080` | Port tpm listens on inside the container. If you change it, change the `providers.http` endpoint and the `loadbalancer.server.port` label to match. |
| `TPM_DATA_DIR` | no | `/data` | Where tpm keeps `proxy-hosts.json`. |
| `TPM_RESPONSE_HEADER_TIMEOUT` | no | none | How long Proxy Hosts with "Skip certificate check" wait for the Forward Target's response headers before Traefik returns a 504, e.g. `30s`. Traefik doesn't apply its static `serversTransport.forwardingTimeouts.responseHeaderTimeout` to them, so if you set that, set this to the same value. |
| `TPM_LOCAL_AUTH` | no | none | Turns on Local Login: tpm itself asks for `TPM_AUTH_USERNAME` and `TPM_AUTH_PASSWORD` on every page. Exactly `basic` (the browser's own prompt); anything else refuses to start. Unset, tpm asks for nothing, and any protection is your Access rule on tpm's router. |
| `TPM_AUTH_USERNAME` | with `TPM_LOCAL_AUTH` | | The Local Login username, taken exactly as set (no trimming). Can't contain `:`. Setting it without `TPM_LOCAL_AUTH` refuses to start. |
| `TPM_AUTH_PASSWORD` | with `TPM_LOCAL_AUTH` | | The Local Login password, taken exactly as set (no trimming, no strength rule). Setting it without `TPM_LOCAL_AUTH` refuses to start. |

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

## License

[MIT](LICENSE)

