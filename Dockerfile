# Start from the official Golang image
FROM golang:alpine AS build

# Set the Current Working Directory inside the container
WORKDIR /app

# Copy go mod and sum files
COPY go.mod go.sum ./

# Download all dependencies. Dependencies will be cached if the go.mod and go.sum files are not changed
RUN go mod download

# Copy the source code from the current directory to the Working Directory inside the container
COPY . .

# fire up redis server and test and build the app.
# hadolint ignore=DL3018
RUN \
  if [ "$(uname -m)" = "x86_64" ]; then \
    apk --no-cache add redis && \
    sed -i "s/bind .*/bind 127.0.0.1/g" /etc/redis.conf && \
    redis-server --daemonize yes && \
    go test ./...; \
  fi && \
  go build -ldflags='-s -w' -o m3u-proxy .

# End from the latest alpine image
# hadolint ignore=DL3007
FROM alpine:latest

# add bash and timezone data
# hadolint ignore=DL3018
RUN apk --no-cache add tzdata \
  ca-certificates \
  su-exec \
  && update-ca-certificates \

# set the current workdir
WORKDIR /m3u-proxy

# copy in our compiled GO app
COPY --from=build /app/m3u-proxy /m3u-proxy/

# Copy the entrypoint script
COPY entrypoint.sh /m3u-proxy/entrypoint.sh

# Make the entrypoint script executable
RUN chmod +x /m3u-proxy/entrypoint.sh

# Set PUID and PGID as environment variables
ENV PUID=1000
ENV PGID=1000

# The container entrypoint
ENTRYPOINT ["/m3u-proxy/entrypoint.sh", "/m3u-proxy/m3u-proxy"]
