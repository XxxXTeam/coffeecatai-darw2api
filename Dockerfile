FROM golang:1.23-alpine AS builder

RUN apk add --no-cache git ca-certificates tzdata

WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags="-s -w" \
    -o /out/coff .
FROM python:3.12-alpine
RUN apk add --no-cache ca-certificates tzdata chromium curl
RUN curl -LsSf https://astral.sh/uv/install.sh | sh && \
    ln -s /root/.local/bin/uv /usr/local/bin/uv
WORKDIR /app
COPY solver/* /app/solver/
RUN cd /app/solver && uv sync
COPY --from=builder /out/coff /app/coff

EXPOSE 8080
ENTRYPOINT ["/app/coff"]
