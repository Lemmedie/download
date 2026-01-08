package main

import (
	"fmt"
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
	// کلید به صورت string (msgID:botID)
	fileCache = make(map[string]CachedFile)
	cacheMu   sync.RWMutex
)

func getCacheKey(msgID int, botID int64) string {
	return fmt.Sprintf("%d:%d", msgID, botID)
}

func getCachedLocation(msgID int, botID int64) (*tg.InputDocumentFileLocation, int64, bool) {
	cacheMu.RLock()
	defer cacheMu.RUnlock()
	key := getCacheKey(msgID, botID)
	item, found := fileCache[key]
	if found && time.Now().Before(item.Expires) {
		return item.Location, item.Size, true
	}
	return nil, 0, false
}

func setCachedLocation(msgID int, botID int64, loc *tg.InputDocumentFileLocation, size int64) {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	key := getCacheKey(msgID, botID)
	fileCache[key] = CachedFile{
		Location: loc,
		Size:     size,
		Expires:  time.Now().Add(3 * time.Hour),
	}
}

func deleteCachedLocation(msgID int, botID int64) {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	delete(fileCache, getCacheKey(msgID, botID))
	logger.Warn("cache.deleted", slog.Int("msg", msgID), slog.Int64("bot", botID))
}