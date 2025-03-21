package safemap

import (
	"github.com/puzpuzpuz/xsync/v3"
)

// Map is a thread-safe map implementation
type Map[K comparable, V any] struct {
	internal *xsync.MapOf[K, V]
}

// New creates a new Map instance.
func New[K comparable, V any]() *Map[K, V] {
	return &Map[K, V]{
		internal: xsync.NewMapOf[K, V](),
	}
}

// Set sets the value for a given key in the map.
func (sm *Map[K, V]) Set(key K, value V) {
	sm.internal.Store(key, value)
}

// Get retrieves the value for a given key from the map.
// The second return value indicates whether the key was found.
func (sm *Map[K, V]) Get(key K) (V, bool) {
	return sm.internal.Load(key)
}

// GetOrSet retrieves the value for a given key if it exists.
// If the key does not exist, it sets the key with the provided value and returns it.
// The second return value indicates whether the value was loaded (true) or set (false).
func (sm *Map[K, V]) GetOrSet(key K, value V) (actual V, loaded bool) {
	return sm.internal.LoadOrStore(key, value)
}

// GetAndDel deletes the key from the map and returns the previous value if it existed.
func (sm *Map[K, V]) GetAndDel(key K) (value V, ok bool) {
	return sm.internal.LoadAndDelete(key)
}

// GetOrCompute retrieves the value for a key or computes and sets it if it does not exist.
func (sm *Map[K, V]) GetOrCompute(key K, valueFn func() V) (actual V, loaded bool) {
	return sm.internal.LoadOrCompute(key, valueFn)
}

func (sm *Map[K, V]) Compute(key K, valueFn func(oldValue V, loaded bool) (newValue V, del bool)) (actual V, loaded bool) {
	return sm.internal.Compute(key, valueFn)
}

// Del removes a key from the map.
func (sm *Map[K, V]) Del(key K) {
	sm.internal.Delete(key)
}

// Len returns the total number of key-value pairs in the map.
func (sm *Map[K, V]) Len() int {
	return sm.internal.Size()
}

// ForEach iterates over all key-value pairs in the map and applies the given function.
// The iteration stops if the function returns false.
func (sm *Map[K, V]) ForEach(fn func(K, V) bool) {
	sm.internal.Range(fn)
}

// Clear removes all key-value pairs from the map.
func (sm *Map[K, V]) Clear() {
	sm.internal.Clear()
}
