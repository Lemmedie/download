package main

import (
	"log/slog"
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
		logger.Info("cache.hit", slog.Int("msg", msgID), slog.Int64("size", item.Size), slog.String("expires", item.Expires.Format(time.RFC3339)))
		return item.Location, item.Size, true
	}
	logger.Info("cache.miss", slog.Int("msg", msgID))
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
	logger.Info("cache.set", slog.Int("msg", msgID), slog.Int64("size", size), slog.String("expires", fileCache[msgID].Expires.Format(time.RFC3339)))
}
