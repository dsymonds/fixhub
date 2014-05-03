package main

import (
	"fmt"
	"math/rand"
	"sync"
	"time"
)

// store is an in-memory data store, soon to be replaced by the real app engine datastore.
type store struct {
	mu      sync.Mutex
	rng     *rand.Rand
	data    map[string]FixData
	timeout map[string]time.Time
}

func (s *store) Put(d FixData) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := fmt.Sprintf("%x", s.rng.Int31())
	s.data[key] = d
	return key
}

func (s *store) Get(key string) (FixData, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if d, ok := s.data[key]; ok {
		return d, nil
	}
	return FixData{}, fmt.Errorf("no such key: %q", key)
}

func (s *store) Remove(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.timeout, key)
	delete(s.data, key)
}

func (s *store) cleanup() {
	for {
		time.Sleep(2 * time.Second)
		s.mu.Lock()
		for k, t := range s.timeout {
			if time.Since(t) > 2*time.Minute {
				delete(s.timeout, k)
				delete(s.data, k)
			}
		}
		s.mu.Unlock()
	}
}

var datastore = &store{
	rng:     rand.New(rand.NewSource(time.Now().UnixNano())),
	data:    make(map[string]FixData),
	timeout: make(map[string]time.Time),
}

func init() {
	go datastore.cleanup()
}
