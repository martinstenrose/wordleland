# One image. The server, the Signal bridge and the admin CLI are one binary,
# so there is one thing to build, tag and publish.

# Must match or exceed the go directive in go.mod, which the golang.org/x
# dependencies keep current. A mismatch fails at "go mod download" with a
# clear message, but only when the image is actually built — so build it.
FROM --platform=$BUILDPLATFORM golang:1.27-bookworm AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Set by buildx for the platform being produced. Go cross-compiles natively,
# so a multi-arch build is a second compile rather than an emulated build.
ARG TARGETOS
ARG TARGETARCH

# What this build is, for the diagnostics page to report. Go stamps VCS
# information into a binary by itself, but not here: .dockerignore excludes
# .git, so the build context has no repository to read. Passing the values
# in is the alternative to shipping the git history into every build.
#
# Defaults match the ones in internal/version, so an image built by hand
# without these says "dev" rather than an empty string.
ARG VERSION=dev
ARG COMMIT=

# CGO off because modernc.org/sqlite is pure Go, which is the whole reason
# it was chosen: a static binary needs no C toolchain in the final image.
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath \
      -ldflags="-s -w \
        -X github.com/martinstenrose/wordleland/internal/version.Version=${VERSION} \
        -X github.com/martinstenrose/wordleland/internal/version.Commit=${COMMIT}" \
      -o /out/wordleland ./cmd/wordleland

# An empty /data owned by the runtime user.
#
# Docker seeds a fresh named volume from whatever is at the mount point in
# the image, ownership included. Without this the volume is created owned by
# root, the nonroot user cannot write to it, and the first start fails with
# "unable to open database file (14)" — which says nothing about
# permissions. The base image has no shell, so the directory is built here
# and copied in rather than created with mkdir at runtime.
RUN mkdir /data

# One binary: `wordleland serve` runs the server and, when Signal is
# configured, the bridge. The same binary carries the admin verbs, reachable
# as `docker compose exec app /wordleland user create ...` — which works on
# a shell-less base because exec with a full path needs no shell.
FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=build /out/wordleland /wordleland
COPY --from=build --chown=nonroot:nonroot /data /data
USER nonroot:nonroot
EXPOSE 8080
# Exec form and the binary itself: the base image has no shell to run a
# command string with, and no curl to run it against.
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD ["/wordleland", "serve", "--healthcheck"]
ENTRYPOINT ["/wordleland"]
CMD ["serve"]
