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

var logger = slog.Default()

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

	// فرض بر این است که NewBotPool و متدهای کش در فایل‌های دیگر شما تعریف شده‌اند
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
		// انتخاب رندوم بات برای توزیع بار
		targetBot := botClients[time.Now().UnixNano()%int64(len(botClients))]

		for attempt := 1; attempt <= 2; attempt++ {
			// ۱. دریافت دسترسی کانال برای متد GetMessages
			channelAccess, err := ensureChannelAccess(r.Context(), targetBot.API, targetBot.BotID, channelID)
			if err != nil {
				http.Error(w, "Channel access error", 500)
				return
			}

			// ۲. بررسی کش (شامل AccessHash خودِ فایل)
			cachedLoc, cachedSize, found := getCachedLocation(msgID, targetBot.BotID)
			var loc *tg.InputDocumentFileLocation
			var size int64
			isFromCache := found

			if found {
				loc = &tg.InputDocumentFileLocation{
					ID:            cachedLoc.ID,
					AccessHash:    cachedLoc.AccessHash, // استفاده از هش فایل
					FileReference: cachedLoc.FileReference,
				}
				size = cachedSize
			} else {
				// ۳. واکشی اطلاعات از تلگرام در صورت عدم وجود در کش
				res, err := targetBot.API.ChannelsGetMessages(r.Context(), &tg.ChannelsGetMessagesRequest{
					Channel: &tg.InputChannel{ChannelID: channelID, AccessHash: int64(channelAccess)},
					ID:      []tg.InputMessageClass{&tg.InputMessageID{ID: msgID}},
				})
				if err != nil {
					http.Error(w, "Telegram API error", 500)
					return
				}

				doc, s, err := extractDocumentFromResponse(res)
				if err != nil {
					http.Error(w, "Document not found", 404)
					return
				}
				size = s
				loc = &tg.InputDocumentFileLocation{
					ID:            doc.ID,
					AccessHash:    doc.AccessHash, // هش اختصاصی سند
					FileReference: doc.FileReference,
				}
			}

			// ۴. شروع استریم (ارسال loc شامل هش صحیح)
			err = handleFileStream(r.Context(), w, r, targetBot.API, loc, size)

			if err != nil {
				if strings.Contains(err.Error(), "FILE_REFERENCE_EXPIRED") {
					logger.Warn("Ref expired, retrying...", slog.Int("msg", msgID))
					deleteCachedLocation(msgID, targetBot.BotID)
					if attempt < 2 {
						continue
					}
				}
				logger.Error("Stream failed", slog.String("error", err.Error()))
				return
			}

			// ۵. ذخیره در کش فقط در صورت موفقیت استریم اولیه
			if !isFromCache {
				setCachedLocation(msgID, targetBot.BotID, loc, size)
			}
			break
		}
	})

	logger.Info("Server started on :2040")
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
		return nil, 0, fmt.Errorf("unknown response type")
	}

	if len(messages) == 0 {
		return nil, 0, errors.New("empty message list")
	}

	msg, ok := messages[0].(*tg.Message)
	if !ok || msg.Media == nil {
		return nil, 0, errors.New("no media in message")
	}

	mediaDoc, ok := msg.Media.(*tg.MessageMediaDocument)
	if !ok {
		return nil, 0, errors.New("media is not a document")
	}

	doc, ok := mediaDoc.Document.(*tg.Document)
	if !ok {
		return nil, 0, errors.New("invalid document structure")
	}

	return doc, doc.Size, nil
}
