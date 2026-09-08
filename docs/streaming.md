# Streaming Path

How a playback request is served: from slug to bytes, including load balancing, the shared ring buffer, pacing, and failover. Code: `handlers/stream_http.go`, `proxy/stream/`, `proxy/loadbalancer/`, `store/concurrency.go`.

## Routing

| Route                                                                        | Serves                                             |
| ---------------------------------------------------------------------------- | -------------------------------------------------- |
| `/p/stream/{slug}`                                                           | TS media proxy (the URL written into the playlist) |
| `/a/{url}`                                                                   | upstream URL passthrough (URL-encoded in the path) |
| `/segment/{url}`                                                             | HLS segment proxy for m3u8 sources                 |
| `/playlist.m3u`, `/epg.xml`                                                  | file sends                                         |
| `/live/{u}/{p}/{id}.ts`, `/movie/...`, `/series/...`, `/player_api.php`, ... | Xtream-compatible API (see main README)            |

## Request flow

1. The handler extracts the slug from the path and resolves it through the catalog store (`sourceproc.DecodeSlug` -> `StreamStore.Get`), yielding the `StreamInfo` with every provider URL for that channel.
2. A per-channel coordinator is fetched from the registry (`GetOrCreateCoordinator` keyed by the slug). If a healthy writer is already streaming that channel, the client attaches to its shared buffer ("Existing shared buffer found") - only one upstream connection per channel is kept regardless of viewer count.
3. Otherwise the load balancer (`proxy/loadbalancer/`) picks a provider URL: sources are ordered by current connection load (the concurrency manager tracks per-source up/down state, connection counts, and per-source `M3U_MAX_CONCURRENCY_X` caps), and the channel's URLs within the chosen source are tested in order.
4. `StreamInstance.ProxyStream` (`proxy/stream/stream_instance.go`) classifies the response:
   - **Shared buffer path** (default): live TS (`video/mp2t` or `.ts`), including 206 replies to the player's `Range:` header - an upstream 206 on a TS stream is still live.
   - **Direct path**: VOD (`206` on non-TS content, e.g. `.mp4` range requests) is proxied 1:1 without the ring buffer, preserving range semantics for seeking.
   - m3u8 sources are handled by the HLS failover processor, segment by segment.
5. On handler exit codes the loop either returns, or excludes the failed URL, waits 500ms, and tries the next candidate.

## The shared ring buffer (coordinator)

`proxy/stream/buffer/coordinator.go`. One `StreamCoordinator` per channel. At its core is a ring of `BUFFER_CHUNK_NUM` slots (default 8), each holding a 1 MiB chunk:

- **Writer** (`coordinator_media.go`): reads the upstream body into 1 MiB slabs, publishes each slab into the ring under a mutex, updates `lastSuccess` after every chunk, and exits on EOF, context cancel, terminal read error, or when no clients remain. A mid-stream EOF marks the coordinator closed; the handler's retry loop starts a fresh writer on the next provider.
- **Readers** (`media_stream.go`): each client walks the ring from its own cursor, copying chunks out. A reader that laps the writer (pump faster than drain) is re-joined at the live edge, at the next TS packet boundary with payload-unit-start (PUSI) alignment (an <=8 KB scan for the 0x47 sync byte + PUSI flag) so decoders resync cleanly.
- **Resume across failover**: a client remembers the last sequence number it wrote (`LastSeq`); when the handler retries with a new writer, the reader resumes from that sequence in the surviving ring instead of jumping to the live edge, so upstream switches are invisible to the player when the buffer covers the gap.

The ring's job is bridging: it covers `BUFFER_CHUNK_NUM MiB / bitrate` seconds of upstream stall or failover. Chunks are allocated lazily (an idle coordinator costs almost nothing; memory scales with channels actually being watched), and the ring is deliberately sized to cover one `STREAM_TIMEOUT` window, not to absorb a paused player - a player that stops reading for longer than the ring can hold will be re-joined at the live edge. The latched log warning (`needs ~Ns of buffer but the ring holds only Ns`) fires when a channel's bitrate makes the ring too small for one timeout window.

## Health checks and timeouts

- `STREAM_TIMEOUT` (default 3s): a writer with no successful chunk within the window is considered stalled and fails over.
- `MAX_RETRIES` (default 5): attempts across providers per client request before giving up.
- `MINIMUM_THROUGHPUT` (default 0 = disabled): opt-in static floor on delivered bytes/sec. Left off by default because low-bitrate channels legitimately deliver below any fixed floor.

## PCR pacing

Some providers pump several times faster than realtime. A player (mpv and friends) fills its demuxer cache, stops reading for tens of seconds, and laps any fixed-size ring - showing up as the stream looping back a scene and reconnecting every 30-60s. No ring size fixes this; the fix is pacing.

`ENABLE_PCR_PACER=true` (default false) enables `proxy/stream/buffer/pcr_pacer.go`, which wraps the writer's publish step:

- It scans MPEG-TS packets for PCR (program clock reference) timestamps and derives the stream's true content bitrate from an EMA over observed PCR deltas.
- The writer is allowed a bounded lead ahead of realtime - `min(10s, ring-duration / 2)` - then sleeps (in <=500ms context-aware slices) so PCR advances at 1x wall-clock. Delivery to clients stays smooth; upstream bandwidth drops to ~1x realtime instead of the pump rate.
- PCR 33-bit wraparound is handled, and PCR discontinuities (jump > +60s or < -5s) re-anchor instead of panicking the math.
- No PCR found in the first 2 MiB (radio, non-TS, sparse PCR): the pacer fails open and behaves as if disabled.

Pacing applies only to the shared-buffer TS writer - never to VOD direct passthrough or HLS segments.

## Memory model

Per actively-watched channel: one upstream connection, one ring (`BUFFER_CHUNK_NUM` x 1 MiB, lazily filled) plus one in-flight slab. Per additional viewer of the same channel: only a cursor and a copy buffer. The catalog is mmap'd, series fragments live on disk, so steady-state RSS is roughly `baseline + 26 MiB per active channel` at the default `BUFFER_CHUNK_NUM=8`. In memory-limited containers (k8s), the Go heap soft limit is auto-capped at 90% of the cgroup limit unless `GOMEMLIMIT` is set explicitly.

Ingest, not streaming, sets the high-water mark. Measured on 800k streams with `GOMAXPROCS=2`: peak RSS 114.8 MiB at `GOMEMLIMIT=115MiB` (a 128 MB container's auto-cap), 93.6 MiB at `GOMEMLIMIT=90MiB`, same 2s runtime. Live heap at the peak is ~57 MiB, dominated by the catalog index slices, so a lower `GOMEMLIMIT` buys headroom for concurrent viewers at no measured throughput cost. For a 128 MB deployment: `GOMEMLIMIT=90MiB` and `BUFFER_CHUNK_NUM=2`.

## Goroutine and connection hygiene

- Header notification, state transitions, and writer swaps inside a coordinator are mutex-guarded (a header channel is closed exactly once per generation; `headerMu`).
- Status reporting back to the handler is context-aware: a disconnecting client cannot leave a proxy goroutine blocked on a full status channel.
- Coordinators with zero clients are reaped by a 30s registry sweep; the last unregister releases the ring immediately unless a handler retry is pending.
