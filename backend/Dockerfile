# syntax=docker/dockerfile:1
FROM golang:1.27-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/worker ./cmd/worker

FROM debian:bookworm-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends ffmpeg ca-certificates \
 && rm -rf /var/lib/apt/lists/* \
 && useradd --system --uid 10001 --home-dir /app phoebe \
 && mkdir -p /data/storage \
 && chown phoebe:phoebe /data/storage \
 && chmod 0700 /data/storage
COPY --from=build /out/api /out/worker /app/
USER phoebe
ENV STORAGE_DIR=/data/storage
EXPOSE 8080
CMD ["/app/api"]
