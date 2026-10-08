// Package cache stores Jev's answers so repeat searches cost nothing.
package cache

import (
	"crypto/sha256"
	"encoding/binary"
	"strings"
	"sync"
)

// Key is sha256(model + question + state as sent).
type Key [32]byte

// Cache maps keys to Noul probabilities. SQLite is the real store; any
// other store only has to implement these two calls.
type Cache interface {
	Get(keys []Key) (map[Key]float64, error)
	Put(key Key, noul float64) error
}

// KeyOf hashes exactly what is sent to Jev. The question is lowercased and
// its white space collapsed first, so trivially different spellings share
// answers. Parts are length-prefixed so they can't run into each other.
func KeyOf(model, question string, state []byte) Key {
	h := sha256.New()
	for _, part := range [][]byte{[]byte(model), []byte(normalize(question)), state} {
		h.Write(binary.LittleEndian.AppendUint64(nil, uint64(len(part))))
		h.Write(part)
	}
	var k Key
	h.Sum(k[:0])
	return k
}

func normalize(q string) string {
	return strings.Join(strings.Fields(strings.ToLower(q)), " ")
}

// Memory is an in-process Cache, for tests and --no-cache style runs.
type Memory struct {
	mu sync.Mutex
	m  map[Key]float64
}

func NewMemory() *Memory { return &Memory{m: map[Key]float64{}} }

func (c *Memory) Get(keys []Key) (map[Key]float64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := map[Key]float64{}
	for _, k := range keys {
		if v, ok := c.m[k]; ok {
			out[k] = v
		}
	}
	return out, nil
}

func (c *Memory) Put(key Key, noul float64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.m[key]; !ok {
		c.m[key] = noul
	}
	return nil
}
