# 📡 M3U Stream Merger Proxy

Streamline your IPTV experience by consolidating multiple M3U playlists into a single source with the M3U Stream Merger Proxy. This service acts as a modern HTTP proxy server, effortlessly merging and streaming MP4 content from various M3U sources. Dockerized for seamless deployment, it ensures the latest streams are always available.

## Simplify Your IPTV Setup

If you're tired of managing multiple IPTV connections with separate M3U links, this proxy is the solution you need. Follow the steps below to simplify your IPTV setup:

## How It Works

1. **Initialization and M3U Playlist Consolidation:**
   - On startup, the service loads M3U playlists from specified URLs, consolidating streams into `/playlist.m3u`.
   - The consolidation process merges streams based on their names and saves them in a database.
   - Each unique stream name aggregates corresponding URLs into the consolidated playlist.

2. **HTTP Endpoints:**
   - **Playlist Endpoint (`/playlist.m3u`):**
     - Access the merged M3U playlist containing streams from different sources.

   - **Stream Endpoint (`/stream/{streamID}.mp4`):**
     - Request MP4 streams for specific stream IDs.

3. **Load Balancing:**
   - The service employs load balancing by cycling through available stream URLs.
   - Users can set max concurrency per stream URLs for optimized performance.

4. **Periodic Updates:**
   - Refreshes M3U playlists at specified intervals (cron schedule syntax) to ensure up-to-date stream information.
   - Updates run in the background with no downtime.

5. **Error Handling:**
   - Logs errors if all stream URLs fail to provide valid MP4 streams.
   - Handles client disconnections during streaming to conserve resources.

6. **Proxy Functionality:**
   - Abstracts complexity for clients, allowing interaction with a single endpoint.
   - Aggregates streams behind the scenes for a seamless user experience.

7. **Customization:**
   - Modify M3U URLs, update intervals, and other configurations in the `.env` file.

### Prerequisites

- [Docker](https://www.docker.com/) installed on your system.

### Docker Compose

Deploy with ease using the provided `docker-compose.yml`:

```yaml
version: '3'

services:
  m3u-stream-merger-proxy:
    image: sonroyaalmerol/m3u-stream-merger-proxy:latest
    ports:
      - "8080:8080"
    volumes:
      - ./data:/app/data
    environment:
      - M3U_URL_1=https://iptvprovider1.com/playlist.m3u
      - M3U_URL_2=https://iptvprovider2.com/playlist.m3u
      - M3U_URL_X=
    restart: always
```

1. Create a `.env` file with the M3U URLs as mentioned above.

2. Run:

   ```bash
   docker-compose up -d
   ```

Access the proxy at `http://localhost:8080`.

### Configuration

- **M3U_URL_1, M3U_URL_2, M3U_URL_X**: Set M3U URLs as environment variables.

- **M3U_MAX_CONCURRENCY_1, M3U_MAX_CONCURRENCY_2, M3U_MAX_CONCURRENCY_X**: Set max concurrency.

- **USER_AGENT**: Set the User-Agent of HTTP requests.

- **CRON_UPDATE**: Set cron schedule expression of the background updates (Default: "0 0 * * *").

### Endpoints

- `/playlist.m3u`: Get the merged M3U playlist.

- `/stream/{streamID}.mp4`: Stream MP4 content for specified stream IDs.

### Data Persistence

Utilizes a volume (`./data`) for M3U file persistence. Customize the volume path in `docker-compose.yml` if needed.

