# Explicit model-required issue #3 gate. Never substitutes a skip for inference.
FROM ubuntu:24.04 AS toolchain
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl strace time python3 \
    && rm -rf /var/lib/apt/lists/*
RUN curl -fsSL https://go.dev/dl/go1.27.3.linux-amd64.tar.gz -o /tmp/go.tar.gz \
    && echo '65efbc80fdc1fa2cd2631cad5b481777a68e8725f34e89f50fc6ac714e379b40  /tmp/go.tar.gz' | sha256sum -c - \
    && tar -xzf /tmp/go.tar.gz -C /usr/local && rm /tmp/go.tar.gz
ENV PATH="/usr/local/go/bin:${PATH}" CGO_ENABLED=0 GOTOOLCHAIN=local
WORKDIR /src

FROM toolchain AS model
# Explicit test-fixture acquisition during image BUILD only. No runtime downloader.
RUN curl --fail --location --silent --show-error --max-time 300 \
    https://huggingface.co/Qwen/Qwen2.5-Coder-0.5B-Instruct-GGUF/resolve/ebb2015119c907b064c512bf053e945850b5875f/qwen2.5-coder-0.5b-instruct-q4_k_m.gguf \
    -o /model.gguf \
    && echo '1d9614638d18024d0fbb36575a15f1302a3adf044df10345688ec4f6e1c4ff32  /model.gguf' | sha256sum -c -

# BuildKit can acquire the pinned fixture while the independent checks run.
# The required linux-cgo-free job runs the full clean ci/verify.sh gate once.
# Keep this image's toolchain audit and every amd64 CPU runtime check independent.
# ARM64 compile-only checks follow the common gate in ci/Dockerfile.
FROM toolchain AS checks
COPY . .
RUN for tool in cc gcc g++ c++ clang clang++ cmake; do \
      if command -v "$tool"; then exit 1; fi; done \
    && test ! -e /usr/include/sqlite3.h \
    && test ! -e /usr/local/include/sqlite3.h \
    && test "$(go env GOOS)/$(go env GOARCH)" = linux/amd64 \
    && test "$(go env GOVERSION)" = go1.27.3 \
    && test "$(go env CGO_ENABLED)" = 0 \
    && test "$(go env GOTOOLCHAIN)" = local \
    && python3 ci/test-long-context-supervisor.py \
    && test -z "$(gofmt -l tools/cpuspike)" \
    && go build -o /cpuspike ./tools/cpuspike \
    && go test -c -tags cpusmoke -o /cpu-wrapper-smoke.test ./internal/inference/goinfer \
    && go test -c -tags cpusmoke -o /w3-cpu-smoke.test ./internal/cli
COPY --from=model /model.gguf /model.gguf
ENTRYPOINT ["/usr/bin/time", "-v", "/cpuspike", "/model.gguf"]
