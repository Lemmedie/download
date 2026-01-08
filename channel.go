package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"reflect"
	"strconv"
	"sync"

	"github.com/gotd/td/tg"
)

var (
	channelAccessMu sync.RWMutex
	channelAccess   = make(map[int64]uint64)
	cacheFile       = func() string {
		if v := os.Getenv("CHANNEL_CACHE_FILE"); v != "" {
			return v
		}
		return "channel_access_cache.json"
	}()
)

func init() {
	loadChannelCache()
}

func loadChannelCache() {
	path := cacheFile
	b, err := os.ReadFile(path)
	if err != nil {
		// no cache is fine
		return
	}
	var m map[string]uint64
	if err := json.Unmarshal(b, &m); err != nil {
		logger.Error("channel.cache.load.error", slog.String("err", err.Error()))
		return
	}
	channelAccessMu.Lock()
	defer channelAccessMu.Unlock()
	for k, v := range m {
		if id, err := strconv.ParseInt(k, 10, 64); err == nil {
			channelAccess[id] = v
		}
	}
	logger.Info("channel.cache.loaded", slog.String("path", path))
}

func saveChannelCache() {
	channelAccessMu.RLock()
	m := make(map[string]uint64, len(channelAccess))
	for k, v := range channelAccess {
		m[strconv.FormatInt(k, 10)] = v
	}
	channelAccessMu.RUnlock()

	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		logger.Error("channel.cache.marshal.error", slog.String("err", err.Error()))
		return
	}
	if err := os.WriteFile(cacheFile, b, 0o600); err != nil {
		logger.Error("channel.cache.save.error", slog.String("err", err.Error()))
	}
}

// fetchChannelInfo attempts to fetch channel information including access hash.
func fetchChannelInfo(ctx context.Context, api *tg.Client, channelID int64) (string, uint64, error) {
	channel := &tg.InputChannel{
		ChannelID: channelID,
	}
	res, err := api.ChannelsGetFullChannel(ctx, channel)
	if err != nil {
		return "", 0, err
	}
	if res == nil {
		return "", 0, errors.New("empty response")
	}
	return "", 0, errors.New("channel info not available")
}

// ensureChannelAccess ensures we have access hash for the given channelID.
// It first checks an in-memory cache, then the env var CHANNEL_ACCESS_HASH,
// then attempts to resolve by CHANNEL_USERNAME via ContactsResolveUsername.
func ensureChannelAccess(ctx context.Context, api *tg.Client, channelID int64) (uint64, error) {
	channelAccessMu.RLock()
	if v, ok := channelAccess[channelID]; ok {
		channelAccessMu.RUnlock()
		return v, nil
	}
	channelAccessMu.RUnlock()

	// Fetch dialogs and search chats for the channel ID
	res, err := api.MessagesGetDialogs(ctx, &tg.MessagesGetDialogsRequest{Limit: 200})
	if err != nil {
		logger.Error("MessagesGetDialogs.error", slog.Int64("channel", channelID), slog.String("err", err.Error()))
		return 0, err
	}
	// reflect over result and find Chats slice
	rv := reflect.ValueOf(res)
	if rv.IsValid() {
		for rv.Kind() == reflect.Ptr || rv.Kind() == reflect.Interface {
			rv = rv.Elem()
			if !rv.IsValid() {
				break
			}
		}
		if rv.IsValid() && rv.Kind() == reflect.Struct {
			f := rv.FieldByName("Chats")
			if f.IsValid() && f.Kind() == reflect.Slice {
				for i := 0; i < f.Len(); i++ {
					ch := f.Index(i)
					if ch.Kind() == reflect.Interface || ch.Kind() == reflect.Ptr {
						ch = ch.Elem()
					}
					if !ch.IsValid() || ch.Kind() != reflect.Struct {
						continue
					}
					idF := ch.FieldByName("ID")
					if !idF.IsValid() {
						idF = ch.FieldByName("Id")
					}
					accF := ch.FieldByName("AccessHash")
					if !idF.IsValid() || !accF.IsValid() {
						continue
					}
					var id int64
					switch idF.Kind() {
					case reflect.Int, reflect.Int32, reflect.Int64:
						id = idF.Int()
					case reflect.Uint, reflect.Uint32, reflect.Uint64:
						id = int64(idF.Uint())
					}
					if id == channelID {
						var acc uint64
						switch accF.Kind() {
						case reflect.Int, reflect.Int32, reflect.Int64:
							acc = uint64(accF.Int())
						case reflect.Uint, reflect.Uint32, reflect.Uint64:
							acc = accF.Uint()
						}
						if acc != 0 {
							channelAccessMu.Lock()
							channelAccess[channelID] = acc
							channelAccessMu.Unlock()
							saveChannelCache()
							logger.Info("channel.access.found_in_dialogs", slog.Int64("channel", channelID), slog.Uint64("access", acc))
							return acc, nil
						}
					}
				}
			}
		}
	}

	logger.Info("channel.not_found_in_dialogs", slog.Int64("channel", channelID))

	// As a last resort try to fetch channel info directly which may return AccessHash
	// This does not require CHANNEL_USERNAME; some servers allow fetching channel via ID
	if c := api; c != nil {
		if name, acc, err := fetchChannelInfo(ctx, c, channelID); err == nil {
			channelAccessMu.Lock()
			channelAccess[channelID] = uint64(acc)
			channelAccessMu.Unlock()
			saveChannelCache()
			logger.Info("channel.access.fetched", slog.Int64("channel", channelID), slog.Uint64("access", uint64(acc)), slog.String("username", name))
			return uint64(acc), nil
		} else {
			logger.Error("channel.fetch.error", slog.Int64("channel", channelID), slog.String("err", err.Error()))
		}
	}

	// No access found

	return 0, errors.New("channel access hash not found; set CHANNEL_ACCESS_HASH or use dialogs")
}
