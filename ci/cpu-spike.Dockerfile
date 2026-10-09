# Explicit model-required issue #3 gate. Never substitutes a skip for inference.
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl time python3 \
    && rm -rf /var/lib/apt/lists/*
RUN curl -fsSL https://go.dev/dl/go1.27.1.linux-amd64.tar.gz -o /tmp/go.tar.gz \
    && echo '63d339f0da5ab53635a56f2490a7984dfe12dfcff22ad749f63edaf590168445  /tmp/go.tar.gz' | sha256sum -c - \
    && tar -xzf /tmp/go.tar.gz -C /usr/local && rm /tmp/go.tar.gz
ENV PATH="/usr/local/go/bin:${PATH}" CGO_ENABLED=0 GOTOOLCHAIN=local
WORKDIR /src
COPY . .
RUN for tool in cc gcc g++ c++ clang clang++ cmake; do \
      if command -v "$tool"; then exit 1; fi; done \
    && python3 ci/test-long-context-supervisor.py \
    && sh ci/verify.sh \
    && test -z "$(gofmt -l tools/cpuspike)" \
    && go build -o /cpuspike ./tools/cpuspike \
    && GOOS=linux GOARCH=arm64 go build -o /cpuspike-arm64 ./tools/cpuspike
# Explicit test-fixture acquisition during image BUILD only. No runtime downloader.
RUN curl --fail --location --silent --show-error --max-time 300 \
    https://huggingface.co/Qwen/Qwen2.5-Coder-0.5B-Instruct-GGUF/resolve/ebb2015119c907b064c512bf053e945850b5875f/qwen2.5-coder-0.5b-instruct-q4_k_m.gguf \
    -o /model.gguf \
    && echo '1d9614638d18024d0fbb36575a15f1302a3adf044df10345688ec4f6e1c4ff32  /model.gguf' | sha256sum -c -
ENTRYPOINT ["/usr/bin/time", "-v", "/cpuspike", "/model.gguf"]
