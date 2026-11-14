FROM golang:1.23-alpine AS build
WORKDIR /app
COPY go.mod ./
COPY . .
RUN go build -o /go-proxy .

FROM alpine:3.20
WORKDIR /app
COPY --from=build /go-proxy /app/go-proxy
COPY .env.example /app/.env.example
# .env dimount dari host, CACHE_DIR default ./cache
EXPOSE 8080
ENTRYPOINT ["/app/go-proxy"]
