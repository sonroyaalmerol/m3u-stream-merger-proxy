#!/usr/bin/bash

# add the abc user if it does not already exist
id -u abc &>/dev/null || useradd -u 911 -U -d /config -s /bin/false abc && usermod -G users abc

# Verify FFmpeg and FFprobe installations
echo "Verifying FFmpeg and FFprobe installations..."
if [ ! -f "$FFMPEG_PATH" ]; then
    echo "FFmpeg not found at $FFMPEG_PATH!"
    exit 1
fi

if [ ! -f "$FFPROBE_PATH" ]; then
    echo "FFprobe not found at $FFPROBE_PATH!"
#    exit 1
fi

# Verify FFmpeg and FFprobe permissions
if [ ! -x "$FFMPEG_PATH" ]; then
    echo "FFmpeg is not executable at $FFMPEG_PATH!"
    exit 1
fi

if [ ! -x "$FFPROBE_PATH" ]; then
    echo "FFprobe is not executable at $FFPROBE_PATH!"
#    exit 1
fi

echo "FFmpeg version:"
"$FFMPEG_PATH" -version

alias ffmpeg=$FFMPEG_PATH
alias ffprobe=$FFPROBE_PATH

## hardware support ##
FILES=$(find /dev/dri /dev/dvb /dev/snd -type c -print 2>/dev/null)

for i in $FILES
do
    VIDEO_GID=$(stat -c '%g' "${i}")
    VIDEO_UID=$(stat -c '%u' "${i}")
    # check if user matches device
    if id -u abc | grep -qw "${VIDEO_UID}"; then
        echo "**** permissions for ${i} are good ****"
    else
        # check if group matches and that device has group rw
        if id -G abc | grep -qw "${VIDEO_GID}" && [ $(stat -c '%A' "${i}" | cut -b 5,6) = "rw" ]; then
            echo "**** permissions for ${i} are good ****"
        # check if device needs to be added to video group
        elif ! id -G abc | grep -qw "${VIDEO_GID}"; then
            # check if video group needs to be created
            VIDEO_NAME=$(getent group "${VIDEO_GID}" | awk -F: '{print $1}')
            if [ -z "${VIDEO_NAME}" ]; then
                VIDEO_NAME="video$(head /dev/urandom | tr -dc 'a-z0-9' | head -c4)"
                groupadd "${VIDEO_NAME}"
                groupmod -g "${VIDEO_GID}" "${VIDEO_NAME}"
                echo "**** creating video group ${VIDEO_NAME} with id ${VIDEO_GID} ****"
            fi
            echo "**** adding ${i} to video group ${VIDEO_NAME} with id ${VIDEO_GID} ****"
            usermod -a -G "${VIDEO_NAME}" abc
        fi
        # check if device has group rw
        if [ $(stat -c '%A' "${i}" | cut -b 5,6) != "rw" ]; then
            echo -e "**** The device ${i} does not have group read/write permissions, attempting to fix inside the container. ****"
            chmod g+rw "${i}"
        fi
    fi
done

# Change ownership of the app directory
chown -R abc:abc /m3u-proxy

# Switch to the new user and execute the main application
"$@"
