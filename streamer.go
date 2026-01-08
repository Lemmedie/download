package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

// handleFileStream وظیفه استریم کردن فایل از تلگرام به سمت کلاینت را دارد
func handleFileStream(ctx context.Context, w http.ResponseWriter, r *http.Request, api *tg.Client, location *tg.InputDocumentFileLocation, size int64) error {

	// تنظیم هدرهای پایه برای پشتیبانی از قابلیت Range (Resume)
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Content-Type", "application/octet-stream")

	var startOffset, endOffset int64
	endOffset = size - 1 // پیش‌فرض تا انتهای فایل

	// مدیریت درخواست‌های بازه‌ای (Partial Content)
	rangeHeader := r.Header.Get("Range")
	if rangeHeader != "" && strings.HasPrefix(rangeHeader, "bytes=") {
		parts := strings.Split(strings.TrimPrefix(rangeHeader, "bytes="), "-")
		startOffset, _ = strconv.ParseInt(parts[0], 10, 64)
		if len(parts) > 1 && parts[1] != "" {
			endOffset, _ = strconv.ParseInt(parts[1], 10, 64)
		}
		if endOffset >= size {
			endOffset = size - 1
		}

		// هدرهای ضروری برای دانلودهای چند رشته‌ای (مثل Aria2)
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", startOffset, endOffset, size))
		w.Header().Set("Content-Length", strconv.FormatInt(endOffset-startOffset+1, 10))
		w.WriteHeader(http.StatusPartialContent)
	} else {
		// درخواست عادی برای کل فایل
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	}

	// اگر درخواست فقط برای دریافت هدرها بود (مثل curl -I)
	if r.Method == http.MethodHead {
		return nil
	}

	
	const chunkSize = 1024 * 1024 // اندازه هر قطعه دریافتی از تلگرام (1 مگابایت)
	offset := startOffset

	for offset <= endOffset {
		// دریافت قطعه از تلگرام
		res, err := api.UploadGetFile(ctx, &tg.UploadGetFileRequest{
			Location: location,
			Offset:   offset,
			Limit:    chunkSize,
		})

		if err != nil {
			// تشخیص قطع اتصال توسط کاربر یا کلاینت
			if errors.Is(err, context.Canceled) {
				return context.Canceled
			}

			// مدیریت محدودیت Flood تلگرام
			if seconds, ok := tgerr.AsFloodWait(err); ok {
				wait := time.Duration(seconds) * time.Second
				logger.Warn("Flood wait triggered", slog.Duration("duration", wait))
				time.Sleep(wait)
				continue
			}
			return err
		}

		if chunk, ok := res.(*tg.UploadFile); ok {
			data := chunk.Bytes

			// اطمینان از اینکه دقیقا به اندازه بازه درخواستی دیتا بفرستیم
			remaining := endOffset - offset + 1
			if int64(len(data)) > remaining {
				data = data[:remaining]
			}

			// ارسال دیتا به کلاینت
			n, err := w.Write(data)
			if err != nil {
				// اگر کلاینت ارتباط را قطع کرده باشد (مثل Cancel در Aria2)
				return context.Canceled
			}
			offset += int64(n)

			// ارسال فوری داده‌ها (Buffer Flush) برای جلوگیری از Timeout کلاینت
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		} else {
			// در صورتی که فایل به انتها رسیده باشد یا پاسخی دریافت نشود
			break
		}
	}

	return nil
}
