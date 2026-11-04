FROM ubuntu:24.04
# Match the foundation image's test-only observer tools and pinned toolchain.
# Ubuntu 24.04 provides strace 6.8 with --kill-on-exit.
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates curl python3 strace \
    && rm -rf /var/lib/apt/lists/*
RUN curl -fsSL https://go.dev/dl/go1.27.3.linux-amd64.tar.gz -o /tmp/go.tar.gz \
    && echo '65efbc80fdc1fa2cd2631cad5b481777a68e8725f34e89f50fc6ac714e379b40  /tmp/go.tar.gz' | sha256sum -c - \
    && tar -xzf /tmp/go.tar.gz -C /usr/local && rm /tmp/go.tar.gz
ENV PATH="/usr/local/go/bin:${PATH}" CGO_ENABLED=0 GOTOOLCHAIN=local
WORKDIR /src
COPY . .
# Fail closed if this image ever gains native build prerequisites.
RUN for tool in cc gcc g++ c++ clang clang++ cmake; do \
      if command -v "$tool"; then exit 1; fi; done \
    && test ! -e /usr/include/sqlite3.h \
    && test ! -e /usr/local/include/sqlite3.h \
    && test "$(go env GOOS)/$(go env GOARCH)" = linux/amd64 \
    && test "$(go env GOVERSION)" = go1.27.3
# The required foundation job runs all of ci/verify.sh. This image only prepares
# the publication job's module/build cache; it does not claim a behavioral PASS.
# That job copies GOPATH/pkg and GOCACHE into its writable non-root evidence
# directory, then rebuilds and runs every probe with --network none. Warm each
# package/tag/architecture combination it uses without running tests as root.
RUN set -eu; \
    warm=$(mktemp -d); \
    mkdir -p "$warm/normal" "$warm/pipelinefaults"; \
    go test -c -o "$warm/normal/" \
      ./internal/cli ./internal/artifact ./internal/publication; \
    go test -c -o "$warm/research-publication.test" ./research/publication; \
    go test -c -tags pipelinefaults -o "$warm/pipelinefaults/" \
      ./internal/localize ./internal/publication ./internal/cli ./internal/tm; \
    go test -c -tags offlinesmoke,pipelinefaults -o "$warm/offline-cli.test" ./internal/cli; \
    GOOS=linux GOARCH=arm64 go test -c -o "$warm/research-publication-arm64.test" ./research/publication; \
    GOOS=linux GOARCH=arm64 go test -c -o "$warm/product-publication-arm64.test" ./internal/publication; \
    rm -rf "$warm"
