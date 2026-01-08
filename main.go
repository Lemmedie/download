package main

import (
	"context"
	"errors"
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

	http.HandleFunc("/stream", func(w http.ResponseWriter, r *http.Request) {
		msgID, _ := strconv.Atoi(r.URL.Query().Get("id"))

		// حداکثر ۳ بار تلاش با ربات‌های مختلف
		for attempt := 0; attempt < 3; attempt++ {
			// انتخاب ربات (با هر بار تکرار یک ربات جدید انتخاب می‌شود)
			targetBot := botClients[(int(time.Now().UnixNano())+attempt)%len(botClients)]

			access, err := ensureChannelAccess(r.Context(), targetBot.API, targetBot.BotID, channelID)
			if err != nil {
				continue
			}

			cachedLoc, cachedSize, found := getCachedLocation(msgID, targetBot.BotID)
			var loc *tg.InputDocumentFileLocation
			var size int64

			if found {
				loc = cachedLoc
				size = cachedSize
			} else {
				res, err := targetBot.API.ChannelsGetMessages(r.Context(), &tg.ChannelsGetMessagesRequest{
					Channel: &tg.InputChannel{ChannelID: channelID, AccessHash: int64(access)},
					ID:      []tg.InputMessageClass{&tg.InputMessageID{ID: msgID}},
				})
				if err != nil {
					continue
				}
				doc, s, err := extractDocumentFromResponse(res)
				if err != nil {
					continue
				}
				size = s
				loc = &tg.InputDocumentFileLocation{ID: doc.ID, AccessHash: doc.AccessHash, FileReference: doc.FileReference}
				setCachedLocation(msgID, targetBot.BotID, loc, size)
			}

			// شروع استریم
			err = handleFileStream(r.Context(), w, r, targetBot.API, loc, size)

			if err != nil {
				// اگر Flood بود یا رفرنس منقضی شده بود، کش را پاک کن و برو سراغ ربات بعدی
				if strings.Contains(err.Error(), "FLOOD_WAIT") || strings.Contains(err.Error(), "FILE_REFERENCE_EXPIRED") {
					logger.Warn("Worker failed, switching...", slog.Int64("bot", targetBot.BotID), slog.String("reason", err.Error()))
					deleteCachedLocation(msgID, targetBot.BotID)
					continue // انتخاب ربات بعدی در تکرار بعدی حلقه for
				}
				if errors.Is(err, context.Canceled) {
					return
				}
				return
			}
			return // موفقیت‌آمیز بود
		}
		http.Error(w, "All workers are busy or flooded", 429)
	})

	_ = http.ListenAndServe(":2040", nil)
}

// [متد extractDocumentFromResponse بدون تغییر باقی می‌ماند]
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
		return nil, 0, errors.New("unexpected message type")
	}

	if len(messages) == 0 {
		return nil, 0, errors.New("no messages found")
	}

	msg, ok := messages[0].(*tg.Message)
	if !ok || msg.Media == nil {
		return nil, 0, errors.New("message has no media")
	}

	mediaDoc, ok := msg.Media.(*tg.MessageMediaDocument)
	if !ok || mediaDoc.Document == nil {
		return nil, 0, errors.New("media is not a document")
	}

	doc, ok := mediaDoc.Document.(*tg.Document)
	if !ok {
		return nil, 0, errors.New("failed to cast to tg.Document")
	}

	return doc, doc.Size, nil
}
