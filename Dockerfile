# Start from the official Golang image
FROM golang:1.22-alpine AS build

# hadolint ignore=DL3018
RUN apk add --no-cache \
  tzdata \
  zip \
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
ENV CC="zig cc"
ENV CXX="zig c++"

###################

FROM build AS amd64-build

RUN GOARCH=amd64 CC='zig cc -target x86_64-linux' CXX='zig c++ -target x86_64-linux' \
  go build -ldflags='-s -w -extldflags "-static"' -o main .

###################

# hadolint ignore=DL3029
FROM --platform=arm64 build AS arm64-build

RUN GOARCH=arm64 CC='zig cc -target aarch64-linux' CXX='zig c++ -target aarch64-linux' \
  go build -ldflags='-s -w -extldflags "-static"' -o main .

####################

ARG TARGETARCH

# hadolint ignore=DL3006
FROM ${TARGETARCH}-build as arch-build-stage

FROM scratch 

COPY --from=arch-build-stage /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=arch-build-stage /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=arch-build-stage /zoneinfo.zip /

ENV ZONEINFO=/zoneinfo.zip
ENV TZ=Etc/UTC

# Copy the built Go binary from the previous stage
COPY --from=arch-build-stage /app/main /gomain

EXPOSE 8080

# Run the entrypoint script
CMD ["/gomain"]
