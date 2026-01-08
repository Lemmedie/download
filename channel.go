package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"sync"

	"github.com/gotd/td/tg"
)

var (
	channelAccessMu sync.RWMutex
	channelAccess   = make(map[int64]uint64)
	cacheFile       = "channel_access_cache.json"
)

func init() {
	loadChannelCache()
}

func loadChannelCache() {
	b, err := os.ReadFile(cacheFile)
	if err != nil {
		return
	}
	var m map[string]uint64
	if err := json.Unmarshal(b, &m); err == nil {
		channelAccessMu.Lock()
		for k, v := range m {
			if id, err := strconv.ParseInt(k, 10, 64); err == nil {
				channelAccess[id] = v
			}
		}
		channelAccessMu.Unlock()
	}
}

func saveChannelCache() {
	channelAccessMu.RLock()
	defer channelAccessMu.RUnlock()
	b, _ := json.MarshalIndent(channelAccess, "", "  ")
	_ = os.WriteFile(cacheFile, b, 0600)
}

// ensureChannelAccess: تنها راه قانونی و بدون ارور برای گرفتن هش
func ensureChannelAccess(ctx context.Context, api *tg.Client, channelID int64) (uint64, error) {
	// ۱. چک کردن حافظه (بسیار سریع)
	channelAccessMu.RLock()
	if v, ok := channelAccess[channelID]; ok {
		channelAccessMu.RUnlock()
		return v, nil
	}
	channelAccessMu.RUnlock()

	// ۲. تلاش از طریق Username (اگر کانال یوزرنیم دارد)
	// این متد تنها متدی است که بدون داشتن هش، اطلاعات کامل (شامل هش) را برمی‌گرداند

	// ۳. تلاش از طریق لیست گفتگوها (اگر ربات عضو کانال است)
	res, err := api.MessagesGetDialogs(ctx, &tg.MessagesGetDialogsRequest{Limit: 100})
	if err == nil {
		var chats []tg.ChatClass

		switch v := res.(type) {
		case *tg.MessagesDialogs:
			chats = v.Chats
		case *tg.MessagesDialogsSlice:
			chats = v.Chats
		case *tg.MessagesDialogsNotModified:
			// در این حالت دیتای جدیدی نیامده، از کش استفاده می‌شود
		}

		for _, chat := range chats {
			if ch, ok := chat.(*tg.Channel); ok && ch.ID == channelID {
				// تبدیل صریح int64 به uint64 برای مطابقت با ورودی تابع
				acc := uint64(ch.AccessHash)
				updateLocalCache(channelID, acc)
				return acc, nil
			}
		}
	}

	return 0, errors.New("حقیقت: ربات هیچ دسترسی به این کانال ندارد. یا ادمینش کن یا یوزرنیم درست بده")
}

func updateLocalCache(id int64, acc uint64) {
	channelAccessMu.Lock()
	channelAccess[id] = acc
	channelAccessMu.Unlock()
	saveChannelCache()
}
