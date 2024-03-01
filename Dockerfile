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
RUN go build -o main .

# Run tests
RUN go test ./...

####################

FROM scratch

WORKDIR /

COPY --from=build /app/main /gomain

EXPOSE 8080

ENTRYPOINT ["/gomain"]


