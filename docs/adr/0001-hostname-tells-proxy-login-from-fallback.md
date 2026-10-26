# Hostname, not headers, tells Proxy Login from Fallback

With Proxy Login and a Local Login Fallback, tpm lets a request skip Local Login only when its `Host` is the one Proxy Login hostname it is configured with (`TPM_PROXY_LOGIN_HOST`); every other hostname, including the Fallback's, is asked for Local Login. Traefik only routes a request to the Proxy Login router, and so through forwardAuth, when its `Host` matches, and passes that `Host` on unchanged, so a client coming through Traefik can't fake it, and an unforeseen hostname fails closed.

## Considered Options

- **Trust the identity provider's header** (`X-authentik-username`, `Remote-User`, …): the Fallback router has no forwardAuth, so a client's forged copy reaches tpm untouched unless every operator remembers a stripping middleware there; forgetting it is a full bypass.
- **A fixed secret header added after forwardAuth**: as safe through Traefik, but the secret sits in plain text in labels and Traefik's API, which the same Docker network can read, and it is one more thing to generate.
- **Configure the Fallback hostname instead**: fails open for any hostname nobody foresaw.

## Consequences

Anything that reaches tpm's port without going through Traefik can send the Proxy Login hostname and skip Local Login. tpm doesn't defend against this; the README and example compose put tpm on a network only Traefik shares.
