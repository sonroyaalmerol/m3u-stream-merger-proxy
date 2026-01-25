package store

import (
	"sync"
	"sync/atomic"
	"testing"
)

// TestConcurrencyManagerBasicOps tests basic increment/decrement functionality
func TestConcurrencyManagerBasicOps(t *testing.T) {
	t.Setenv("M3U_MAX_CONCURRENCY_TEST1", "2")
	cm := NewConcurrencyManager()

	t.Run("InitialState", func(t *testing.T) {
		if cm.CheckConcurrency("TEST1") {
			t.Error("New playlist should have 0 connections")
		}
	})

	t.Run("SingleIncrement", func(t *testing.T) {
		if !cm.UpdateConcurrency("TEST1", true) {
			t.Error("Should successfully increment")
		}
		if cm.GetCount("TEST1") != 1 {
			t.Errorf("Expected 1 connection, got %d", cm.GetCount("TEST1"))
		}
	})

	t.Run("DecrementBelowZero", func(t *testing.T) {
		cm.UpdateConcurrency("TEST1", false)
		cm.UpdateConcurrency("TEST1", false) // Shouldn't go below 0
		if cm.GetCount("TEST1") != 0 {
			t.Errorf("Expected 0 connections, got %d", cm.GetCount("TEST1"))
		}
	})
}

// TestConcurrencyManagerLimit tests that the limit is enforced
func TestConcurrencyManagerLimit(t *testing.T) {
	t.Setenv("M3U_MAX_CONCURRENCY_LIMIT", "2")
	cm := NewConcurrencyManager()

	// First two should succeed
	if !cm.UpdateConcurrency("LIMIT", true) {
		t.Error("First increment should succeed")
	}
	if !cm.UpdateConcurrency("LIMIT", true) {
		t.Error("Second increment should succeed")
	}

	// Third should fail
	if cm.UpdateConcurrency("LIMIT", true) {
		t.Error("Third increment should fail (at limit)")
	}

	if count := cm.GetCount("LIMIT"); count != 2 {
		t.Errorf("Expected 2 connections (at limit), got %d", count)
	}

	// After decrement, should be able to increment again
	cm.UpdateConcurrency("LIMIT", false)
	if !cm.UpdateConcurrency("LIMIT", true) {
		t.Error("Should succeed after decrement")
	}
}

// TestConcurrencyManagerPriority tests priority value calculations
func TestConcurrencyManagerPriority(t *testing.T) {
	t.Setenv("M3U_MAX_CONCURRENCY_PRIO", "3")
	cm := NewConcurrencyManager()

	// Initial priority should be max concurrency (3 - 0 = 3)
	if prio := cm.ConcurrencyPriorityValue("PRIO"); prio != 3 {
		t.Errorf("Expected priority 3, got %d", prio)
	}

	cm.UpdateConcurrency("PRIO", true)
	if prio := cm.ConcurrencyPriorityValue("PRIO"); prio != 2 {
		t.Errorf("Expected priority 2, got %d", prio)
	}
}

// TestConcurrencyManagerRaceConditions tests for race conditions
func TestConcurrencyManagerRaceConditions(t *testing.T) {
	t.Setenv("M3U_MAX_CONCURRENCY_STRESS", "100")
	cm := NewConcurrencyManager()
	var wg sync.WaitGroup

	successCount := int32(0)

	// Start 150 concurrent requests (50 should fail)
	for i := 0; i < 150; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if cm.UpdateConcurrency("STRESS", true) {
				atomic.AddInt32(&successCount, 1)
			}
		}()
	}

	wg.Wait()

	// Should only have 100 successful increments (the limit)
	if count := cm.GetCount("STRESS"); count != 100 {
		t.Errorf("Expected 100 connections (limit), got %d", count)
	}

	if successCount != 100 {
		t.Errorf("Expected 100 successful increments, got %d", successCount)
	}

	// Decrement all
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cm.UpdateConcurrency("STRESS", false)
		}()
	}

	wg.Wait()
	if count := cm.GetCount("STRESS"); count != 0 {
		t.Errorf("Expected 0 connections, got %d", count)
	}
}
