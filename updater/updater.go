package updater

import (
	"context"
	"fmt"
	"m3u-stream-merger/database"
	"m3u-stream-merger/m3u"
	"m3u-stream-merger/utils"
	"os"
	"strings"
	"sync"

	"github.com/robfig/cron/v3"
)

type Updater struct {
	sync.Mutex
	ctx       context.Context
	db        *database.Instance
	Cron      *cron.Cron
	M3UParser *m3u.Parser
}

func Initialize(ctx context.Context) (*Updater, error) {
	db, err := database.InitializeDb()
	if err != nil {
		utils.SafeLog("Error initializing Redis database: %v", err)
		return nil, err
	}

	clearOnBoot := os.Getenv("CLEAR_ON_BOOT")
	if len(strings.TrimSpace(clearOnBoot)) == 0 {
		clearOnBoot = "false"
	}

	if clearOnBoot == "true" {
		utils.SafeLogln("CLEAR_ON_BOOT enabled. Clearing current database.")
		if err := db.ClearDb(); err != nil {
			utils.SafeLog("Error clearing database: %v", err)
			return nil, err
		}
	}

	cronSched := os.Getenv("SYNC_CRON")
	if len(strings.TrimSpace(cronSched)) == 0 {
		utils.SafeLogln("SYNC_CRON not initialized. Defaulting to 0 0 * * * (12am every day).")
		cronSched = "0 0 * * *"
	}

	updateInstance := &Updater{
		ctx:       ctx,
		db:        db,
		M3UParser: m3u.InitializeParser(),
	}

	c := cron.New()
	_, err = c.AddFunc(cronSched, func() {
		go updateInstance.UpdateSources(ctx)
	})
	if err != nil {
		utils.SafeLog("Error initializing background processes: %v", err)
		return nil, err
	}
	c.Start()

	syncOnBoot := os.Getenv("SYNC_ON_BOOT")
	if len(strings.TrimSpace(syncOnBoot)) == 0 {
		syncOnBoot = "true"
	}

	if syncOnBoot == "true" {
		utils.SafeLogln("SYNC_ON_BOOT enabled. Starting initial M3U update.")

		go updateInstance.UpdateSources(ctx)
	}

	updateInstance.Cron = c

	return updateInstance, nil
}

func (instance *Updater) UpdateSource(m3uUrl string, index int) {
	utils.SafeLog("Background process: Updating M3U #%d from %s\n", index+1, m3uUrl)
	err := instance.M3UParser.ParseURL(m3uUrl, index)
	if err != nil {
		utils.SafeLog("Background process: Error updating M3U: %v\n", err)
	} else {
		utils.SafeLog("Background process: Updated M3U #%d from %s\n", index+1, m3uUrl)
	}
}

func (instance *Updater) UpdateSources(ctx context.Context) {
	// Ensure only one job is running at a time
	instance.Lock()
	defer instance.Unlock()

	db, err := database.InitializeDb()
	if err != nil {
		utils.SafeLogln("Background process: Failed to initialize db connection.")
		return
	}

	select {
	case <-ctx.Done():
		return
	default:
		utils.SafeLogln("Background process: Checking M3U_URLs...")
		var wg sync.WaitGroup
		index := 0

		for {
			m3uUrl, m3uExists := os.LookupEnv(fmt.Sprintf("M3U_URL_%d", index+1))
			if !m3uExists {
				break
			}

			utils.SafeLog("Background process: Fetching M3U_URL_%d...\n", index+1)
			wg.Add(1)
			// Start the goroutine for periodic updates
			go func(m3uUrl string, index int) {
				defer wg.Done()
				instance.UpdateSource(m3uUrl, index)
			}(m3uUrl, index)

			index++
		}
		wg.Wait()

		utils.SafeLog("Background process: M3U fetching complete. Saving to database...\n")

		err := db.SaveToDb(instance.M3UParser.GetStreams())
		if err != nil {
			utils.SafeLog("Background process: Error updating M3U database: %v\n", err)
			utils.SafeLogln("Background process: Clearing database after error in attempt to fix issue after container restart.")

			if err := db.ClearDb(); err != nil {
				utils.SafeLog("Background process: Error clearing database: %v\n", err)
			}
		}

		m3u.ClearCache()

		cacheOnSync := os.Getenv("CACHE_ON_SYNC")
		if len(strings.TrimSpace(cacheOnSync)) == 0 {
			cacheOnSync = "false"
		}

		utils.SafeLogln("Background process: Updated M3U database.")
		if cacheOnSync == "true" {
			if _, ok := os.LookupEnv("BASE_URL"); !ok {
				utils.SafeLogln("BASE_URL is required for CACHE_ON_SYNC to work.")
			}
			utils.SafeLogln("CACHE_ON_SYNC enabled. Building cache.")
			m3u.InitCache(db)
		}
	}
}
