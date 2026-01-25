package sourceproc

import (
	"m3u-stream-merger/config"
	"sync"

	"github.com/gofrs/flock"
)

var lockFile *flock.Flock
var mu sync.Mutex

func init() {
	lockFile = flock.New(config.GetLockFile())
}

func lockSources() {
	mu.Lock()
	defer mu.Unlock()

	lockFile.Lock()
}

func unlockSources() {
	mu.Lock()
	defer mu.Unlock()

	lockFile.Unlock()
}
