# Build:
#   docker build -t guestbooks .
#
# Run (mount your config.yaml and a data volume for the SQLite DB):
#   docker run -d --name guestbooks \
#     -p 6235:6235 \
#     -v $(pwd)/config.yaml:/app/config.yaml:ro \
#     -v guestbooks-data:/app/data \
#     guestbooks
#
# Note: gorm.io/driver/sqlite uses CGO, so we build with CGO enabled on
# alpine and ship libc + sqlite-libs in the runtime image.

# ---- build stage ----
FROM golang:1.25-alpine AS build

RUN apk add --no-cache build-base sqlite-dev

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=1 go build -tags release -o /out/guestbooks .

# ---- runtime stage ----
FROM alpine:3.20

RUN apk add --no-cache ca-certificates sqlite-libs tzdata && \
    adduser -D -u 10001 guestbooks

WORKDIR /app
COPY --from=build /out/guestbooks /app/guestbooks
COPY assets ./assets
COPY templates ./templates

# Data volume for the SQLite database. The app writes guestbook.db* to
# /app/data; symlink it into the working directory so the existing relative
# paths keep working.
RUN mkdir -p /app/data && \
    ln -s /app/data/guestbook.db /app/guestbook.db && \
    chown -R guestbooks:guestbooks /app && \
    chown -h guestbooks:guestbooks /app/guestbook.db

VOLUME ["/app/data"]
EXPOSE 6235

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD wget -q --spider http://localhost:6235/ || exit 1

USER guestbooks
CMD ["./guestbooks"]
