#!/bin/sh

# Add a group with the specified PGID if it doesn't already exist
if ! getent group appgroup > /dev/null 2>&1; then
    addgroup -g "${PGID}" appgroup
fi

# Add a user with the specified PUID if it doesn't already exist
if ! id -u appuser > /dev/null 2>&1; then
    adduser -D -u "${PUID}" -G appgroup appuser
fi

# Change ownership of the app directory
chown -R appuser:appgroup /m3u-proxy

# Switch to the new user and execute the main application
exec su-exec appuser "$@"

