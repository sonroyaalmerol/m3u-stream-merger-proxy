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

func LockSources() {
	mu.Lock()
	defer mu.Unlock()

	_ = lockFile.Lock()
}

func UnlockSources() {
	mu.Lock()
	defer mu.Unlock()

	_ = lockFile.Unlock()
}
