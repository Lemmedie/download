package main

import (
	"log"
	"sync"
	"time"

	"github.com/gotd/td/tg"
)

type CachedFile struct {
	Location *tg.InputDocumentFileLocation
	Size     int64
	Expires  time.Time
}

var (
	fileCache = make(map[int]CachedFile)
	cacheMu   sync.RWMutex
)

func getCachedLocation(msgID int) (*tg.InputDocumentFileLocation, int64, bool) {
	cacheMu.RLock()
	defer cacheMu.RUnlock()
	item, found := fileCache[msgID]
	if found && time.Now().Before(item.Expires) {
		log.Printf("cache hit: msg=%d size=%d expires=%s", msgID, item.Size, item.Expires)
		return item.Location, item.Size, true
	}
	log.Printf("cache miss: msg=%d", msgID)
	return nil, 0, false
}

func setCachedLocation(msgID int, loc *tg.InputDocumentFileLocation, size int64) {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	fileCache[msgID] = CachedFile{
		Location: loc,
		Size:     size,
		Expires:  time.Now().Add(3 * time.Hour),
	}
	log.Printf("cache set: msg=%d size=%d expires=%s", msgID, size, fileCache[msgID].Expires)
}
