# Start from the official Golang image
FROM golang:1.22-alpine AS build

# hadolint ignore=DL3018
RUN apk add --no-cache \
  tzdata \
  zip \
  musl-dev \
  ca-certificates \
  && apk add --no-cache \
    zig --repository=https://dl-cdn.alpinelinux.org/alpine/edge/testing \
  && zip -q -r -0 /zoneinfo.zip /usr/share/zoneinfo

# Set the Current Working Directory inside the container
WORKDIR /app

# Copy go mod and sum files
COPY go.mod go.sum ./

# Download all dependencies. Dependencies will be cached if the go.mod and go.sum files are not changed
RUN go mod download

# Copy the source code from the current directory to the Working Directory inside the container
COPY . .

ENV CGO_ENABLED=1
ENV GOOS=linux

RUN GOARCH=amd64 CC='zig cc' CXX='zig c++' go test ./... \
  && GOARCH=amd64 CC='zig cc' CXX='zig c++' go build -ldflags='-s -w -extldflags "-static"' -o main .

RUN GOARCH=arm64 CC='zig cc -target aarch64-linux-musl' CXX='zig c++ -target aarch64-linux-musl' \
  go build -ldflags='-s -w -extldflags "-static"' -o main-arm64 .

####################

# Start a new stage from scratch
FROM scratch AS amd64-release

COPY --from=build /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /zoneinfo.zip /

ENV ZONEINFO=/zoneinfo.zip
ENV TZ=Etc/UTC

# Copy the built Go binary from the previous stage
COPY --from=build /app/main /gomain

# Expose ports for Go application and Redis
EXPOSE 8080

# Run the entrypoint script
CMD ["/gomain"]

# Start a new stage for arm64 
# hadolint ignore=DL3029
FROM --platform=arm64 scratch AS arm64-release

COPY --from=build /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /zoneinfo.zip /

ENV ZONEINFO=/zoneinfo.zip
ENV TZ=Etc/UTC

# Copy the built Go binary from the previous stage
COPY --from=build /app/main-arm64 /gomain

# Expose ports for Go application and Redis
EXPOSE 8080

# Run the entrypoint script
CMD ["/gomain"]

###################

ARG TARGETARCH

# hadolint ignore=DL3006
FROM ${TARGETARCH}-release
