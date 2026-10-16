FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ts .

FROM alpine:3.23
RUN apk add --no-cache ca-certificates && addgroup -S ts && adduser -S -G ts -u 10001 ts
COPY --from=build /out/ts /usr/local/bin/ts
USER ts
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/ts"]
CMD ["-config", "/config/config.toml"]
