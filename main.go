package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"reflect"
	"strconv"
	"strings"

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

	http.HandleFunc("/stream", func(w http.ResponseWriter, r *http.Request) {
		msgID, _ := strconv.Atoi(r.URL.Query().Get("id"))

		clientIP, ipRange := getClientIP(r)
		log.Printf("Incoming request: %s %s id=%d from=%s range=%s", r.Method, r.URL.Path, msgID, clientIP, ipRange)

		// ۱. چک کردن کش
		if loc, size, found := getCachedLocation(msgID); found {
			log.Printf("Cache hit for msg=%d size=%d", msgID, size)
			handleFileStream(r.Context(), w, pool.GetNext(), loc, size)
			return
		}
		log.Printf("Cache miss for msg=%d", msgID)

		// ۲. پیدا کردن فایل (اگر در کش نبود)
		api := pool.GetNext()
		if api == nil {
			log.Printf("no available bot clients to handle request for msg=%d", msgID)
			http.Error(w, "service unavailable", http.StatusServiceUnavailable)
			return
		}
		log.Printf("Fetching msg=%d from channel=%d", msgID, channelID)
		res, err := api.ChannelsGetMessages(r.Context(), &tg.ChannelsGetMessagesRequest{
			Channel: &tg.InputChannel{ChannelID: channelID},
			ID:      []tg.InputMessageClass{&tg.InputMessageID{ID: msgID}},
		})
		if err != nil {
			log.Printf("ChannelsGetMessages error for msg=%d: %v", msgID, err)
			http.Error(w, "error fetching message", http.StatusInternalServerError)
			return
		}

		// استخراج لوکیشن از پاسخ
		loc, size, ok := extractDocumentFromResponse(res)
		if !ok || loc == nil {
			log.Printf("No document found in msg=%d", msgID)
			http.Error(w, "file not found in message", http.StatusNotFound)
			return
		}

		log.Printf("Found document for msg=%d size=%d — caching and streaming", msgID, size)
		setCachedLocation(msgID, loc, size)
		log.Printf("Start streaming msg=%d size=%d to %s", msgID, size, clientIP)
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
	log.Printf("Server ready on %s", bindAddr)

	err := http.ListenAndServe(bindAddr, nil)
	if err != nil {
		log.Fatal(err)
	}
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
