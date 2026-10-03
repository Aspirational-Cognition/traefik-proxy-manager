# Traefik Proxy Manager (tpm)

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


## Badges

## Visuals

## Installation

## Usage

## Support

## Roadmap

## Contributing

## Authors and acknowledgment

## License
MIT

