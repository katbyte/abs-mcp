# build with the vendored deps, then ship the static binary on alpine: small, but keeps a shell
# so you can `docker exec -it abs-mcp sh` to poke at things. runs as a non-root user.
# make docker passes VERSION/COMMIT from git; a bare `docker build .` reports "dev".
#
# REGISTRY is where the two base images come from: Google's mirror of Docker Hub, which holds
# the same images, unless a build says otherwise. Docker Hub itself (docker.io/library) limits
# how much an address may pull, which a CI runner's shared address runs into; CI builds again
# from it when the mirror gives nothing. Nothing here needs a newer Dockerfile syntax than every
# builder has, so no syntax image is named: that was a third thing to fetch before a build.
ARG GO_VERSION=1.27
ARG ALPINE_VERSION=3.24
ARG REGISTRY=mirror.gcr.io/library

FROM ${REGISTRY}/golang:${GO_VERSION}-alpine AS build
ARG VERSION=dev
ARG COMMIT=unknown
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -mod=vendor \
      -ldflags "-s -w \
        -X github.com/katbyte/go-kt/version.Version=${VERSION} \
        -X github.com/katbyte/go-kt/version.GitCommit=${COMMIT}" \
      -o /abs-mcp .

FROM ${REGISTRY}/alpine:${ALPINE_VERSION}
# ffmpeg is how item_compare_audio reads a book's audio: where the library is
# local, as in a container beside it, is where that is cheapest to run
RUN apk add --no-cache ca-certificates tzdata ffmpeg \
    && adduser -D -H -u 65532 abs
COPY --from=build /abs-mcp /usr/local/bin/abs-mcp
USER abs
# HTTP transport by default in the container; set ABS_SERVER and ABS_TOKEN at runtime,
# ABS_AUTH_TOKEN to protect the endpoint. the healthcheck assumes the default port.
ENV ABS_LISTEN=:8080
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s \
  CMD wget -q --spider http://127.0.0.1:8080/healthz || exit 1
ENTRYPOINT ["abs-mcp"]
CMD ["serve"]
