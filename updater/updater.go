package updater

import (
	"context"
	"m3u-stream-merger/config"
	"m3u-stream-merger/epg"
	"m3u-stream-merger/handlers"
	"m3u-stream-merger/logger"
	"m3u-stream-merger/sourceproc"
	"os"
	"strings"
	"sync"

	"github.com/robfig/cron/v3"
)

type Updater struct {
	sync.Mutex
	ctx        context.Context
	Cron       *cron.Cron
	logger     logger.Logger
	m3uHandler *handlers.M3UHTTPHandler
	epgHandler *handlers.EPGHTTPHandler
}

func Initialize(ctx context.Context, logger logger.Logger, m3uHandler *handlers.M3UHTTPHandler, epgHandler *handlers.EPGHTTPHandler) (*Updater, error) {
	updateInstance := &Updater{
		ctx:        ctx,
		logger:     logger,
		m3uHandler: m3uHandler,
		epgHandler: epgHandler,
	}

	clearOnBoot := os.Getenv("CLEAR_ON_BOOT")
	if len(strings.TrimSpace(clearOnBoot)) == 0 {
		clearOnBoot = "false"
	}

	if clearOnBoot == "true" {
		updateInstance.logger.Log("CLEAR_ON_BOOT enabled. Clearing current cache.")
		sourceproc.ClearProcessedM3Us()
	} else {
		sourceproc.LockSources()
		latestM3u, err := config.GetLatestProcessedM3UPath()
		sourceproc.UnlockSources()

		if err == nil {
			m3uHandler.SetProcessedPath(latestM3u)
		}

		// Restore EPG path if a previously merged file exists.
		if _, err := os.Stat(config.GetEPGPath()); err == nil {
			epgHandler.SetProcessedPath(config.GetEPGPath())
		}
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

	processor := sourceproc.NewProcessor()
	select {
	case <-ctx.Done():
		return
	default:
		instance.logger.Log("Background process: Updating sources...")

		instance.logger.Log("Background process: Building merged M3U...")
		if _, ok := os.LookupEnv("BASE_URL"); !ok {
			instance.logger.Error("BASE_URL is required for M3U processing to work.")
			return
		}
		if err := processor.Run(ctx, nil); err == nil {
			instance.m3uHandler.SetProcessedPath(processor.GetResultPath())
		}

		instance.logger.Log("Background process: Building merged EPG...")
		epgProcessor := epg.NewProcessor(instance.logger)
		if err := epgProcessor.Run(ctx); err != nil {
			instance.logger.Warnf("EPG update failed (non-fatal): %v", err)
		} else {
			instance.epgHandler.SetProcessedPath(config.GetEPGPath())
		}
	}
}
