# On-Disk Storage Formats

The proxy is stateless in the sense that there is no external database - everything is derived from sources and cached as flat files with small binary codecs. All formats are little-endian. Code: `sourceproc/store.go`, `sourceproc/sorting.go`, `xtream/series_cache.go`, `config/config.go` (paths).

## Why binary formats (and not JSON)

Every hot path needs record-level random access, not whole-file reads. A stream request resolves to `Get(slug)` (`store.go`): hash the slug, binary-search a sorted section of fixed-width 12-byte entries, decode one record. O(log n), one small heap allocation. JSON gives you no fixed-width sorted keys, so a JSON catalog forces one of two shapes: re-parse the entire file per lookup, or keep the entire corpus decoded on the heap forever. For ~800k streams that is hundreds of MB of live Go heap - the exact whole-file-decode pattern that OOM-killed the pod when the EPG and list endpoints did it.

The rest follows:

- Fixed-width sections are what make binary search possible; a JSON side index of `id -> offset` entries would just be this format with a JSON payload behind it.
- Mmap reads keep the corpus in page cache, not Go heap; a generation swap is one pointer-file rename with readers never seeing a torn state.
- The spill sorter streams length-prefixed frames through partition files and k-way merges them - appendable, sequential, no quoting/escaping overhead. A JSON array cannot be appended to or merged without a full reparse.
- Length-prefixed frames are self-delimiting and skip per-record field names; records are roughly half the size of their JSON rendering.

Text is used where the data is already text and consumed wholesale: the playlist is M3U, EPG is XMLTV, series fragments are rendered M3U lines replayed through the normal parser.

## Directory layout

```
.data/                     (root; see config.GetStreamStoreDirPath and friends)
├── m3u/                   downloaded source playlists (m3u_<index>)
├── processed/             merged playlist + stream catalog generations
│   ├── current            text file: active catalog generation number
│   ├── g<N>.cat           catalog data (generation N)
│   ├── g<N>.cix           catalog index (generation N)
│   └── playlist.m3u (+ .new candidate during sync)
├── sort/                  spill sorter scratch (deleted after each pass)
├── epg/                   cached EPG sources + merged epg.xml + tvg-ids
└── series/                Xtream stubs and fragment caches
```

## Spill record format (sort scratch)

Both the partition files and the sorted run files use length-prefixed frames:

```
[uint32 length][record bytes]
```

The record is a `StreamInfo` serialized field-by-field (`appendStreamInfo` in `sorting.go`), each string as `[uint32 len][bytes]`:

```
Title, TvgID, TvgChNo, TvgType, LogoURL, Group, SourceM3U  (strings)
SourceIndex                                                (int32)
URL count                                                  (uint32)
  per URL: M3UIndex (string), LineNum (int32), URL (string)
```

Partition files (`p0000.bin`...) exist only during a sync pass; the `sort/` directory is wiped at pass start and end.

## Stream catalog (`.cat` / `.cix`)

There is exactly one catalog for all source types: M3U and Xtream lines are indistinguishable after ingestion (Xtream sources are rendered to M3U text at download time), so the same records serve both playback and the Xtream API. It is write-once per sync: a full new generation is built, then atomically swapped in. Readers mmap both files and decode records on demand - steady-state memory is one mapped page cache plus per-request scratch, not the corpus.

### Data file `g<N>.cat`

A flat log of framed records:

```
[uint32 size][record bytes] [uint32 size][record bytes] ...
```

Each record starts with a 64-byte fixed header (`appendCatalogRecord`) followed by the ext string and the spill-format `StreamInfo` payload:

```
offset  len  field
0       28   slug digest (SHA3-224 of title)
28      8    stream id   = xxhash(title) & 0x7fff...  (63-bit)
36      8    category id = xxhash(group) ^ (kind * golden) & 0x7fffffff
44      8    series id   = xxhash("series|" + show) & 0x7fff...
52      4    show name length (uint32)
56      2    season (uint16)
58      2    episode (uint16)
60      1    kind: 1=live 2=movie 3=series
61      1    ext length  (e.g. ".ts"; from first URL's path)
62      2    padding
64      var  ext bytes, then StreamInfo payload (same encoding as spill records)
```

Kind comes from `tvg-type`; a series-typed stream without a parseable `SxxExx` title degrades to live (`parseEpisodeTitle`). Stream/series/category ids are 63-bit so every id fits in a signed 64-bit JSON number.

### Index file `g<N>.cix`

Starts with a 128-byte header (magic `M3UCAT04`, header length, counts, and section offsets), followed by fixed-width sorted sections:

| Section           | Entry width                  | Sorted by                  | Purpose                           |
| ----------------- | ---------------------------- | -------------------------- | --------------------------------- |
| offsets           | 8                            | record id (implicit)       | record id -> data-file offset     |
| slug lookups      | 12 (u64 key + u32 record id) | key                        | slug digest -> records (playback) |
| stream-id lookups | 12                           | key                        | stream id -> records (Xtream API) |
| categories        | 16                           | kind, then name            | category directory                |
| category members  | 12                           | key                        | category id -> member records     |
| series            | 32                           | series key                 | series directory (episode ranges) |
| series order      | 4                            | -                          | display-order positions           |
| episodes          | 16                           | series id, season, episode | series id -> episodes             |

Lookups are binary-searched (`lookupRange`); a key can map to multiple records. Loading validates the header, magic, data size, and that every section fits the file - a corrupt or truncated index is refused, not partially read.

### Generation swap

A new generation `N+1` is written while generation `N` keeps serving. `Commit` (`store.go`) fsyncs data, writes the index, then publishes by renaming `current.new` -> `current` (atomic) and fsyncing the directory. Readers (re)load by reading `current`, mmapping `g<N>.cat`/`g<N>.cix`, and only then unmapping the old generation. The previous generation files are deleted after the swap; a crash mid-sync leaves `current` pointing at the last complete generation.

## Xtream series stubs and fragments

The only source-type-specific storage. Xtream series episodes are populated lazily after the main pass (see [ingestion.md](ingestion.md)); these two files are the cache that makes replay cheap.

Stub file (magic `XSTUB01`, `uint16` count, then per stub: `uint64` upstream id + three length-prefixed strings name/group/cover). Stubs are rewritten atomically (tmp + rename) each sync.

Fragment files are append-only text with a lightweight envelope:

```
#XSERIES <upstream-id>
<rendered M3U lines...>
```

Replay semantics are last-write-wins per id in file order, so population appends batches (O(batch) IO) and a single compaction pass per sync rewrites the file keeping only the last version of each series (and only ids still present in the stub list). A corrupt fragment file is treated as empty and rebuilt by the next pass.

## EPG cache

Per-source XMLTV files are cached verbatim (`epg/epg_<index>`); `epg.xml` is the merged output written fresh each sync; `tvg_ids.txt` is the newline-separated id set produced by the playlist pass (see [ingestion.md](ingestion.md)).
