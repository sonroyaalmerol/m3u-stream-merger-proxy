#!/bin/sh

# Start Redis server
redis-server --daemonize yes

# Wait until Redis is ready
while ! redis-cli ping &>/dev/null; do
  sleep 0.1
done

# Start Go application and redirect its stdout to the container's stdout
/gomain
