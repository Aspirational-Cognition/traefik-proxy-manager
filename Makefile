# Tailwind's standalone CLI builds the embedded CSS; no Node needed.
TAILWIND_VERSION := v4.3.3
TAILWIND_OS := $(subst Darwin,macos,$(subst Linux,linux,$(shell uname -s)))
TAILWIND_ARCH := $(subst aarch64,arm64,$(subst x86_64,x64,$(shell uname -m)))
WEB := internal/app/web

.PHONY: build css test
build: css
	CGO_ENABLED=0 go build -o bin/tpm ./cmd/tpm

css: bin/tailwindcss
	bin/tailwindcss -i $(WEB)/tailwind.css -o $(WEB)/static/app.css --minify

test: css
	go test ./...

bin/tailwindcss:
	mkdir -p bin
	curl -fsSLo $@ https://github.com/tailwindlabs/tailwindcss/releases/download/$(TAILWIND_VERSION)/tailwindcss-$(TAILWIND_OS)-$(TAILWIND_ARCH)
	chmod +x $@
