FROM golang:1.22-alpine AS builder
WORKDIR /src
COPY go.mod .
COPY . .
RUN go build -ldflags="-s -w" -o /tmpstore ./cmd/main.go

FROM alpine:3.19
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=builder /tmpstore /app/tmpstore
COPY config.json /app/config.json

# Data volume for uploaded files
VOLUME ["/app/data"]

EXPOSE 8080

ENTRYPOINT ["/app/tmpstore", "-config", "/app/config.json"]
