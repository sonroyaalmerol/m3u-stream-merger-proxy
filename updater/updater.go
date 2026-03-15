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
	m3uMu      sync.Mutex
	epgMu      sync.Mutex
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

	m3uCron := os.Getenv("SYNC_CRON")
	if len(strings.TrimSpace(m3uCron)) == 0 {
		updateInstance.logger.Log("SYNC_CRON not initialized. Defaulting to 0 0 * * * (12am every day).")
		m3uCron = "0 0 * * *"
	}

	// EPG_SYNC_CRON is optional; falls back to the M3U schedule when unset.
	epgCron := strings.TrimSpace(os.Getenv("EPG_SYNC_CRON"))
	separateEPGCron := epgCron != "" && epgCron != m3uCron
	if separateEPGCron {
		updateInstance.logger.Logf("EPG_SYNC_CRON set to %q (independent of SYNC_CRON).", epgCron)
	}

	c := cron.New()

	if _, err := c.AddFunc(m3uCron, func() {
		go updateInstance.UpdateM3USources(ctx)
	}); err != nil {
		updateInstance.logger.Logf("Error initializing M3U cron: %v", err)
		return nil, err
	}

	if separateEPGCron {
		if _, err := c.AddFunc(epgCron, func() {
			go updateInstance.UpdateEPGSources(ctx)
		}); err != nil {
			updateInstance.logger.Logf("Error initializing EPG cron: %v", err)
			return nil, err
		}
	}

	c.Start()

	syncOnBoot := os.Getenv("SYNC_ON_BOOT")
	if len(strings.TrimSpace(syncOnBoot)) == 0 {
		syncOnBoot = "true"
	}

	if syncOnBoot == "true" {
		updateInstance.logger.Log("SYNC_ON_BOOT enabled. Starting initial update.")
		go updateInstance.UpdateM3USources(ctx)
		if separateEPGCron {
			go updateInstance.UpdateEPGSources(ctx)
		}
	}

	updateInstance.Cron = c
	return updateInstance, nil
}

// UpdateM3USources rebuilds the merged M3U playlist.  When M3U and EPG share
// the same cron schedule it also triggers an EPG update immediately after so
// the new tvg-id filter is applied without waiting for a separate EPG cron.
func (instance *Updater) UpdateM3USources(ctx context.Context) {
	instance.m3uMu.Lock()
	defer instance.m3uMu.Unlock()

	processor := sourceproc.NewProcessor()
	select {
	case <-ctx.Done():
		return
	default:
		instance.logger.Log("Background process: Updating M3U sources...")

		if _, ok := os.LookupEnv("BASE_URL"); !ok {
			instance.logger.Error("BASE_URL is required for M3U processing to work.")
			return
		}
		if err := processor.Run(ctx, nil); err == nil {
			instance.m3uHandler.SetProcessedPath(processor.GetResultPath())
		}

		// When EPG is on the same schedule, run it inline so the freshly
		// written tvg-ids are picked up immediately.
		epgCron := strings.TrimSpace(os.Getenv("EPG_SYNC_CRON"))
		m3uCron := strings.TrimSpace(os.Getenv("SYNC_CRON"))
		if epgCron == "" || epgCron == m3uCron {
			instance.runEPG(ctx)
		}
	}
}

// UpdateEPGSources rebuilds the merged EPG independently of the M3U schedule.
// Called by the separate EPG cron when EPG_SYNC_CRON differs from SYNC_CRON.
func (instance *Updater) UpdateEPGSources(ctx context.Context) {
	instance.epgMu.Lock()
	defer instance.epgMu.Unlock()

	select {
	case <-ctx.Done():
		return
	default:
		instance.runEPG(ctx)
	}
}

func (instance *Updater) runEPG(ctx context.Context) {
	instance.logger.Log("Background process: Building merged EPG...")
	epgProcessor := epg.NewProcessor(instance.logger)
	if err := epgProcessor.Run(ctx); err != nil {
		instance.logger.Warnf("EPG update failed (non-fatal): %v", err)
	} else {
		instance.epgHandler.SetProcessedPath(config.GetEPGPath())
	}
}
