# Stage 1: Build Go binary
FROM golang:1.26-alpine AS build
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o dev-portal .

FROM oven/bun:1.3.13 AS bun

# Stage 2: Runtime — debian with bun for building spaces from source
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates unzip && \
    rm -rf /var/lib/apt/lists/*
COPY --from=bun /usr/local/bin/bun /usr/local/bin/bun
RUN mkdir -p /app/data/bundles /app/data/sources
WORKDIR /app
COPY --from=build /app/dev-portal .
ENV PORT=80
EXPOSE 80
CMD ["./dev-portal"]
