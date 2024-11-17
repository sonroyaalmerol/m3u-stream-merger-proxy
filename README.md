# 📡 M3U Stream Merger Proxy
[![Codacy Badge](https://app.codacy.com/project/badge/Grade/15a1064c638d4402931fe633b2baa51d)](https://app.codacy.com/gh/sonroyaalmerol/m3u-stream-merger-proxy/dashboard?utm_source=gh&utm_medium=referral&utm_content=&utm_campaign=Badge_grade) [![Docker Pulls](https://img.shields.io/docker/pulls/sonroyaalmerol/m3u-stream-merger-proxy.svg)](https://hub.docker.com/r/sonroyaalmerol/m3u-stream-merger-proxy/) [![](https://img.shields.io/docker/image-size/sonroyaalmerol/m3u-stream-merger-proxy)](https://img.shields.io/docker/image-size/sonroyaalmerol/m3u-stream-merger-proxy) [![Release Images](https://github.com/sonroyaalmerol/m3u-stream-merger-proxy/actions/workflows/release.yml/badge.svg)](https://github.com/sonroyaalmerol/m3u-stream-merger-proxy/actions/workflows/release.yml) [![Developer Images](https://github.com/sonroyaalmerol/m3u-stream-merger-proxy/actions/workflows/developer.yml/badge.svg)](https://github.com/sonroyaalmerol/m3u-stream-merger-proxy/actions/workflows/developer.yml) [![Discord](https://img.shields.io/discord/1274826220596625603?logo=discord&label=Discord&link=https%3A%2F%2Fdiscord.gg%2Fb2hVjRvkcj)](https://discord.com/invite/b2hVjRvkcj)
<!-- ALL-CONTRIBUTORS-BADGE:START - Do not remove or modify this section -->
[![All Contributors](https://img.shields.io/badge/all_contributors-1-orange.svg?style=flat-square)](#contributors-)
<!-- ALL-CONTRIBUTORS-BADGE:END -->

> [!WARNING]  
> This repo is currently in heavy development. Expect major changes on every release until the first stable release, `1.0.0`.
> Using the `:dev` tag will allow you to be up-to-date with the main branch.

Streamline your IPTV experience by consolidating multiple M3U playlists into a single source with the blazingly fast 🔥 and lightweight M3U Stream Merger Proxy. This service acts as a modern HTTP proxy server, effortlessly merging and streaming content from various M3U sources.

Uses the channel title or `tvg-name` (as fallback) to merge multiple identical channels into one. This is not an xTeVe/Threadfin replacement but is often used with it.

> [!IMPORTANT]  
> Redis has been **removed** as a dependency starting `0.16.0`. The proxy should now be (mostly) **stateless**.
> Migrating to `0.16.0` is as easy as removing the Redis container from your compose file.
> Due to a major change on how data is being processed, any Redis persistence cannot be migrated over and a sync from the original M3U sources will be required.

## How It Works

1. **Initialization and M3U Playlist Consolidation:**
   - The service loads M3U playlists from specified URLs, consolidating streams into `/playlist.m3u`.
   - The consolidation process merges streams based on their names and saves them into one M3U file.
   - Each unique stream name aggregates corresponding URLs into the consolidated playlist.

2. **HTTP Endpoints:**
   - **Playlist Endpoint (`/playlist.m3u`):**
     - Access the merged M3U playlist containing streams from different sources.

   - **Stream Endpoint (`/p/{originalBasePath}/{streamToken}.{fileExt}`):**
     - Request video streams for specific stream IDs.
     - `originalBasePath`: Parsed from one of the original source. This is to prevent clients to miscategorize the stream due to a missing keyword (e.g. live, vod, etc.).
     - `streamToken`: An encoded string that contains the stream title and an array of the original stream URLs associated with the stream title. This token allows the proxy to be **stateless** as the M3U itself is the "database".
     - `fileExt`: Parsed file extension from one of the original source.

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

services:
  m3u-stream-merger-proxy:
    image: sonroyaalmerol/m3u-stream-merger-proxy:latest
    ports:
      - "8080:8080"
    environment:
      - PUID=1000
      - PGID=1000
      - TZ=America/Toronto
      - SYNC_ON_BOOT=true
      - SYNC_CRON=0 0 * * *
      - M3U_URL_1=https://iptvprovider1.com/playlist.m3u
      - M3U_MAX_CONCURRENCY_1=2
      - M3U_URL_2=https://iptvprovider2.com/playlist.m3u
      - M3U_MAX_CONCURRENCY_2=1
      - M3U_URL_X=
    restart: always
    # [OPTIONAL] Cache persistence: This will allow you to reuse the M3U cache across container recreates.
    # volumes:
    #   - ./data:/m3u-proxy/data
```

Access the generated M3U playlist at `http://<server ip>:8080/playlist.m3u`.

## Environment Variable Configurations

> [!NOTE]
> This configuration list only applies to the latest release version.
> To see the README of a specific version, navigate to the specific tag of the desired version (e.g. [`0.10.0`](https://github.com/sonroyaalmerol/m3u-stream-merger-proxy/tree/0.10.0)).

> [!TIP]
> For variables needing Go regexp (regular expression) values, make sure to [build the regexp](https://regex101.com/) with the Golang flavor.
> If you only need to filter out a specific substring, then putting in the substring itself in those variables should work just fine.

### Container Configs
| ENV VAR                     | Description                                              | Default Value | Possible Values                                |
|-----------------------------|----------------------------------------------------------|---------------|------------------------------------------------|
| PORT | Set listening port of service inside the container.                  |   8080 |   Any valid port |
| PUID | Set UID of user running the container.                  |   1000 |   Any valid UID |
| PGID | Set GID of user running the container.                  |   1000 |   Any valid GID |
| TZ                          | Set timezone                                           | Etc/UTC     | [TZ Identifiers](https://nodatime.org/TimeZones) |

### Playlist Source Configs
| ENV VAR                     | Description                                              | Default Value | Possible Values                                |
|-----------------------------|----------------------------------------------------------|---------------|------------------------------------------------|
| M3U_URL_1, M3U_URL_2, M3U_URL_X | Set M3U URLs as environment variables.                  |   N/A            |   Any valid M3U URLs                                             |
| M3U_MAX_CONCURRENCY_1, M3U_MAX_CONCURRENCY_2, M3U_MAX_CONCURRENCY_X | Set max concurrency. The "X" should match the M3U URL.                                 |  1             |   Any integer                                             |
| USER_AGENT                  | Set the User-Agent of HTTP requests.                    | IPTV Smarters/1.0.3 (iPad; iOS 16.6.1; Scale/2.00)    |  Any valid user agent        |
| SYNC_CRON                   | Set cron schedule expression of the background updates. | 0 0 * * *   |  Any valid cron expression    |
| SYNC_ON_BOOT                | Set if an initial background syncing will be executed on boot | true    | true/false   |
| CACHE_ON_SYNC               | Set if an initial background cache building will be executed after sync. Requires BASE_URL to be set. | false | true/false   |
| CLEAR_ON_BOOT                | Set if an initial database clearing will be executed on boot | false   | true/false   |

### Load Balancer Configs
| ENV VAR                     | Description                                              | Default Value | Possible Values                                |
|-----------------------------|----------------------------------------------------------|---------------|------------------------------------------------|
| MAX_RETRIES | Set max number of retries (loop) across all M3Us while streaming. 0 to never stop retrying (beware of throttling from provider). | 5 | Any integer greater than or equal 0 |
| RETRY_WAIT | Set a wait time before retrying (looping) across all M3Us on stream initialization error. | 0 | Any integer greater than or equal 0 |
| STREAM_TIMEOUT | Set timeout duration in seconds of retrying on error before a stream is considered down. | 3 | Any positive integer greater than 0 |
| BUFFER_MB | Set buffer size in mb. **This is not a shared buffer (for now).** | 0 (no buffer) | Any positive integer |

### Playlist Output (`/playlist.m3u`) Configs
> [!NOTE]
> Filter configs (e.g. `INCLUDE_GROUPS_X`, `EXCLUDE_GROUPS_X`, `INCLUDE_TITLE_X`, `EXCLUDE_TITLE_X`) only applies **every sync** from source.
> Changes in the values will not reflect immediately unless the cache is cleared which forces the sync to trigger.
> Also, the `X` values on these env vars are **not associated** with the `X` values of the M3U URLs. They are simply a way for you to use multiple filters for each.

| ENV VAR                     | Description                                              | Default Value | Possible Values                                |
|-----------------------------|----------------------------------------------------------|---------------|------------------------------------------------|
| BASE_URL | Sets the base URL for the stream URls in the M3U file to be generated. | http/s://<request_hostname> (e.g. <http://192.168.1.10:8080>)    | Any string that follows the URL format  |
| SORTING_KEY | Set tag to be used for sorting the stream list | tvg-id | tvg-id, tvg-chno |
| INCLUDE_GROUPS_1, INCLUDE_GROUPS_2, INCLUDE_GROUPS_X    | Set channels to include based on groups (Takes precedence over EXCLUDE_GROUPS_X) | N/A | Go regexp |
| EXCLUDE_GROUPS_1, EXCLUDE_GROUPS_2, EXCLUDE_GROUPS_X    | Set channels to exclude based on groups | N/A | Go regexp |
| INCLUDE_TITLE_1, INCLUDE_TITLE_2, INCLUDE_TITLE_X    | Set channels to include based on title (Takes precedence over EXCLUDE_TITLE_X) | N/A | Go regexp |
| EXCLUDE_TITLE_1, EXCLUDE_TITLE_2, EXCLUDE_TITLE_X    | Set channels to exclude based on title | N/A | Go regexp |
| TITLE_SUBSTR_FILTER | Sets a regex pattern used to exclude substrings from channel titles. This modifies the title of the streams when rendered in `/playlist.m3u`. | none    | Go regexp   |

### Logging Configs
| ENV VAR                     | Description                                              | Default Value | Possible Values                                |
|-----------------------------|----------------------------------------------------------|---------------|------------------------------------------------|
| DEBUG                | Set if verbose logging is enabled | false    | true/false   |
| SAFE_LOGS | Set if sensitive info are removed from logs. Always enable this if submitting a log publicly. | false    | true/false   |

## Sponsors ✨
Huge thanks to those who donated for the development of this project!

<p align="left"><!-- markdownlint-disable-line --><!-- markdownlint-disable-next-line -->
<!-- sponsors --><!-- sponsors -->
</p>

<p align="left"><!-- markdownlint-disable-line --><!-- markdownlint-disable-next-line -->
<a href="https://github.com/kpirnie"><img src="https://github.com/kpirnie.png" width="50px" alt="kpirnie" /></a>&nbsp;&nbsp;<a href="https://github.com/jbumgard"><img src="https://github.com/jbumgard.png" width="50px" alt="jbumgard" /></a>&nbsp;&nbsp;<a href="https://github.com/aniel300"><img src="https://github.com/aniel300.png" width="50px" alt="aniel300" /></a>&nbsp;&nbsp;
</p>

## Contributors ✨

<!-- ALL-CONTRIBUTORS-LIST:START - Do not remove or modify this section -->
<!-- prettier-ignore-start -->
<!-- markdownlint-disable -->
<table>
  <tbody>
    <tr>
      <td align="center" valign="top" width="14.28%"><a href="https://paypal.me/saalmerol"><img src="https://avatars.githubusercontent.com/u/17176864?v=4?s=100" width="100px;" alt="Son Roy Almerol"/><br /><sub><b>Son Roy Almerol</b></sub></a><br /><a href="#infra-sonroyaalmerol" title="Infrastructure (Hosting, Build-Tools, etc)">🚇</a> <a href="https://github.com/sonroyaalmerol/m3u-stream-merger-proxy/commits?author=sonroyaalmerol" title="Tests">⚠️</a> <a href="https://github.com/sonroyaalmerol/m3u-stream-merger-proxy/commits?author=sonroyaalmerol" title="Documentation">📖</a> <a href="https://github.com/sonroyaalmerol/m3u-stream-merger-proxy/commits?author=sonroyaalmerol" title="Code">💻</a></td>
      <td align="center" valign="top" width="14.28%"><a href="https://github.com/andjos"><img src="https://avatars.githubusercontent.com/u/15799439?v=4?s=100" width="100px;" alt="Anders Josefsson"/><br /><sub><b>Anders Josefsson</b></sub></a><br /><a href="https://github.com/sonroyaalmerol/m3u-stream-merger-proxy/commits?author=andjos" title="Documentation">📖</a> <a href="https://github.com/sonroyaalmerol/m3u-stream-merger-proxy/commits?author=andjos" title="Code">💻</a></td>
      <td align="center" valign="top" width="14.28%"><a href="https://pixelhosting.nl"><img src="https://avatars.githubusercontent.com/u/45701225?v=4?s=100" width="100px;" alt="blackwhitebear8"/><br /><sub><b>blackwhitebear8</b></sub></a><br /><a href="https://github.com/sonroyaalmerol/m3u-stream-merger-proxy/commits?author=blackwhitebear8" title="Code">💻</a></td>
      <td align="center" valign="top" width="14.28%"><a href="https://kevinpirnie.com"><img src="https://avatars.githubusercontent.com/u/48323656?v=4?s=100" width="100px;" alt="Kevin Pirnie"/><br /><sub><b>Kevin Pirnie</b></sub></a><br /><a href="https://github.com/sonroyaalmerol/m3u-stream-merger-proxy/commits?author=kpirnie" title="Code">💻</a></td>
    </tr>
  </tbody>
</table>

<!-- markdownlint-restore -->
<!-- prettier-ignore-end -->

<!-- ALL-CONTRIBUTORS-LIST:END -->

## Contributing

First off, thanks for taking the time to contribute! ❤️

All types of contributions are encouraged and valued. 🎉

I'm currently looking to add more `tvg-*` tags that might be used by some IPTV providers. Feel free to post an issue if you require a specific tag!

And if you like the project, but just don't have time to contribute, that's fine. There are other easy ways to support the project and show your appreciation, which I would also be very happy about:
- Star the project
- Tweet about it
- Mention the project and tell your friends/colleagues
