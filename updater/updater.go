package updater

import (
	"context"
	"m3u-stream-merger/logger"
	"m3u-stream-merger/store"
	"m3u-stream-merger/utils"
	"os"
	"strings"
	"sync"

	"github.com/robfig/cron/v3"
)

type Updater struct {
	sync.Mutex
	ctx    context.Context
	Cron   *cron.Cron
	logger logger.Logger
}

func Initialize(ctx context.Context, logger logger.Logger) (*Updater, error) {
	updateInstance := &Updater{
		ctx:    ctx,
		logger: logger,
	}

	clearOnBoot := os.Getenv("CLEAR_ON_BOOT")
	if len(strings.TrimSpace(clearOnBoot)) == 0 {
		clearOnBoot = "false"
	}

	if clearOnBoot == "true" {
		updateInstance.logger.Log("CLEAR_ON_BOOT enabled. Clearing current cache.")
		store.ClearCache()
	}

	cronSched := os.Getenv("SYNC_CRON")
	if len(strings.TrimSpace(cronSched)) == 0 {
		updateInstance.logger.Log("SYNC_CRON not initialized. Defaulting to 0 0 * * * (12am every day).")
		cronSched = "0 0 * * *"
	}

	c := cron.New()
	_, err := c.AddFunc(cronSched, func() {
		go updateInstance.UpdateSources(ctx)
	})
	if err != nil {
		updateInstance.logger.Logf("Error initializing background processes: %v", err)
		return nil, err
	}
	c.Start()

	syncOnBoot := os.Getenv("SYNC_ON_BOOT")
	if len(strings.TrimSpace(syncOnBoot)) == 0 {
		syncOnBoot = "true"
	}

	if syncOnBoot == "true" {
		updateInstance.logger.Log("SYNC_ON_BOOT enabled. Starting initial M3U update.")

		go updateInstance.UpdateSources(ctx)
	}

	updateInstance.Cron = c

	return updateInstance, nil
}

func (instance *Updater) UpdateSources(ctx context.Context) {
	// Ensure only one job is running at a time
	instance.Lock()
	defer instance.Unlock()

	select {
	case <-ctx.Done():
		return
	default:
		instance.logger.Log("Background process: Checking M3U_URLs...")
		var wg sync.WaitGroup

		indexes := utils.GetM3UIndexes()
		for _, idx := range indexes {
			instance.logger.Logf("Background process: Fetching M3U_URL_%s...", idx)
			wg.Add(1)
			// Start the goroutine for periodic updates
			go func(idx string) {
				defer wg.Done()
				err := store.DownloadM3USource(idx)
				if err != nil {
					instance.logger.Debugf("Background process: Error fetching M3U_URL_%s: %v", idx, err)
				}
			}(idx)
		}
		wg.Wait()

		instance.logger.Logf("Background process: M3U fetching complete.")

		store.ClearSessionStore()

		cacheOnSync := os.Getenv("CACHE_ON_SYNC")
		if len(strings.TrimSpace(cacheOnSync)) == 0 {
			cacheOnSync = "false"
		}

		instance.logger.Log("Background process: Updated M3U store.")
		if cacheOnSync == "true" {
			if _, ok := os.LookupEnv("BASE_URL"); !ok {
				instance.logger.Log("BASE_URL is required for CACHE_ON_SYNC to work.")
			}
			instance.logger.Log("CACHE_ON_SYNC enabled. Building cache.")
			_ = store.RevalidatingGetM3U(nil, true)
		}
	}
}
