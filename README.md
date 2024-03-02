# M3U Stream Merger Proxy

This service provides a simple HTTP proxy server for merging and streaming MP4 content from multiple M3U playlists. It acts as a proxy, consolidating streams from different M3U sources, effectively serving as a load balancer. The service is dockerized for easy deployment, and it periodically updates its M3U playlists to ensure the latest streams are available.

## Use Case: Consolidating Multiple IPTV Connections

If your IPTV provider issues multiple M3U links for each connection, but you prefer to consolidate them into a single URL for convenience, the M3U Stream Merger Proxy can help. Follow the steps below to simplify your IPTV setup:

## How It Works

1. **Initialization and M3U Playlist Consolidation:**
   - On startup, the service loads M3U playlists from the specified URLs (M3U_URL_1, M3U_URL_2, M3U_URL_3, etc.). This usually takes a while, depending on the size of the original M3U files as it goes through each entry. The parsing process will be done in the background.
   - It consolidates the playlists by merging different streams based on their stream name.
   - The consolidated data will be saved in a JSON file in the data folder which will serve as its "database."
   - For each unique stream name, the service aggregates the corresponding stream URLs into a consolidated M3U playlist.

2. **HTTP Endpoints:**
   - **Playlist Endpoint (`/playlist.m3u`):**
     - Accessing this endpoint returns the merged M3U playlist containing streams consolidated from different sources.

   - **Stream Endpoint (`/stream/{streamID}.mp4`):**
     - When a client requests an MP4 stream for a specific stream ID, the service fetches the corresponding stream URL from the consolidated M3U playlist.

3. **Load Balancing:**
   - The service employs a simple load-balancing strategy by cycling through the available stream URLs for a particular stream ID.
   - It attempts to fetch the MP4 stream from each URL until a successful connection is established or all URLs are exhausted.

4. **Periodic Updates:**
   - To ensure up-to-date stream information, the service periodically refreshes the M3U playlists at a specified interval (default is 24 hours).
   - This ensures that new streams or changes in the source M3U playlists are reflected in the consolidated playlist.

5. **Error Handling:**
   - If all stream URLs fail to provide a valid MP4 stream, the service logs the error and responds with an appropriate error message to the client.
   - It handles client disconnections during the streaming process and avoids unnecessary resource usage.

6. **Proxy Functionality:**
   - The M3U Stream Merger Proxy acts as a proxy server, abstracting the complexity of managing multiple M3U sources from clients.
   - Clients can interact with a single endpoint (`/stream/{streamID}.mp4`) while benefiting from the aggregated streams behind the scenes.

7. **Customization:**
   - Users can customize the service by modifying the M3U URLs, update interval, and other configuration options in the `.env` file.

### Prerequisites

- [Docker](https://www.docker.com/) installed on your system.

### Docker Compose

Use the provided `docker-compose.yml` file to deploy the M3U Stream Merger Proxy:

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

2. Run the following command:

   ```bash
   docker-compose up -d
   ```

This will start the M3U Stream Merger Proxy, and you can access it at `http://localhost:8080`.

### Configuration

- **M3U_URL_1, M3U_URL_2, M3U_URL_X**: Set the M3U URLs as environment variables in the `.env` file.

- **USER_AGENT**: Set the User-Agent of all HTTP requests. Default is `IPTV Smarters/1.0.3 (iPad; iOS 16.6.1; Scale/2.00)`.

- **UPDATE_INTERVAL**: (Optional) Set the update interval in hours. Default is 24 hours.

### Endpoints

- `/playlist.m3u`: Get the merged M3U playlist.

- `/stream/{streamID}.mp4`: Stream the MP4 content for the specified stream ID.

### Data Persistence

The service uses a volume (`./data`) to persist M3U files. You can customize this volume path in the `docker-compose.yml` file if needed.

Feel free to explore and modify the service according to your requirements! The M3U Stream Merger Proxy essentially acts as a proxy and load balancer between the provided M3U sources.
