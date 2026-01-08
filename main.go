package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"

	"github.com/gotd/td/tg"
	"github.com/joho/godotenv"
)

type BotClient struct {
	API   *tg.Client
	BotID int64
}

func main() {
	_ = godotenv.Load()
	apiID, _ := strconv.Atoi(os.Getenv("API_ID"))
	apiHash := os.Getenv("API_HASH")
	tokens := strings.Split(os.Getenv("BOT_TOKENS"), ",")
	channelID, _ := strconv.ParseInt(os.Getenv("CHANNEL_ID"), 10, 64)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	pool, err := NewBotPool(ctx, apiID, apiHash, tokens)
	if err != nil {
		panic(err)
	}

	var botClients []BotClient
	for _, t := range tokens {
		parts := strings.Split(t, ":")
		if len(parts) > 0 {
			bID, _ := strconv.ParseInt(parts[0], 10, 64)
			botClients = append(botClients, BotClient{API: pool.GetNext(), BotID: bID})
		}
	}

	for _, bc := range botClients {
		go func(bot BotClient) {
			acc, err := ensureChannelAccess(ctx, bot.API, bot.BotID, channelID)
			if err == nil {
				logger.Info("bot.ready", slog.Int64("id", bot.BotID), slog.Uint64("hash", acc))
			}
		}(bc)
	}

	http.HandleFunc("/stream", func(w http.ResponseWriter, r *http.Request) {
		msgID, _ := strconv.Atoi(r.URL.Query().Get("id"))
		targetBot := botClients[time.Now().UnixNano()%int64(len(botClients))]

		// گلوله حقیقت: حداکثر ۲ تلاش برای مقابله با انقضای فایل
		for attempt := 1; attempt <= 2; attempt++ {
			access, err := ensureChannelAccess(r.Context(), targetBot.API, targetBot.BotID, channelID)
			if err != nil {
				http.Error(w, "Access error", 500)
				return
			}

			cachedLoc, cachedSize, found := getCachedLocation(msgID, targetBot.BotID)
			var loc *tg.InputDocumentFileLocation
			var size int64

			if found {
				loc = &tg.InputDocumentFileLocation{
					ID:            cachedLoc.ID,
					AccessHash:    int64(access),
					FileReference: cachedLoc.FileReference,
				}
				size = cachedSize
			} else {
				res, err := targetBot.API.ChannelsGetMessages(r.Context(), &tg.ChannelsGetMessagesRequest{
					Channel: &tg.InputChannel{ChannelID: channelID, AccessHash: int64(access)},
					ID:      []tg.InputMessageClass{&tg.InputMessageID{ID: msgID}},
				})
				if err != nil {
					http.Error(w, "Telegram fetch error", 500)
					return
				}
				doc, s, err := extractDocumentFromResponse(res)
				if err != nil {
					http.Error(w, "File not found", 404)
					return
				}
				size = s
				loc = &tg.InputDocumentFileLocation{ID: doc.ID, AccessHash: int64(access), FileReference: doc.FileReference}
				setCachedLocation(msgID, targetBot.BotID, loc, size)
			}

			// تلاش برای استریم
			err = handleFileStream(r.Context(), w, r, targetBot.API, targetBot.BotID, access, loc, size)

			if err != nil {
				// چک کردن خطای انقضای رفرنس
				if strings.Contains(err.Error(), "FILE_REFERENCE_EXPIRED") {
					logger.Warn("expired_ref_detected. clearing_cache_and_retrying", slog.Int("msg", msgID), slog.Int("attempt", attempt))
					deleteCachedLocation(msgID, targetBot.BotID)
					if attempt < 2 {
						continue
					} // تلاش دوباره
				}
				logger.Error("stream.error", slog.String("err", err.Error()))
				return
			}
			break // خروج از حلقه در صورت موفقیت
		}
	})

	logger.Info("server.running", slog.String("port", "2040"))
	_ = http.ListenAndServe(":2040", nil)
}

func extractDocumentFromResponse(res tg.MessagesMessagesClass) (*tg.Document, int64, error) {
	var messages []tg.MessageClass
	switch v := res.(type) {
	case *tg.MessagesMessages:
		messages = v.Messages
	case *tg.MessagesMessagesSlice:
		messages = v.Messages
	case *tg.MessagesChannelMessages:
		messages = v.Messages
	default:
		return nil, 0, fmt.Errorf("unexpected type: %T", res)
	}
	if len(messages) == 0 {
		return nil, 0, errors.New("no messages")
	}
	msg, ok := messages[0].(*tg.Message)
	if !ok || msg.Media == nil {
		return nil, 0, errors.New("no media")
	}
	mediaDoc, ok := msg.Media.(*tg.MessageMediaDocument)
	if !ok {
		return nil, 0, errors.New("not a document")
	}
	doc, ok := mediaDoc.Document.(*tg.Document)
	if !ok {
		return nil, 0, errors.New("cast failed")
	}
	return doc, doc.Size, nil
}
