# Use the official Go image as the base image
FROM golang:1.21.5-alpine

# Set the working directory inside the container
WORKDIR /app

# Copy the local package files to the container's workspace
COPY . .

# Download and install any dependencies
RUN go mod download

# Build the Go application
RUN go build -o main .

EXPOSE 8080

# Command to run the executable
ENTRYPOINT ["./main"]