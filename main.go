package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"

	"github.com/gotd/td/tg"
	"github.com/joho/godotenv"
)

func main() {
	_ = godotenv.Load()
	apiID, _ := strconv.Atoi(os.Getenv("API_ID"))
	apiHash := os.Getenv("API_HASH")
	tokens := strings.Split(os.Getenv("BOT_TOKENS"), ",")
	channelID, _ := strconv.ParseInt(os.Getenv("CHANNEL_ID"), 10, 64)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	pool, _ := NewBotPool(ctx, apiID, apiHash, tokens)
	// Try to resolve channel access hash on startup using any available client
	if c := pool.GetNext(); c != nil {
		go func() {
			if acc, err := ensureChannelAccess(ctx, c, channelID); err != nil {
				logger.Error("channel.access.bootstrap.failed", slog.Int64("channel", channelID), slog.String("err", err.Error()))
			} else {
				logger.Info("channel.access.bootstrap", slog.Int64("channel", channelID), slog.Uint64("access", acc))
			}
		}()
	}

	http.HandleFunc("/stream", func(w http.ResponseWriter, r *http.Request) {
		msgID, _ := strconv.Atoi(r.URL.Query().Get("id"))

		clientIP, ipRange := getClientIP(r)
		// ensure we have a request id
		rid := r.Header.Get("X-Request-ID")
		if rid == "" {
			rid = generateRequestID()
			r.Header.Set("X-Request-ID", rid)
		}
		start := time.Now()
		defer func() {
			logger.Info("request.finished", slog.String("request_id", rid), slog.Int("msg_id", msgID), slog.Duration("duration", time.Since(start)))
		}()
		logger.Info("request.incoming", slog.String("method", r.Method), slog.String("path", r.URL.Path), slog.Int("msg_id", msgID), slog.String("request_id", rid), slog.String("client_ip", clientIP), slog.String("ip_range", ipRange))

		// ۱. چک کردن کش
		if loc, size, found := getCachedLocation(msgID); found {
			logger.Info("cache.hit", slog.Int("msg", msgID), slog.Int64("size", size))
			handleFileStream(r.Context(), w, pool.GetNext(), loc, size)
			return
		}
		logger.Info("cache.miss", slog.Int("msg", msgID))

		// ۲. پیدا کردن فایل (اگر در کش نبود)
		api := pool.GetNext()
		if api == nil {
			logger.Error("no bot clients available", slog.Int("msg", msgID))
			http.Error(w, "service unavailable", http.StatusServiceUnavailable)
			return
		}
		logger.Info("fetching.message", slog.Int("msg", msgID), slog.Int64("channel", channelID))
		access, err := ensureChannelAccess(r.Context(), api, channelID)
		if err != nil {
			logger.Error("channel.access.missing", slog.Int64("channel", channelID), slog.String("err", err.Error()))
			http.Error(w, "channel access not available", http.StatusInternalServerError)
			return
		}
		res, err := api.ChannelsGetMessages(r.Context(), &tg.ChannelsGetMessagesRequest{
			Channel: &tg.InputChannel{ChannelID: channelID, AccessHash: int64(access)},
			ID:      []tg.InputMessageClass{&tg.InputMessageID{ID: msgID}},
		})
		if err != nil {
			logger.Error("ChannelsGetMessages.error", slog.Int("msg", msgID), slog.String("err", err.Error()))
			http.Error(w, "error fetching message", http.StatusInternalServerError)
			return
		}

		// استخراج لوکیشن از پاسخ
		doc, size, err := extractDocumentFromResponse(res)
		if err != nil || doc == nil {
			logger.Info("no.document", slog.Int("msg", msgID), slog.String("err", err.Error()))
			logger.Info("no.document", slog.Int("msg", msgID))
			http.Error(w, "file not found in message", http.StatusNotFound)
			return
		}

		logger.Info("document.found", slog.Int("msg", msgID), slog.Int64("size", size))
		loc := &tg.InputDocumentFileLocation{
			ID:            doc.ID,
			AccessHash:    doc.AccessHash,
			FileReference: doc.FileReference,
		}
		setCachedLocation(msgID, loc, size)
		logger.Info("start.streaming", slog.Int("msg", msgID), slog.Int64("size", size), slog.String("to", clientIP))
		handleFileStream(r.Context(), w, api, loc, size)
		logger.Info("start.streaming", slog.Int("msg", msgID), slog.Int64("size", size), slog.String("to", clientIP))
		handleFileStream(r.Context(), w, api, loc, size)
	})

	// client IP helper is defined at package level

	// Bind address can be configured with BIND_ADDR (e.g. 0.0.0.0:2040) or PORT
	bindAddr := os.Getenv("BIND_ADDR")
	if bindAddr == "" {
		port := os.Getenv("PORT")
		if port == "" {
			port = "2040"
		}
		bindAddr = ":" + port
	}
	logger.Info("server.ready", slog.String("addr", bindAddr))

	server := &http.Server{Addr: bindAddr, Handler: nil}
	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("server.listen.error", slog.String("err", err.Error()))
		}
	}()

	// Wait for shutdown signal (Ctrl+C)
	<-ctx.Done()
	logger.Info("shutdown.initiated")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("server.shutdown.error", slog.String("err", err.Error()))
	}
	// persist channel cache on shutdown
	saveChannelCache()
	logger.Info("shutdown.complete")
	return
}

// getClientIP returns the best-effort remote IP and a simple range (/24 or /64)
func getClientIP(r *http.Request) (string, string) {
	ip := strings.TrimSpace(r.Header.Get("X-Real-IP"))
	if ip == "" {
		xff := strings.TrimSpace(r.Header.Get("X-Forwarded-For"))
		if xff != "" {
			parts := strings.Split(xff, ",")
			ip = strings.TrimSpace(parts[0])
		}
	}
	if ip == "" {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			ip = r.RemoteAddr
		} else {
			ip = host
		}
	}

	parsed := net.ParseIP(ip)
	if parsed == nil {
		return ip, ""
	}
	if parsed.To4() != nil {
		parts := strings.Split(ip, ".")
		if len(parts) >= 3 {
			return ip, fmt.Sprintf("%s.%s.%s.0/24", parts[0], parts[1], parts[2])
		}
		return ip, ""
	}
	hext := strings.Split(ip, ":")
	if len(hext) >= 4 {
		return ip, strings.Join(hext[:4], ":") + "::/64"
	}
	return ip, ""
}

// extractDocumentFromResponse inspects the response from ChannelsGetMessages and returns
// an InputDocumentFileLocation and its size if a document is found in any message.
// This implementation uses reflection to avoid depending on concrete generated tg types
// which may vary between versions.
func extractDocumentFromResponse(res tg.MessagesMessagesClass) (*tg.Document, int64, error) {
	// ۱. تبدیل اینترفیس به ساختار واقعی (Type Assertion)
	var messages []tg.MessageClass

	switch v := res.(type) {
	case *tg.MessagesMessages:
		messages = v.Messages
	case *tg.MessagesMessagesSlice:
		messages = v.Messages
	case *tg.MessagesChannelMessages:
		messages = v.Messages
	default:
		return nil, 0, fmt.Errorf("unexpected response type: %T", res)
	}

	if len(messages) == 0 {
		return nil, 0, errors.New("no messages in response")
	}

	// ۲. استخراج مدیا از اولین پیام
	msg, ok := messages[0].(*tg.Message)
	if !ok {
		return nil, 0, errors.New("message is not a regular message (maybe service message?)")
	}

	if msg.Media == nil {
		return nil, 0, errors.New("message has no media")
	}

	// ۳. چک کردن اینکه آیا مدیا واقعاً یک فایل (Document) است
	mediaDoc, ok := msg.Media.(*tg.MessageMediaDocument)
	if !ok {
		return nil, 0, errors.New("media is not a document (maybe photo or poll?)")
	}

	document, ok := mediaDoc.Document.(*tg.Document)
	if !ok {
		return nil, 0, errors.New("document metadata not found")
	}

	return document, document.Size, nil
}
