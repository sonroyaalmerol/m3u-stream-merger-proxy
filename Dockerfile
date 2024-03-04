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

# Build the Go app
ENV CGO_ENABLED=1

# hadolint ignore=DL3018
RUN apk add --no-cache gcc musl-dev \
  && go build -ldflags='-s -w -extldflags "-static"' -o main .

# Run tests
RUN go test ./...

####################

# Start a new stage from scratch
FROM scratch 

# Copy the built Go binary from the previous stage
COPY --from=build /app/main /gomain

# Expose ports for Go application and Redis
EXPOSE 8080

# Run the entrypoint script
CMD ["/gomain"]
