FROM golang:1.26-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -buildid=" -o /out/postgres-s3-backup ./cmd/postgres-s3-backup

FROM alpine:3.23
RUN apk add --no-cache postgresql18-client \
    && addgroup -S backup \
    && adduser -S -G backup backup
COPY --from=builder /out/postgres-s3-backup /usr/local/bin/postgres-s3-backup
USER backup
STOPSIGNAL SIGTERM
ENTRYPOINT ["postgres-s3-backup"]
