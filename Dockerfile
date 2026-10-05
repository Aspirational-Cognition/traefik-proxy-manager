# The build stage runs on the build platform: Tailwind's standalone CLI is
# native there, and Go cross-compiles to the target.
FROM --platform=$BUILDPLATFORM golang:1.27-bookworm AS build
WORKDIR /src
COPY Makefile .
RUN make bin/tailwindcss
COPY . .
ARG TARGETOS TARGETARCH
RUN GOOS=$TARGETOS GOARCH=$TARGETARCH make build && mkdir /data

# Only the binary, run as nonroot (uid 65532), plus an empty /data it owns
# for a fresh named volume to inherit.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /src/bin/tpm /tpm
COPY --from=build --chown=65532:65532 /data /data
VOLUME /data
EXPOSE 8080
ENTRYPOINT ["/tpm"]
