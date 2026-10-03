# Research-only independent oracle, never a Yakuori dependency or release artifact.
FROM rust:1.90.0-bookworm AS oracle
WORKDIR /oracle
COPY tools/w3spike/oracle/Cargo.toml tools/w3spike/oracle/Cargo.lock ./
COPY tools/w3spike/oracle/src ./src
RUN cargo build --locked

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl \
    && rm -rf /var/lib/apt/lists/*
RUN curl -fsSL https://go.dev/dl/go1.27.1.linux-amd64.tar.gz -o /tmp/go.tar.gz \
    && echo '63d339f0da5ab53635a56f2490a7984dfe12dfcff22ad749f63edaf590168445  /tmp/go.tar.gz' | sha256sum -c - \
    && tar -xzf /tmp/go.tar.gz -C /usr/local && rm /tmp/go.tar.gz
COPY --from=oracle /oracle/target/debug/yakuori-w3strings-oracle /usr/local/bin/w3strings-oracle
ENV PATH="/usr/local/go/bin:${PATH}" CGO_ENABLED=0 GOTOOLCHAIN=local W3STRINGS_ORACLE=/usr/local/bin/w3strings-oracle
WORKDIR /src
COPY . .
RUN sh ci/w3strings-spike.sh
