#!/bin/sh

# Define function to gracefully stop services
stop_services() {
  echo "Stopping services..."
  # Stop the Go application gracefully
  kill -TERM "$gomain_pid" 2>/dev/null
  # Stop the Redis server gracefully
  redis-cli shutdown
  exit 0
}

# Trap SIGTERM signal to stop services gracefully
trap stop_services TERM

# Start Redis server
redis-server --daemonize yes --loglevel warning

# Wait until Redis is ready
while ! redis-cli ping &>/dev/null; do
  sleep 0.1
done

# Start Go application and redirect its stdout to the container's stdout
/gomain &
gomain_pid=$!

# Wait for SIGTERM signal to stop services gracefully
wait "$gomain_pid"
