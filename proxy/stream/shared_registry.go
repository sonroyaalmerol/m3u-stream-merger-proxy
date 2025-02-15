package stream

import (
	"m3u-stream-merger/logger"
	"m3u-stream-merger/store"
	"sync"
	"sync/atomic"
	"time"
)

type StreamRegistry struct {
	coordinators  sync.Map
	logger        logger.Logger
	config        *StreamConfig
	cleanupTicker *time.Ticker
	cm            *store.ConcurrencyManager
	done          chan struct{}

	Unrestrict bool
}

func NewStreamRegistry(config *StreamConfig, cm *store.ConcurrencyManager, logger logger.Logger, cleanupInterval time.Duration) *StreamRegistry {
	registry := &StreamRegistry{
		logger: logger,
		config: config,
		cm:     cm,
		done:   make(chan struct{}),
	}

	if cleanupInterval > 0 {
		registry.cleanupTicker = time.NewTicker(cleanupInterval)
		go registry.runCleanup()
	}

	return registry
}

func (r *StreamRegistry) GetOrCreateCoordinator(streamID string) *StreamCoordinator {
	coordId := streamID
	// if !r.Unrestrict {
	// 	streamInfo, err := sourceproc.DecodeSlug(streamID)
	// 	if err != nil {
	// 		r.logger.Logf("Invalid m3uID for GetOrCreateCoordinator from %s", streamID)
	// 		return nil
	// 	}

	// 	existingStreams := sourceproc.GetCurrentStreams()

	// 	if _, ok := existingStreams[streamInfo.Title]; !ok {
	// 		r.logger.Logf("Invalid m3uID for GetOrCreateCoordinator from %s", streamID)
	// 		return nil
	// 	}
	// 	coordId = streamInfo.Title
	// }

	if coord, ok := r.coordinators.Load(coordId); ok {
		return coord.(*StreamCoordinator)
	}

	coord := NewStreamCoordinator(coordId, r.config, r.cm, r.logger)

	actual, loaded := r.coordinators.LoadOrStore(coordId, coord)
	if loaded {
		return actual.(*StreamCoordinator)
	}

	return coord
}

func (r *StreamRegistry) RemoveCoordinator(coordId string) {
	r.coordinators.Delete(coordId)
}

func (r *StreamRegistry) runCleanup() {
	for {
		select {
		case <-r.done:
			if r.cleanupTicker != nil {
				r.cleanupTicker.Stop()
			}
			return
		case <-r.cleanupTicker.C:
			r.cleanup()
		}
	}
}

func (r *StreamRegistry) cleanup() {
	r.coordinators.Range(func(key, value interface{}) bool {
		streamID := key.(string)
		coord := value.(*StreamCoordinator)

		if atomic.LoadInt32(&coord.clientCount) == 0 {
			r.logger.Logf("Removing inactive coordinator for stream: %s", streamID)
			r.RemoveCoordinator(streamID)
		}
		return true
	})
}

func (r *StreamRegistry) Shutdown() {
	close(r.done)
	r.coordinators.Range(func(key, value interface{}) bool {
		r.coordinators.Delete(key)
		return true
	})
}
