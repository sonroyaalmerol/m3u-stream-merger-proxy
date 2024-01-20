# M3U Stream Merger Proxy

This service provides a simple HTTP proxy server for merging and streaming MP4 content from multiple M3U playlists. It acts as a proxy, consolidating streams from different M3U sources, effectively serving as a load balancer. The service is dockerized for easy deployment, and it periodically updates its M3U playlists to ensure the latest streams are available.

## Use Case: Consolidating Multiple IPTV Connections

If your IPTV provider issues multiple M3U links for each connection, but you prefer to consolidate them into a single URL for convenience, the M3U Stream Merger Proxy can help. Follow the steps below to simplify your IPTV setup:

### Prerequisites

- [Docker](https://www.docker.com/) installed on your system.

### Docker Compose

Use the provided `docker-compose.yml` file to deploy the M3U Stream Merger Proxy:

```yaml
version: '3'

services:
  m3u-stream-merger-proxy:
    image: registry.snry.xyz/sysadmin/m3u-stream-merger:main-latest
    ports:
      - "8080:8080"
    volumes:
      - ./data:/app/data
    environment:
      - M3U_URL_1=https://iptvprovider1.com/playlist.m3u
      - M3U_URL_2=https://iptvprovider2.com/playlist.m3u
    restart: always
```

1. Create a `.env` file with the M3U URLs as mentioned above.

2. Run the following command:

   ```bash
   docker-compose up -d
   ```

This will start the M3U Stream Merger Proxy, and you can access it at `http://localhost:8080`.

### Configuration

- **M3U_URL_1, M3U_URL_2**: Set the M3U URLs as environment variables in the `.env` file.

- **UPDATE_INTERVAL**: (Optional) Set the update interval in hours. Default is 24 hours.

### Endpoints

- `/playlist.m3u`: Get the merged M3U playlist.

- `/stream/{streamID}.mp4`: Stream the MP4 content for the specified stream ID.

### Data Persistence

The service uses a volume (`./data`) to persist M3U files. You can customize this volume path in the `docker-compose.yml` file if needed.

Feel free to explore and modify the service according to your requirements! The M3U Stream Merger Proxy essentially acts as a proxy and load balancer between the provided M3U sources.