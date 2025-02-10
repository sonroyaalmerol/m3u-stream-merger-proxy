package updater

import (
	"context"
	"m3u-stream-merger/logger"
	sourceproc "m3u-stream-merger/source_processor"
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
		sourceproc.ClearCache()
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
		instance.logger.Log("Background process: Updating sources...")
		sourceproc.ClearCache()

		cacheOnSync := os.Getenv("CACHE_ON_SYNC")
		if len(strings.TrimSpace(cacheOnSync)) == 0 {
			cacheOnSync = "false"
		}

		instance.logger.Log("Background process: Building merged M3U...")
		if cacheOnSync == "true" {
			if _, ok := os.LookupEnv("BASE_URL"); !ok {
				instance.logger.Log("BASE_URL is required for CACHE_ON_SYNC to work.")
			}
			instance.logger.Log("CACHE_ON_SYNC enabled. Building cache.")
			_ = sourceproc.RevalidatingGetM3U(nil, true)
		}
	}
}
