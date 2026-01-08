package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"sync"

	"github.com/gotd/td/tg"
)

var (
	channelAccessMu sync.RWMutex
	channelAccess   = make(map[int64]map[int64]uint64)
	cacheFile       = "channel_access_cache.json"
)

func init() {
	loadChannelCache()
}

func loadChannelCache() {
	b, err := os.ReadFile(cacheFile)
	if err != nil { return }
	var rawMap map[string]map[string]uint64
	if err := json.Unmarshal(b, &rawMap); err == nil {
		channelAccessMu.Lock()
		for bIDStr, channels := range rawMap {
			bID, _ := strconv.ParseInt(bIDStr, 10, 64)
			if channelAccess[bID] == nil { channelAccess[bID] = make(map[int64]uint64) }
			for cIDStr, hash := range channels {
				cID, _ := strconv.ParseInt(cIDStr, 10, 64)
				channelAccess[bID][cID] = hash
			}
		}
		channelAccessMu.Unlock()
	}
}

func saveChannelCache() {
	channelAccessMu.RLock()
	defer channelAccessMu.RUnlock()
	serializable := make(map[string]map[string]uint64)
	for bID, channels := range channelAccess {
		serializable[strconv.FormatInt(bID, 10)] = make(map[string]uint64)
		for cID, hash := range channels {
			serializable[strconv.FormatInt(bID, 10)][strconv.FormatInt(cID, 10)] = hash
		}
	}
	b, _ := json.MarshalIndent(serializable, "", "  ")
	_ = os.WriteFile(cacheFile, b, 0600)
}

func ensureChannelAccess(ctx context.Context, api *tg.Client, botID int64, channelID int64) (uint64, error) {
	channelAccessMu.RLock()
	if botMap, ok := channelAccess[botID]; ok {
		if hash, found := botMap[channelID]; found {
			channelAccessMu.RUnlock()
			return hash, nil
		}
	}
	channelAccessMu.RUnlock()

	res, err := api.ChannelsGetChannels(ctx, []tg.InputChannelClass{&tg.InputChannel{ChannelID: channelID, AccessHash: 0}})
	if err == nil {
		if chats, ok := res.(*tg.MessagesChats); ok {
			for _, chat := range chats.Chats {
				if ch, ok := chat.(*tg.Channel); ok && ch.ID == channelID {
					acc := uint64(ch.AccessHash)
					updateLocalCache(botID, channelID, acc)
					return acc, nil
				}
			}
		}
	}
	return 0, fmt.Errorf("access not found for bot %d", botID)
}

func updateLocalCache(botID, channelID int64, acc uint64) {
	channelAccessMu.Lock()
	if channelAccess[botID] == nil { channelAccess[botID] = make(map[int64]uint64) }
	channelAccess[botID][channelID] = acc
	channelAccessMu.Unlock()
	saveChannelCache()
}