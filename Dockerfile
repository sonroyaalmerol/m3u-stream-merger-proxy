# Start from the official Golang image
FROM golang:bookworm AS build

RUN apt-get update \
 && DEBIAN_FRONTEND=noninteractive \
    apt-get install --assume-yes --no-install-recommends \
      build-essential=12.9 \
      musl-tools=1.2.3-1 \
      redis-server=7.0.15-1~deb12u1

# Set the Current Working Directory inside the container
WORKDIR /app

# Copy go mod and sum files
COPY go.mod go.sum ./

# Download all dependencies. Dependencies will be cached if the go.mod and go.sum files are not changed
RUN go mod download

RUN redis-server --daemonize yes

# Copy the source code from the current directory to the Working Directory inside the container
COPY . .

RUN go test ./... \
  && CGO_ENABLED=1 CC=musl-gcc go build -ldflags='-s -w -extldflags "-static"' -o main .

####################

# Start a new stage from scratch
FROM scratch 

COPY --from=build /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /usr/local/go/lib/time/zoneinfo.zip /

ENV ZONEINFO=/zoneinfo.zip
ENV TZ=Etc/UTC

# Copy the built Go binary from the previous stage
COPY --from=build /app/main /gomain

# Expose ports for Go application and Redis
EXPOSE 8080

# Run the entrypoint script
CMD ["/gomain"]
