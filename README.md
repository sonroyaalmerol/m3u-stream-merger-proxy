# 📡 M3U Stream Merger Proxy
[![Codacy Badge](https://app.codacy.com/project/badge/Grade/15a1064c638d4402931fe633b2baa51d)](https://app.codacy.com/gh/sonroyaalmerol/m3u-stream-merger-proxy/dashboard?utm_source=gh&utm_medium=referral&utm_content=&utm_campaign=Badge_grade) [![Docker Pulls](https://img.shields.io/docker/pulls/sonroyaalmerol/m3u-stream-merger-proxy.svg)](https://hub.docker.com/r/sonroyaalmerol/m3u-stream-merger-proxy/) [![](https://img.shields.io/docker/image-size/sonroyaalmerol/m3u-stream-merger-proxy)](https://img.shields.io/docker/image-size/sonroyaalmerol/m3u-stream-merger-proxy) [![Release Images](https://github.com/sonroyaalmerol/m3u-stream-merger-proxy/actions/workflows/release.yml/badge.svg)](https://github.com/sonroyaalmerol/m3u-stream-merger-proxy/actions/workflows/release.yml) [![Developer Images](https://github.com/sonroyaalmerol/m3u-stream-merger-proxy/actions/workflows/developer.yml/badge.svg)](https://github.com/sonroyaalmerol/m3u-stream-merger-proxy/actions/workflows/developer.yml) [![Discord](https://img.shields.io/discord/1274826220596625603?logo=discord&label=Discord&link=https%3A%2F%2Fdiscord.gg%2Fb2hVjRvkcj)](https://discord.com/invite/b2hVjRvkcj)

[Chat with the community on Discord](https://discord.gg/b2hVjRvkcj)

Streamline your IPTV experience by consolidating multiple M3U playlists into a single source with the blazingly fast 🔥 and lightweight M3U Stream Merger Proxy. This service acts as a modern HTTP proxy server, effortlessly merging and streaming content from various M3U sources.

Uses the channel title or `tvg-name` (as fallback) to merge multiple identical channels into one. This is not an xTeVe/Threadfin replacement but is often used with it.

Currently, nested M3U files are not supported (M3U playlists inside a parent M3U playlist).

> [!IMPORTANT]  
> All versions after `0.10.0` will require an external Redis/Valkey instance. The SQLite database within the data folder will not be used going forward. For data persistence, refer to the [Redis](https://redis.io/docs/latest/operate/oss_and_stack/management/persistence/) docs. The sample `docker-compose.yml` below has also been modified to include Redis.
> To see the README of a specific version, navigate to the specific tag of the desired version (e.g. [`0.10.0`](https://github.com/sonroyaalmerol/m3u-stream-merger-proxy/tree/0.10.0)).

## How It Works

1. **Initialization and M3U Playlist Consolidation:**
   - The service loads M3U playlists from specified URLs, consolidating streams into `/playlist.m3u`.
   - The consolidation process merges streams based on their names and saves them in a database.
   - Each unique stream name aggregates corresponding URLs into the consolidated playlist.

2. **HTTP Endpoints:**
   - **Playlist Endpoint (`/playlist.m3u`):**
     - Access the merged M3U playlist containing streams from different sources.

   - **Stream Endpoint (`/stream/{streamID}.{fileExt}`):**
     - Request video streams for specific stream IDs.

3. **Load Balancing:**
   - The service employs load balancing by cycling through available stream URLs.
   - Users can set max concurrency per stream URLs for optimized performance.

4. **Periodic Updates:**
   - Refreshes M3U playlists at specified intervals (cron schedule syntax) to ensure up-to-date stream information.
   - Updates run in the background with no downtime.

5. **Proxy Functionality:**
   - Abstracts complexity for clients, allowing interaction with a single endpoint.
   - Aggregates streams behind the scenes for a seamless user experience.

6. **Customization:**
   - Modify M3U URLs, update intervals, and other configurations in the `.env` file.

## Prerequisites

- [Docker](https://www.docker.com/) installed on your system.
- M3U URLs containing a playlist of video streams.

## Docker Compose

Deploy with ease using the provided `docker-compose.yml`:

```yaml
version: '3'

services:
  m3u-stream-merger-proxy:
    image: sonroyaalmerol/m3u-stream-merger-proxy:latest
    ports:
      - "8080:8080"
    environment:
      - TZ=America/Toronto
      - REDIS_ADDR=redis:6379
      - SYNC_ON_BOOT=true
      - SYNC_CRON=0 0 * * *
      - M3U_URL_1=https://iptvprovider1.com/playlist.m3u
      - M3U_MAX_CONCURRENCY_1=2
      - M3U_URL_2=https://iptvprovider2.com/playlist.m3u
      - M3U_MAX_CONCURRENCY_2=1
      - M3U_URL_X=
    restart: always
  redis:
    image: redis
    restart: always
    # Redis persistence is OPTIONAL. This will allow you to reuse the database across restarts.
    # command: redis-server --save 60 1
    # volumes:
    #   - ./data:/data
```

Access the generated M3U playlist at `http://<server ip>:8080/playlist.m3u`.

## Configuration

| ENV VAR                     | Description                                              | Default Value | Possible Values                                |
|-----------------------------|----------------------------------------------------------|---------------|------------------------------------------------|
| M3U_URL_1, M3U_URL_2, M3U_URL_X | Set M3U URLs as environment variables.                  |   N/A            |   Any valid M3U URLs                                             |
| M3U_MAX_CONCURRENCY_1, M3U_MAX_CONCURRENCY_2, M3U_MAX_CONCURRENCY_X | Set max concurrency.                                 |  1             |   Any integer                                             |
| REDIS_ADDR | Set Redis server address | N/A | e.g. localhost:6379 |
| REDIS_PASS | Set Redis server password | N/A | Any string |
| REDIS_DB | Set Redis server database to be used | 0 | 0 to 15 |
| SORTING_KEY | Set tag to be used for sorting the stream list | tvg-id | tvg-id, tvg-chno |
| USER_AGENT                  | Set the User-Agent of HTTP requests.                    | IPTV Smarters/1.0.3 (iPad; iOS 16.6.1; Scale/2.00)    |  Any valid user agent        |
| ~~LOAD_BALANCING_MODE~~ (removed on version 0.10.0)                | Set load balancing algorithm to a specific mode | brute-force    | brute-force/round-robin   |
| PARSER_WORKERS | Set number of workers to spawn for M3U parsing. | 5 | Any positive integer |
| BUFFER_MB | Set buffer size in mb. | 0 (no buffer) | Any positive integer |
| INCLUDE_GROUPS_1, INCLUDE_GROUPS_2, INCLUDE_GROUPS_X    | Set channel groups to include | all    | Comma-separated values   |
| TITLE_SUBSTR_FILTER | Sets a regex pattern used to exclude substrings from channel titles | none    | Go regexp   |
| TZ                          | Set timezone                                           | Etc/UTC     | [TZ Identifiers](https://nodatime.org/TimeZones) |
| SYNC_CRON                   | Set cron schedule expression of the background updates. | 0 0 * * *   |  Any valid cron expression    |
| SYNC_ON_BOOT                | Set if an initial background syncing will be executed on boot | true    | true/false   |
| CACHE_ON_SYNC               | Set if an initial background cache building will be executed after sync | false | true/false   |
| CLEAR_ON_BOOT                | Set if an initial database clearing will be executed on boot | false   | true/false   |
| DEBUG                | Set if verbose logging is enabled | false    | true/false   |


## Endpoints

- `/playlist.m3u`: Get the merged M3U playlist.

- `/stream/{streamID}.{fileExt}`: Stream video content for specified stream IDs.

## Contributing

First off, thanks for taking the time to contribute! ❤️

All types of contributions are encouraged and valued. 🎉

I'm currently looking to add more `tvg-*` tags that might be used by some IPTV providers. Feel free to post an issue if you require a specific tag!

And if you like the project, but just don't have time to contribute, that's fine. There are other easy ways to support the project and show your appreciation, which I would also be very happy about:
- Star the project
- Tweet about it
- Mention the project and tell your friends/colleagues
