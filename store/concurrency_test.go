package store

import (
	"sync"
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
		cm.UpdateConcurrency("TEST1", true)
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

// TestConcurrencyManagerPriority tests priority value calculations
func TestConcurrencyManagerPriority(t *testing.T) {
	t.Setenv("M3U_MAX_CONCURRENCY_PRIO", "3")
	cm := NewConcurrencyManager()

	// Initial priority (count = 0) should be 0
	if prio := cm.ConcurrencyPriorityValue("PRIO"); prio != 0 {
		t.Errorf("Expected priority 0 for count=0, got %d", prio)
	}

	// Add one connection (count = 1) should give highest priority (2)
	cm.UpdateConcurrency("PRIO", true)
	if prio := cm.ConcurrencyPriorityValue("PRIO"); prio != 2 {
		t.Errorf("Expected priority 2 for count=1, got %d", prio)
	}

	// Add another connection (count > 1) should give medium priority (1)
	cm.UpdateConcurrency("PRIO", true)
	if prio := cm.ConcurrencyPriorityValue("PRIO"); prio != 1 {
		t.Errorf("Expected priority 1 for count>1, got %d", prio)
	}

	// Back to one connection should restore highest priority (2)
	cm.UpdateConcurrency("PRIO", false)
	if prio := cm.ConcurrencyPriorityValue("PRIO"); prio != 2 {
		t.Errorf("Expected priority 2 for count=1, got %d", prio)
	}
}

// TestConcurrencyManagerRaceConditions tests for race conditions
func TestConcurrencyManagerRaceConditions(t *testing.T) {
	t.Setenv("M3U_MAX_CONCURRENCY_STRESS", "100")
	cm := NewConcurrencyManager()
	var wg sync.WaitGroup

	// Start 150 concurrent requests
	for i := 0; i < 150; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cm.UpdateConcurrency("STRESS", true)
		}()
	}

	wg.Wait()
	if count := cm.GetCount("STRESS"); count != 150 {
		t.Errorf("Expected 150 connections, got %d", count)
	}

	// Decrement all
	for i := 0; i < 150; i++ {
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
