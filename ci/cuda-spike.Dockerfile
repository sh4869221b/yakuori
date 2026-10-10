# Evaluation binary only: no model download or CUDA toolkit.
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl \
    && rm -rf /var/lib/apt/lists/*
RUN curl -fsSL https://go.dev/dl/go1.27.1.linux-amd64.tar.gz -o /tmp/go.tar.gz \
    && echo '63d339f0da5ab53635a56f2490a7984dfe12dfcff22ad749f63edaf590168445  /tmp/go.tar.gz' | sha256sum -c - \
    && tar -xzf /tmp/go.tar.gz -C /usr/local && rm /tmp/go.tar.gz
ENV PATH="/usr/local/go/bin:${PATH}" CGO_ENABLED=0 GOTOOLCHAIN=local
WORKDIR /src
COPY . .
RUN for tool in cc gcc g++ c++ clang clang++ cmake nvcc; do \
      if command -v "$tool"; then exit 1; fi; done \
    && test "$(go env GOVERSION)" = go1.27.1 \
    && go test -c -tags 'cuda,cudasmoke' -o /cuda-evaluation.test ./internal/inference/goinfer
