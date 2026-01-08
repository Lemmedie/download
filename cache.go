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
	// کلید مپ را به string تغییر می‌دهیم تا ترکیب msgID:botID باشد
	fileCache = make(map[string]CachedFile)
	cacheMu   sync.RWMutex
)

// تولید کلید یکتا برای هر فایل و هر ربات
func getCacheKey(msgID int, botID int64) string {
	return fmt.Sprintf("%d:%d", msgID, botID)
}

func getCachedLocation(msgID int, botID int64) (*tg.InputDocumentFileLocation, int64, bool) {
	cacheMu.RLock()
	defer cacheMu.RUnlock()

	key := getCacheKey(msgID, botID)
	item, found := fileCache[key]

	if found && time.Now().Before(item.Expires) {
		logger.Info("cache.hit", slog.Int("msg", msgID), slog.Int64("bot", botID))
		return item.Location, item.Size, true
	}
	logger.Info("cache.miss", slog.Int("msg", msgID), slog.Int64("bot", botID))
	return nil, 0, false
}

func setCachedLocation(msgID int, botID int64, loc *tg.InputDocumentFileLocation, size int64) {
	cacheMu.Lock()
	defer cacheMu.Unlock()

	key := getCacheKey(msgID, botID)
	fileCache[key] = CachedFile{
		Location: loc,
		Size:     size,
		Expires:  time.Now().Add(15 * time.Minute),
	}
	logger.Info("cache.set", slog.String("key", key), slog.Int64("size", size), slog.String("expires", fileCache[key].Expires.Format(time.RFC3339)))
}

func deleteCachedLocation(msgID int, botID int64) {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	key := getCacheKey(msgID, botID)
	delete(fileCache, key)
	logger.Warn("cache.deleted", slog.String("key", key))
}
