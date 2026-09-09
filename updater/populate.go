package updater

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"m3u-stream-merger/config"
	"m3u-stream-merger/xtream"
)

const (
	populateStreakLimit = 10
	populateBatchSize   = 250
	populatePassGap     = time.Hour
)

var populateBackoffMax = 30 * time.Second

func seriesBGWorkers() int {
	if n, err := strconv.Atoi(os.Getenv("XTREAM_SERIES_BG_WORKERS")); err == nil && n > 0 {
		return min(n, 16)
	}
	return 2
}

func seriesBGDelay() time.Duration {
	if n, err := strconv.Atoi(os.Getenv("XTREAM_SERIES_BG_DELAY_MS")); err == nil && n >= 50 {
		return time.Duration(n) * time.Millisecond
	}
	return 250 * time.Millisecond
}

// populateSeriesLoop trickle-fetches series info at a capped rate, then reingests.
func (instance *Updater) populateSeriesLoop(ctx context.Context) {
	for ctx.Err() == nil {
		time.Sleep(30 * time.Second)
		if ctx.Err() == nil {
			start := time.Now()
			fetched, ok := instance.tryPopulateSeriesPass(ctx)
			if !ok {
				instance.logger.Log("Series populate: M3U update active; deferring pass")
				continue
			}
			switch {
			case fetched > 0:
				instance.logger.Logf("Series populate: pass fetched %d series in %s, reingesting playlist", fetched, time.Since(start).Round(time.Second))
				instance.UpdateM3USources(ctx)
			case ctx.Err() == nil:
				instance.logger.Logf("Series populate: catalog up to date (%s)", time.Since(start).Round(time.Second))
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(populatePassGap):
		}
	}
}

func (instance *Updater) tryPopulateSeriesPass(ctx context.Context) (int, bool) {
	if !instance.m3uMu.TryLock() {
		return 0, false
	}
	defer instance.m3uMu.Unlock()
	return instance.populateSeriesPass(ctx), true
}

func (instance *Updater) populateSeriesPass(ctx context.Context) int {
	files, _ := filepath.Glob(filepath.Join(config.GetSeriesCacheDirPath(), "stubs-*.bin"))
	if len(files) == 0 {
		return 0
	}
	instance.logger.Logf("Series populate: pass starting (%d source file(s))", len(files))
	var (
		mu    sync.Mutex
		wg    sync.WaitGroup
		total int
	)
	for _, f := range files {
		idx := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(f), "stubs-"), ".bin")
		wg.Add(1)
		go func(f, idx string) {
			defer wg.Done()
			n := instance.populateSource(ctx, f, idx)
			mu.Lock()
			total += n
			mu.Unlock()
		}(f, idx)
	}
	wg.Wait()
	return total
}

// populateSource refetches every valid stub each pass so new episodes appear eventually.
func (instance *Updater) populateSource(ctx context.Context, stubPath, idx string) int {
	host := strings.TrimSuffix(os.Getenv("XTREAM_URL_"+idx), "/")
	if host == "" {
		return 0
	}
	client := xtream.NewClient(host, os.Getenv("XTREAM_USERNAME_"+idx), os.Getenv("XTREAM_PASSWORD_"+idx))
	stubs, err := xtream.ReadSeriesStubs(stubPath)
	if err != nil || len(stubs) == 0 {
		return 0
	}
	instance.logger.Logf("Series populate: refreshing %d series for source %s", len(stubs), idx)

	jobs := make(chan xtream.SeriesStub, len(stubs))
	var (
		mu         sync.Mutex
		fetched    = make(map[uint64]xtream.FragmentEntry)
		successes  int
		failStreak int
		wg         sync.WaitGroup
	)
	fragPath := filepath.Join(config.GetSeriesCacheDirPath(), "frag-"+idx+".m3u")
	defer func() {
		valid := make(map[uint64]struct{}, len(stubs))
		for _, s := range stubs {
			valid[s.UpstreamID] = struct{}{}
		}
		if err := xtream.CompactSeriesFragment(fragPath, valid); err != nil {
			instance.logger.Errorf("series populate fragment compact failed for %s: %v", idx, err)
		}
	}()
	flush := func() {
		mu.Lock()
		if len(fetched) == 0 {
			mu.Unlock()
			return
		}
		entries := make([]xtream.FragmentEntry, 0, len(fetched))
		for _, e := range fetched {
			entries = append(entries, e)
		}
		done := successes
		// Appending bounds this pass to one batch in RAM; compaction runs
		// once at pass end instead of rewriting the whole cache every batch.
		clear(fetched)
		mu.Unlock()
		if err := xtream.AppendSeriesFragment(fragPath, entries); err != nil {
			instance.logger.Errorf("series populate fragment write failed for %s: %v", idx, err)
		} else {
			instance.logger.Logf("Series populate: source %s at %d/%d", idx, done, len(stubs))
		}
	}

	tick := time.NewTicker(seriesBGDelay())
	defer tick.Stop()
	for range seriesBGWorkers() {
		wg.Go(func() {
			backoff := seriesBGDelay()
			for stub := range jobs {
				select {
				case <-tick.C:
				case <-ctx.Done():
					return
				}
				info, err := client.SeriesInfo(ctx, strconv.FormatUint(stub.UpstreamID, 10))
				mu.Lock()
				if err != nil {
					failStreak++
					streak := failStreak
					successesNow := successes
					mu.Unlock()
					if streak >= populateStreakLimit {
						instance.logger.Warnf("series populate aborted for %s after %d consecutive failures", idx, streak)
						return
					}
					if successesNow == 0 {
						time.Sleep(min(backoff, populateBackoffMax))
						backoff *= 2
					} else {
						backoff = seriesBGDelay()
					}
					continue
				}
				failStreak = 0
				backoff = seriesBGDelay()
				successes++
				done := successes
				fetched[stub.UpstreamID] = xtream.FragmentEntry{
					UpstreamID: stub.UpstreamID,
					Lines:      xtream.SeriesToLines(client, stub.Name, stub.Group, info),
				}
				mu.Unlock()
				if done%populateBatchSize == 0 {
					flush()
				}
			}
		})
	}
	for _, stub := range stubs {
		select {
		case jobs <- stub:
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			flush()
			mu.Lock()
			defer mu.Unlock()
			return successes
		}
	}
	close(jobs)
	wg.Wait()
	flush()
	mu.Lock()
	defer mu.Unlock()
	if successes > 0 {
		instance.logger.Logf("Series populate: source %s done, %d fetched", idx, successes)
	}
	return successes
}
