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

// BotClient ساختار بات‌ها
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

	// Context اصلی برای مدیریت کل برنامه
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	pool, err := NewBotPool(ctx, apiID, apiHash, tokens)
	if err != nil {
		panic(fmt.Sprintf("Failed to create bot pool: %v", err))
	}

	var botClients []BotClient
	for _, t := range tokens {
		parts := strings.Split(t, ":")
		if len(parts) > 0 {
			bID, _ := strconv.ParseInt(parts[0], 10, 64)
			botClients = append(botClients, BotClient{API: pool.GetNext(), BotID: bID})
		}
	}

	// استارت اولیه بات‌ها برای اطمینان از دسترسی
	for _, bc := range botClients {
		go func(bot BotClient) {
			acc, err := ensureChannelAccess(ctx, bot.API, bot.BotID, channelID)
			if err == nil {
				logger.Info("Bot ready", slog.Int64("bot_id", bot.BotID), slog.Uint64("channel_access_hash", acc))
			}
		}(bc)
	}

	http.HandleFunc("/stream", func(w http.ResponseWriter, r *http.Request) {
		msgID, _ := strconv.Atoi(r.URL.Query().Get("id"))
		// توزیع بار روی بات‌ها به صورت چرخشی/رندوم
		targetBot := botClients[time.Now().UnixNano()%int64(len(botClients))]

		// ایجاد یک Context مستقل برای عملیات Fetch اولیه (جلوگیری از قطع شدن با r.Context)
		fetchCtx, fetchCancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer fetchCancel()

		for attempt := 1; attempt <= 2; attempt++ {
			// ۱. دریافت دسترسی کانال (برای متد GetMessages)
			channelAccess, err := ensureChannelAccess(fetchCtx, targetBot.API, targetBot.BotID, channelID)
			if err != nil {
				logger.Error("Channel access failed", slog.Int64("bot", targetBot.BotID), slog.Any("err", err))
				http.Error(w, "Internal Server Error (Access)", 500)
				return
			}

			// ۲. بررسی کش (بازیابی لوکیشن و هش فایل)
			cachedLoc, cachedSize, found := getCachedLocation(msgID, targetBot.BotID)
			var loc *tg.InputDocumentFileLocation
			var size int64
			isFromCache := found

			if found {
				loc = &tg.InputDocumentFileLocation{
					ID:            cachedLoc.ID,
					AccessHash:    cachedLoc.AccessHash, // هش اختصاصی فایل
					FileReference: cachedLoc.FileReference,
				}
				size = cachedSize
			} else {
				// ۳. واکشی مستقیم از تلگرام
				res, err := targetBot.API.ChannelsGetMessages(fetchCtx, &tg.ChannelsGetMessagesRequest{
					Channel: &tg.InputChannel{ChannelID: channelID, AccessHash: int64(channelAccess)},
					ID:      []tg.InputMessageClass{&tg.InputMessageID{ID: msgID}},
				})
				if err != nil {
					logger.Error("Telegram fetch error", slog.Int("msg_id", msgID), slog.Any("err", err))
					http.Error(w, "Error fetching from Telegram", 502)
					return
				}

				doc, s, err := extractDocumentFromResponse(res)
				if err != nil {
					logger.Warn("Media not found", slog.Int("msg_id", msgID))
					http.Error(w, "File not found", 404)
					return
				}

				size = s
				loc = &tg.InputDocumentFileLocation{
					ID:            doc.ID,
					AccessHash:    doc.AccessHash, // استفاده از هشِ فایل
					FileReference: doc.FileReference,
				}
				logger.Info("Metadata fetched", slog.Int("msg", msgID), slog.Int64("bot", targetBot.BotID))
			}

			// ۴. شروع استریم واقعی داده‌ها (استفاده از r.Context برای قطع دانلود در صورت بستن کاربر)
			err = handleFileStream(r.Context(), w, r, targetBot.API, loc, size)

			if err != nil {
				// اگر کلاینت اتصال را قطع کرد
				if errors.Is(err, context.Canceled) {
					logger.Info("Client disconnected during stream", slog.Int("msg", msgID))
					return
				}

				// اگر رفرنس منقضی شده بود، کش را پاک و دوباره تلاش کن
				if strings.Contains(err.Error(), "FILE_REFERENCE_EXPIRED") {
					logger.Warn("File reference expired, retrying...", slog.Int("msg", msgID))
					deleteCachedLocation(msgID, targetBot.BotID)
					if attempt < 2 {
						continue
					}
				}

				logger.Error("Stream error", slog.String("err", err.Error()))
				return
			}

			// ۵. ذخیره در کش در صورت موفقیت اولین استریم
			if !isFromCache {
				setCachedLocation(msgID, targetBot.BotID, loc, size)
			}
			break
		}
	})

	port := os.Getenv("PORT")
	if port == "" {
		port = "2040"
	}
	logger.Info("Server is running", slog.String("port", port))
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		logger.Error("Server failed", slog.Any("err", err))
	}
}

// extractDocumentFromResponse استخراج سند از پاسخ تلگرام
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
