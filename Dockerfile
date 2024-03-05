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

RUN go build -ldflags='-s -w' -o main . \
  && go test ./...

####################

# Start a new stage from scratch
FROM alpine:3.19.1 

# Install a specific version of CA certificates
RUN apk --no-cache add ca-certificates=20240226-r0

# Copy the built Go binary from the previous stage
COPY --from=build /app/main /gomain

# Expose ports for Go application and Redis
EXPOSE 8080

# Run the entrypoint script
CMD ["/gomain"]
