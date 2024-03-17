# Start from the official Golang image
FROM golang:1.22-alpine AS build

# hadolint ignore=DL3018
RUN apk add --no-cache \
  tzdata=2024a-r1 \
  zip=3.0-r12 \ 
  ca-certificates=20240226-r0 \
  && apk add --no-cache \
    zig --repository=https://dl-cdn.alpinelinux.org/alpine/edge/testing \
  && zip -r -0 /zoneinfo.zip /usr/share/zoneinfo

# Set the Current Working Directory inside the container
WORKDIR /app

# Copy go mod and sum files
COPY go.mod go.sum ./

# Download all dependencies. Dependencies will be cached if the go.mod and go.sum files are not changed
RUN go mod download

# Copy the source code from the current directory to the Working Directory inside the container
COPY . .

ENV CGO_ENABLED=1

RUN go test ./... \
  && CC="zig cc" CXX="zig c++" go build -ldflags='-s -w -extldflags "-static"' -o main .

####################

# Start a new stage from scratch
FROM scratch 

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
