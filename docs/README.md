# Technical Documentation

Deep dives into how the proxy works internally. For usage and configuration, see the [main README](../README.md).

- [Ingestion Pipeline](ingestion.md) - how M3U and Xtream sources become a merged, sorted, deduplicated playlist
- [On-Disk Storage Formats](storage.md) - the binary codecs: catalog store, spill records, series fragments, EPG cache
- [Streaming Path](streaming.md) - how a playback request is served: load balancing, the shared ring buffer, PCR pacing, failover
