package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"reflect"
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
		loc, size, ok := extractDocumentFromResponse(res)
		if !ok || loc == nil {
			logger.Info("no.document", slog.Int("msg", msgID))
			http.Error(w, "file not found in message", http.StatusNotFound)
			return
		}

		logger.Info("document.found", slog.Int("msg", msgID), slog.Int64("size", size))
		setCachedLocation(msgID, loc, size)
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
func extractDocumentFromResponse(res interface{}) (*tg.InputDocumentFileLocation, int64, bool) {
	if res == nil {
		return nil, 0, false
	}

	rv := reflect.ValueOf(res)
	if !rv.IsValid() {
		return nil, 0, false
	}
	rv = reflect.Indirect(rv)

	var iter reflect.Value
	// If the value itself is a slice of messages, iterate directly
	if rv.Kind() == reflect.Slice {
		iter = rv
	} else {
		// Otherwise try to get a field named "Messages"
		f := rv.FieldByName("Messages")
		if !f.IsValid() || f.Kind() != reflect.Slice {
			return nil, 0, false
		}
		iter = f
	}

	for i := 0; i < iter.Len(); i++ {
		item := iter.Index(i)
		item = reflect.Indirect(item)
		if !item.IsValid() {
			continue
		}

		media := item.FieldByName("Media")
		if !media.IsValid() {
			continue
		}
		media = reflect.Indirect(media)
		if !media.IsValid() {
			continue
		}

		doc := media.FieldByName("Document")
		if !doc.IsValid() {
			continue
		}
		doc = reflect.Indirect(doc)
		if !doc.IsValid() {
			continue
		}

		// helpers to find fields in doc
		findField := func(names ...string) (reflect.Value, bool) {
			for _, n := range names {
				f := doc.FieldByName(n)
				if f.IsValid() {
					return f, true
				}
			}
			return reflect.Value{}, false
		}

		// id
		var id uint64
		if fv, ok := findField("ID", "Id", "DocumentID", "DocumentId"); ok {
			switch fv.Kind() {
			case reflect.Int, reflect.Int32, reflect.Int64:
				id = uint64(fv.Int())
			case reflect.Uint, reflect.Uint32, reflect.Uint64:
				id = fv.Uint()
			}
		} else {
			continue
		}

		// access hash
		var accessHash uint64
		if fv, ok := findField("AccessHash", "Accesshash", "Access_Hash"); ok {
			switch fv.Kind() {
			case reflect.Int, reflect.Int32, reflect.Int64:
				accessHash = uint64(fv.Int())
			case reflect.Uint, reflect.Uint32, reflect.Uint64:
				accessHash = fv.Uint()
			}
		} else {
			continue
		}

		// file reference (optional)
		var fileRef []byte
		if fv, ok := findField("FileReference", "FileRef", "File_reference"); ok {
			if fv.Kind() == reflect.Slice && fv.Type().Elem().Kind() == reflect.Uint8 {
				fileRef = fv.Bytes()
			}
		}

		// size (optional)
		var size int64
		if fv, ok := findField("Size", "FileSize"); ok {
			switch fv.Kind() {
			case reflect.Int, reflect.Int32, reflect.Int64:
				size = fv.Int()
			case reflect.Uint, reflect.Uint32, reflect.Uint64:
				size = int64(fv.Uint())
			}
		}

		// build InputDocumentFileLocation using reflection to avoid direct field type assumptions
		locType := reflect.TypeOf(tg.InputDocumentFileLocation{})
		locVal := reflect.New(locType).Elem()
		if f := locVal.FieldByName("ID"); f.IsValid() && f.CanSet() {
			switch f.Kind() {
			case reflect.Int, reflect.Int32, reflect.Int64:
				f.SetInt(int64(id))
			case reflect.Uint, reflect.Uint32, reflect.Uint64:
				f.SetUint(id)
			}
		}
		if f := locVal.FieldByName("AccessHash"); f.IsValid() && f.CanSet() {
			switch f.Kind() {
			case reflect.Int, reflect.Int32, reflect.Int64:
				f.SetInt(int64(accessHash))
			case reflect.Uint, reflect.Uint32, reflect.Uint64:
				f.SetUint(accessHash)
			}
		}
		if f := locVal.FieldByName("FileReference"); f.IsValid() && f.CanSet() {
			if f.Kind() == reflect.Slice && f.Type().Elem().Kind() == reflect.Uint8 {
				f.SetBytes(fileRef)
			}
		}

		loc := locVal.Addr().Interface().(*tg.InputDocumentFileLocation)
		return loc, size, true
	}

	return nil, 0, false
}
