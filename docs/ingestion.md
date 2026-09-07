# Ingestion Pipeline

How sources become the merged playlist, the stream catalog, and the EPG file. Code lives in `sourceproc/` (M3U), `xtream/` + `updater/` (Xtream), and `epg/`.

## Overview

```
M3U sources ──┐
              ├─> line stream ─> parse ─> spill sorter ─> fold+sort+render ─> playlist.m3u + catalog store
Xtream sources┘   (per source)   StreamInfo   (disk)        (per partition)      (single ordered pass)
```

Ingestion runs on boot (`SYNC_ON_BOOT=true`, default) and on the `SYNC_CRON` schedule (default `0 0 * * *`, daily at midnight). `EPG_SYNC_CRON` optionally runs EPG-only syncs on its own schedule. See `updater/updater.go`.

## Phase 1: download and parse

- Each configured source (`M3U_URL_X` / `XTREAM_URL_X`) is downloaded line-by-line and streamed to parsers - the full file is never held in memory (`sourceproc/downloader.go`, `sourceproc/processor.go`).
- Xtream sources are converted to M3U text first: `get_live_streams`, `get_vod_streams` and `get_series` are fetched from the provider and rendered as M3U lines (`xtream/`). Series are rendered lazily (see below).
- The parser (`sourceproc/parser.go`) walks `#EXTINF` attribute pairs (`tvg-id`, `tvg-chno`/`channel-id`/`channel-number`, `tvg-name`, `tvg-type`, `tvg-group`/`group-title`, `tvg-logo`), then the display name after the last unquoted comma. A stream without a title is dropped.
- Each parsed stream becomes a `StreamInfo` (`sourceproc/stream_info.go`) carrying its provider index (`SourceM3U`, the `X` of `M3U_URL_X`), the physical line number in that provider's file (`SourceIndex`), and the URL list. Streams are then handed to the spill sorter (`processor.go` `addStream`).
- `M3U_FILTER_INCLUDE` / `M3U_FILTER_EXCLUDE` regex filters drop streams before they reach the sorter (`sourceproc/filter.go`).
- Parsing is parallel: one producer goroutine per source, `NumCPU x 2` parser workers consuming a buffered channel.

## Phase 2: external hash-partitioned sort

The sort (`sourceproc/sorting.go`) never holds the corpus in RAM. It is an external sort with three stages:

1. **Spill while parsing.** `spillSorter.Add` hashes the sanitized title (`xxhash`) and appends the binary-encoded `StreamInfo` to one of 256-512 partition files (`p0000.bin`...), routed by `hash % numPartitions`. Partitioning layout is sized from `GOMAXPROCS` and pinned so fold memory stays around 1/8 of corpus size (`spillLayout`).
2. **Fold + sort each partition, in parallel.** `buildRun` reads one whole partition into a pooled arena, folds duplicates by sanitized title in a map (merge semantics: first non-empty wins per attribute, URLs are unioned, provider position keeps the minimum `(SourceM3U, SourceIndex)` - see `mergeStreamInfoAttributes`), sorts the folded entries, and writes a sorted run file of pre-rendered entries.
3. **K-way merge.** `MergeRendered` opens every run file, heap-merges them (`runHeap`), and emits entries in final order through a single callback. This is a single sequential write pass - there is no intermediate unsorted playlist on disk.

## Sorting keys

The active key is `SORTING_KEY` (default `provider-order`), direction is `SORTING_DIRECTION` (`asc`/`desc`, default `asc`). Supported keys (`sortEntryFor`):

| Key                                         | Sorts by                                                                                                 |
| ------------------------------------------- | -------------------------------------------------------------------------------------------------------- |
| `provider-order` / `source-order` (default) | Provider position: all of M3U_1 in file order, then M3U_2, ... (key = padded source index + line number) |
| `title` (and any unrecognized value)        | Lowercased title                                                                                         |
| `tvg-chno`, `channel-id`, `channel-number`  | Channel number, numeric when parseable                                                                   |
| `tvg-id`                                    | Tvg id, numeric when parseable                                                                           |
| `source`                                    | Source M3U index, numeric when parseable                                                                 |
| `tvg-group`, `group-title`                  | Lowercased group                                                                                         |
| `tvg-type`                                  | Lowercased type                                                                                          |

Because duplicates are folded by sanitized title, merged channels have no single provider position; provider-order uses the earliest `(source, line)` among the merged entries.

## Phase 3: emit

During the merge pass each entry is rendered once (`compileM3U` in `processor.go`):

- a playlist line `#EXTINF:... ` + `{baseURL}/p/stream/{slug}` written to the new playlist file, where `slug` is `base64url(SHA3-224(title))` - a stable, collision-safe id containing no credentials or upstream URLs,
- a binary catalog record appended to the new stream store generation (`storage.md`),
- the stream's `tvg-id` collected into a set that the EPG processor later uses to filter programme data.

The whole pass is one ordered write: playlist, catalog, and tvg-id set are all produced by the same merge. On success the new files replace the old ones atomically (`applyNewRemoteFiles`); on failure the candidate files are deleted and the previous generation keeps serving (`cleanFailedRemoteFiles`).

## Xtream series lazy population

Series catalogs are large (tens of thousands of series per provider), and resolving episodes for every series on every sync would hammer providers. Instead:

1. At sync time, each series is stored as a tiny stub (`XSTUB01` binary file; see `storage.md`) with its upstream id, name, group and cover. Series playlist lines are emitted as stubs.
2. A background populate loop (`updater/populate.go`) walks stubs in batches (250 at a time, throttled), calls `get_series_info` upstream, renders the episode M3U lines, and **appends** them to a per-source fragment cache file.
3. Fragment files are compacted once per pass: last-write-wins per series id, dropping series no longer in the stub list. Appending is semantically identical to replacing because replay reads last-wins (`xtream/series_cache.go`).
4. Players browsing a specific series force on-demand population through the same cache (bounded in-memory LRU in `handlers/series_lazy.go`).

## EPG sync

`EPG_URL_X` sources are downloaded (gzip-aware, with fallback to the previously cached file on failure), then merged into one XMLTV document (`epg/processor.go`):

- `<channel>` elements: deduplicated by `id`; first source wins.
- `<programme>` elements: all sources included, filtered to the tvg-id set saved by the playlist pass, so the merged EPG only contains channels that survived merging.
- Merging is streaming (token-level XML pass per source per element kind), never decoding a whole document into memory.
- A combined size limit of `EPG_MAX_SIZE_MB` (default 500) caps per-source downloads.

The result is written to `epg.xml` and served by `/epg.xml` and `/xmltv.php` as a plain file send.
