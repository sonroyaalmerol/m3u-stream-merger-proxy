package sourceproc

import (
	"sync"

	"golang.org/x/crypto/sha3"
)

const shardCount = 256

type StreamShard struct {
	sync.RWMutex
	streams map[string]*StreamInfo
}

type ShardedStreamMap struct {
	shards []*StreamShard
}

func newShardedStreamMap() *ShardedStreamMap {
	shards := make([]*StreamShard, shardCount)
	for i := range shards {
		shards[i] = &StreamShard{
			streams: make(map[string]*StreamInfo),
		}
	}
	return &ShardedStreamMap{shards: shards}
}

func (m *ShardedStreamMap) getShard(key string) *StreamShard {
	h := sha3.Sum224([]byte(key))
	idx := int(h[0]) % shardCount
	return m.shards[idx]
}

func (m *ShardedStreamMap) toMap() map[string]*StreamInfo {
	result := make(map[string]*StreamInfo)
	for _, shard := range m.shards {
		shard.RLock()
		for k, v := range shard.streams {
			result[k] = v.Clone() // Use Clone method for thread safety
		}
		shard.RUnlock()
	}
	return result
}

func (m *ShardedStreamMap) clear() {
	for _, shard := range m.shards {
		shard.Lock()
		shard.streams = make(map[string]*StreamInfo)
		shard.Unlock()
	}
}
