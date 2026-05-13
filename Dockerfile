FROM golang:1.25 AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /out/publisher ./cmd/publisher

FROM debian:bookworm-slim

RUN apt-get update \
  && apt-get install -y --no-install-recommends ca-certificates \
  && rm -rf /var/lib/apt/lists/* \
  && useradd --system --create-home --uid 10001 app

WORKDIR /app

COPY --from=builder /out/publisher /usr/local/bin/publisher

USER app

ENTRYPOINT ["publisher"]
CMD ["run"]
