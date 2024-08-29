package updater

import (
	"context"
	"fmt"
	"log"
	"m3u-stream-merger/database"
	"m3u-stream-merger/m3u"
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

func Initialize(ctx context.Context) *Updater {
	db, err := database.InitializeDb()
	if err != nil {
		log.Fatalf("Error initializing Redis database: %v", err)
	}

	clearOnBoot := os.Getenv("CLEAR_ON_BOOT")
	if len(strings.TrimSpace(clearOnBoot)) == 0 {
		clearOnBoot = "false"
	}

	if clearOnBoot == "true" {
		log.Println("CLEAR_ON_BOOT enabled. Clearing current database.")
		if err := db.ClearDb(); err != nil {
			log.Fatalf("Error clearing database: %v", err)
		}
	}

	cacheOnSync := os.Getenv("CACHE_ON_SYNC")
	if len(strings.TrimSpace(cacheOnSync)) == 0 {
		cacheOnSync = "false"
	}

	cronSched := os.Getenv("SYNC_CRON")
	if len(strings.TrimSpace(cronSched)) == 0 {
		log.Println("SYNC_CRON not initialized. Defaulting to 0 0 * * * (12am every day).")
		cronSched = "0 0 * * *"
	}

	updateInstance := &Updater{
		ctx:       ctx,
		db:        db,
		M3UParser: m3u.InitializeParser(),
	}

	c := cron.New()
	_, err = c.AddFunc(cronSched, func() {
		var wg sync.WaitGroup

		wg.Add(1)
		go updateInstance.UpdateSources(ctx)
		if cacheOnSync == "true" {
			if _, ok := os.LookupEnv("BASE_URL"); !ok {
				log.Println("BASE_URL is required for CACHE_ON_SYNC to work.")
			}
			wg.Wait()
			log.Println("CACHE_ON_SYNC enabled. Building cache.")
			m3u.InitCache(db)
		}
	})
	if err != nil {
		log.Fatalf("Error initializing background processes: %v", err)
	}
	c.Start()

	syncOnBoot := os.Getenv("SYNC_ON_BOOT")
	if len(strings.TrimSpace(syncOnBoot)) == 0 {
		syncOnBoot = "true"
	}

	if syncOnBoot == "true" {
		log.Println("SYNC_ON_BOOT enabled. Starting initial M3U update.")

		var wg sync.WaitGroup

		wg.Add(1)
		go updateInstance.UpdateSources(ctx)
		if cacheOnSync == "true" {
			if _, ok := os.LookupEnv("BASE_URL"); !ok {
				log.Println("BASE_URL is required for CACHE_ON_SYNC to work.")
			}
			wg.Wait()
			log.Println("CACHE_ON_SYNC enabled. Building cache.")
			m3u.InitCache(db)
		}
	}

	updateInstance.Cron = c

	return updateInstance
}

func (instance *Updater) UpdateSource(m3uUrl string, index int) {
	log.Printf("Background process: Updating M3U #%d from %s\n", index+1, m3uUrl)
	err := instance.M3UParser.ParseURL(m3uUrl, index)
	if err != nil {
		log.Printf("Background process: Error updating M3U: %v\n", err)
	} else {
		log.Printf("Background process: Updated M3U #%d from %s\n", index+1, m3uUrl)
	}
}

func (instance *Updater) UpdateSources(ctx context.Context) {
	// Ensure only one job is running at a time
	instance.Lock()
	defer instance.Unlock()

	db, err := database.InitializeDb()
	if err != nil {
		log.Println("Background process: Failed to initialize db connection.")
		return
	}

	select {
	case <-ctx.Done():
		return
	default:
		log.Println("Background process: Checking M3U_URLs...")
		var wg sync.WaitGroup
		index := 0

		for {
			m3uUrl, m3uExists := os.LookupEnv(fmt.Sprintf("M3U_URL_%d", index+1))
			if !m3uExists {
				break
			}

			log.Printf("Background process: Fetching M3U_URL_%d...\n", index+1)
			wg.Add(1)
			// Start the goroutine for periodic updates
			go func(m3uUrl string, index int) {
				log.Println(index)
				defer wg.Done()
				instance.UpdateSource(m3uUrl, index)
			}(m3uUrl, index)

			index++
		}
		wg.Wait()

		log.Printf("Background process: M3U fetching complete. Saving to database...\n")

		err := db.SaveToDb(instance.M3UParser.GetStreams())
		if err != nil {
			log.Printf("Background process: Error updating M3U database: %v\n", err)
			log.Println("Background process: Clearing database after error in attempt to fix issue after container restart.")

			if err := db.ClearDb(); err != nil {
				log.Printf("Background process: Error clearing database: %v\n", err)
			}
		}

		m3u.ClearCache()

		log.Println("Background process: Updated M3U database.")
	}
}
