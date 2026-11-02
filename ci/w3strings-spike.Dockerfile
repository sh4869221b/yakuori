# Research-only independent oracle, never a Yakuori dependency or release artifact.
FROM rust:1.90.0-bookworm AS oracle
WORKDIR /oracle
COPY tools/w3spike/oracle/Cargo.toml tools/w3spike/oracle/Cargo.lock ./
COPY tools/w3spike/oracle/src ./src
RUN cargo build --locked

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl \
    && rm -rf /var/lib/apt/lists/*
RUN curl -fsSL https://go.dev/dl/go1.27.3.linux-amd64.tar.gz -o /tmp/go.tar.gz \
    && echo '65efbc80fdc1fa2cd2631cad5b481777a68e8725f34e89f50fc6ac714e379b40  /tmp/go.tar.gz' | sha256sum -c - \
    && tar -xzf /tmp/go.tar.gz -C /usr/local && rm /tmp/go.tar.gz
COPY --from=oracle /oracle/target/debug/yakuori-w3strings-oracle /usr/local/bin/w3strings-oracle
ENV PATH="/usr/local/go/bin:${PATH}" CGO_ENABLED=0 GOTOOLCHAIN=local W3STRINGS_ORACLE=/usr/local/bin/w3strings-oracle
WORKDIR /src
COPY . .
RUN sh ci/w3strings-spike.sh && sh ci/w3strings-adapter.sh
