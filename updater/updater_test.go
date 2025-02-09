package updater

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"m3u-stream-merger/config"
	"m3u-stream-merger/logger"
)

// TestLogger is a simple logger to capture log messages for testing.
type TestLogger struct {
	mu   sync.Mutex
	logs []string

	logger.DefaultLogger
}

func (tl *TestLogger) Log(s string) {
	tl.mu.Lock()
	defer tl.mu.Unlock()
	tl.logs = append(tl.logs, s)
}

func (tl *TestLogger) Logf(format string, a ...interface{}) {
	tl.Log(fmt.Sprintf(format, a...))
}

func (tl *TestLogger) Debugf(format string, a ...interface{}) {
	tl.Log(fmt.Sprintf(format, a...))
}

func (tl *TestLogger) Reset() {
	tl.mu.Lock()
	defer tl.mu.Unlock()
	tl.logs = []string{}
}

func (tl *TestLogger) GetLogs() []string {
	tl.mu.Lock()
	defer tl.mu.Unlock()
	out := make([]string, len(tl.logs))
	copy(out, tl.logs)
	return out
}

// testLogger is our global logger instance used in tests.
var testLogger = &TestLogger{}

// TestInitialize_Defaults verifies that when no environment variables are set,
// default values are used. In particular it checks for log messages regarding default
// SYNC_CRON ("0 0 * * *") and SYNC_ON_BOOT (enabled by default).
func TestInitialize_Defaults(t *testing.T) {
	// Create temporary directories for config.
	tempDataDir := t.TempDir()
	tempTmpDir := t.TempDir()
	config.SetConfig(&config.Config{
		DataPath: tempDataDir,
		TempPath: tempTmpDir,
	})

	// Replace the default logger.
	testLogger.Reset()
	logger.Default.Logger = testLogger

	// Clear the environment variables so defaults are used.
	t.Setenv("CLEAR_ON_BOOT", "")
	t.Setenv("SYNC_CRON", "")
	t.Setenv("SYNC_ON_BOOT", "")
	t.Setenv("CACHE_ON_SYNC", "")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	up, err := Initialize(ctx, testLogger)
	if err != nil {
		t.Fatalf("Initialize returned error: %v", err)
	}

	// Allow some time for asynchronous update if SYNC_ON_BOOT is enabled.
	time.Sleep(100 * time.Millisecond)

	// Check for the expected log messages.
	logs := testLogger.GetLogs()
	foundCronWarning := false
	foundSyncBootStart := false
	for _, msg := range logs {
		if strings.Contains(msg, "SYNC_CRON not initialized") {
			foundCronWarning = true
		}
		if strings.Contains(msg, "SYNC_ON_BOOT enabled") {
			foundSyncBootStart = true
		}
	}
	if !foundCronWarning {
		t.Error("Expected log message about default SYNC_CRON not found")
	}
	if !foundSyncBootStart {
		t.Error("Expected log message about SYNC_ON_BOOT enabled not found")
	}

	// Clean up the cron job.
	up.Cron.Stop()
}

// TestInitialize_ClearOnBoot verifies that when CLEAR_ON_BOOT is set to true the
// log reflects that the cache is being cleared.
func TestInitialize_ClearOnBoot(t *testing.T) {
	tempDataDir := t.TempDir()
	tempTmpDir := t.TempDir()
	config.SetConfig(&config.Config{
		DataPath: tempDataDir,
		TempPath: tempTmpDir,
	})

	testLogger.Reset()
	logger.Default.Logger = testLogger

	t.Setenv("CLEAR_ON_BOOT", "true")
	t.Setenv("SYNC_CRON", "")
	t.Setenv("SYNC_ON_BOOT", "false") // disable automatic sync

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	up, err := Initialize(ctx, testLogger)
	if err != nil {
		t.Fatalf("Initialize returned error: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	logs := testLogger.GetLogs()
	foundClearCache := false
	for _, msg := range logs {
		if strings.Contains(msg, "CLEAR_ON_BOOT enabled. Clearing current cache.") {
			foundClearCache = true
			break
		}
	}
	if !foundClearCache {
		t.Error("Expected log message for CLEAR_ON_BOOT enabled not found")
	}
	up.Cron.Stop()
}

// TestInitialize_InvalidCron verifies that an invalid cron schedule causes Initialize
// to return an error.
func TestInitialize_InvalidCron(t *testing.T) {
	tempDataDir := t.TempDir()
	tempTmpDir := t.TempDir()
	config.SetConfig(&config.Config{
		DataPath: tempDataDir,
		TempPath: tempTmpDir,
	})

	testLogger.Reset()
	logger.Default.Logger = testLogger

	t.Setenv("SYNC_CRON", "invalid-cron")
	t.Setenv("SYNC_ON_BOOT", "false")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_, err := Initialize(ctx, testLogger)
	if err == nil {
		t.Error("Expected error from Initialize when using invalid cron schedule, got none")
	}
}

// TestUpdateSources_ContextCancelled verifies that if the context is cancelled before
// UpdateSources runs its work, then no processing (or logging) occurs.
func TestUpdateSources_ContextCancelled(t *testing.T) {
	testLogger.Reset()
	logger.Default.Logger = testLogger

	// Create a context that is cancelled immediately.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	up := &Updater{ctx: ctx, logger: testLogger}
	up.UpdateSources(ctx)

	// There should be no log messages if the context was cancelled.
	for _, msg := range testLogger.GetLogs() {
		if strings.Contains(msg, "Background process: Checking M3U_URLs") {
			t.Error("UpdateSources ran despite cancelled context")
		}
	}
}

// TestUpdateSources_CacheOnSync_WithBaseURL verifies that when CACHE_ON_SYNC is true
// and BASE_URL is set, the cache-building branch is executed.
func TestUpdateSources_CacheOnSync_WithBaseURL(t *testing.T) {
	testLogger.Reset()

	t.Setenv("CACHE_ON_SYNC", "true")
	t.Setenv("BASE_URL", "http://example.com")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	up := &Updater{ctx: ctx, logger: testLogger}
	up.UpdateSources(ctx)

	logs := testLogger.GetLogs()
	foundCacheEnabled := false
	foundBaseURLMessage := false

	for _, msg := range logs {
		if strings.Contains(msg, "CACHE_ON_SYNC enabled. Building cache.") {
			foundCacheEnabled = true
		}
		if strings.Contains(msg, "BASE_URL is required") {
			foundBaseURLMessage = true
		}
	}
	if !foundCacheEnabled {
		t.Error("Expected log message for CACHE_ON_SYNC enabled not found")
	}
	if foundBaseURLMessage {
		t.Error("BASE_URL error message was logged unexpectedly")
	}
}

// TestUpdateSources_CacheOnSync_WithoutBaseURL verifies that when CACHE_ON_SYNC is true
// but BASE_URL is missing, the updater logs that BASE_URL is required.
func TestUpdateSources_CacheOnSync_WithoutBaseURL(t *testing.T) {
	testLogger.Reset()
	logger.Default.Logger = testLogger

	t.Setenv("CACHE_ON_SYNC", "true")
	// Ensure BASE_URL is not set.
	os.Unsetenv("BASE_URL")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	up := &Updater{ctx: ctx, logger: testLogger}
	up.UpdateSources(ctx)

	logs := testLogger.GetLogs()
	foundBaseURLRequired := false
	for _, msg := range logs {
		if strings.Contains(msg, "BASE_URL is required for CACHE_ON_SYNC to work.") {
			foundBaseURLRequired = true
			break
		}
	}
	if !foundBaseURLRequired {
		t.Error("Expected log message for missing BASE_URL not found")
	}

	foundCacheEnabled := false
	for _, msg := range logs {
		if strings.Contains(msg, "CACHE_ON_SYNC enabled. Building cache.") {
			foundCacheEnabled = true
			break
		}
	}
	if !foundCacheEnabled {
		t.Error("Expected log message for CACHE_ON_SYNC enabled not found")
	}
}

// TestUpdateSources_NormalFlow runs the UpdateSources method (with a non‑cancelled context)
// and checks that the typical log messages (such as starting the update and completion)
// are present.
func TestUpdateSources_NormalFlow(t *testing.T) {
	testLogger.Reset()
	logger.Default.Logger = testLogger

	t.Setenv("CACHE_ON_SYNC", "false")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	up := &Updater{ctx: ctx, logger: testLogger}
	up.UpdateSources(ctx)

	logs := testLogger.GetLogs()
	foundStart := false
	foundComplete := false

	for _, msg := range logs {
		if strings.Contains(msg, "Background process: Checking M3U_URLs") {
			foundStart = true
		}
		if strings.Contains(msg, "Background process: M3U fetching complete.") {
			foundComplete = true
		}
	}
	if !foundStart {
		t.Error("Expected log message for starting M3U update not found")
	}
	if !foundComplete {
		t.Error("Expected log message for M3U fetching complete not found")
	}
}
