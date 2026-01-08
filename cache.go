package main

import (
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
		return item.Location, item.Size, true
	}
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
}