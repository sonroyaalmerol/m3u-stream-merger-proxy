# Start from the official Golang image
FROM golang:bookworm AS build

# Set the Current Working Directory inside the container
WORKDIR /app

# Copy go mod and sum files
COPY go.mod go.sum ./

# Download all dependencies. Dependencies will be cached if the go.mod and go.sum files are not changed
RUN go mod download

# Copy the source code from the current directory to the Working Directory inside the container
COPY . .

RUN CGO_ENABLED=0 go build -ldflags='-s -w' -o main . \
  && go test ./...

####################

# Start a new stage from scratch
FROM scratch 

COPY --from=build /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

# Copy the built Go binary from the previous stage
COPY --from=build /app/main /gomain

# Expose ports for Go application and Redis
EXPOSE 8080

# Run the entrypoint script
CMD ["/gomain"]
