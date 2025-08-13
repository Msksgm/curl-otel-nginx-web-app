# ---- Build stage ----
FROM golang:1.22 AS builder
WORKDIR /src

COPY app/go.mod app/go.sum ./
RUN go mod download

COPY app/ .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /bin/app

# ---- Runtime stage ----
FROM alpine:3.20
# Add non-root user
RUN adduser -D -H appuser
COPY --from=builder /bin/app /app
USER appuser
EXPOSE 8080
ENTRYPOINT ["/app"]
