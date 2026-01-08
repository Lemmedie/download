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

func handleFileStream(ctx context.Context, w http.ResponseWriter, r *http.Request, api *tg.Client, location tg.InputFileLocationClass, size int64) {
	// ۱. تنظیم هدرهای پایه
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Accept-Ranges", "bytes")

	startOffset := int64(0)
	endOffset := size - 1

	// ۲. مدیریت Range Request (حیاتی برای دانلود منیجرها)
	rangeHeader := r.Header.Get("Range")
	if rangeHeader != "" && strings.HasPrefix(rangeHeader, "bytes=") {
		parts := strings.Split(strings.TrimPrefix(rangeHeader, "bytes="), "-")
		if len(parts) == 2 {
			if s, err := strconv.ParseInt(parts[0], 10, 64); err == nil {
				startOffset = s
			}
			if e, err := strconv.ParseInt(parts[1], 10, 64); err == nil && parts[1] != "" {
				endOffset = e
			}
		}

		if startOffset >= size || startOffset > endOffset {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", size))
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}

		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", startOffset, endOffset, size))
		w.Header().Set("Content-Length", fmt.Sprintf("%d", endOffset-startOffset+1))
		w.WriteHeader(http.StatusPartialContent)
	} else {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", size))
	}

	// ۳. پاسخ به متد HEAD (فقط هدرها را می‌خواهد)
	if r.Method == http.MethodHead {
		logger.Info("head request handled", slog.Int64("size", size))
		return
	}

	logger.Info("stream.started", slog.Int64("from", startOffset), slog.Int64("to", endOffset))

	// ۴. حلقه اصلی استریم با آفست داینامیک
	const chunkSize = 1024 * 1024 // 1MB
	offset := startOffset

	for offset <= endOffset {
		// محاسبه دقیق مقدار دیتای باقی‌مانده برای این چانک
		currentLimit := chunkSize
		if offset+int64(currentLimit) > endOffset {
			currentLimit = int(endOffset - offset + 1)
		}

		res, err := api.UploadGetFile(ctx, &tg.UploadGetFileRequest{
			Location: location,
			Offset:   offset,
			Limit:    currentLimit,
		})

		if err != nil {
			if seconds, ok := tgerr.AsFloodWait(err); ok {
				// اگر عدد خیلی بزرگ است (مثلاً بیشتر از ۱ میلیون)، یعنی نانوثانیه است
				waitDuration := time.Duration(seconds)
				if seconds > 1000000 {
					// مستقیم از خودش استفاده کن چون خودش Duration است
					logger.Warn("flood wait detected (nano)", slog.Any("duration", waitDuration))
				} else {
					// اگر عدد کوچک بود، یعنی واقعاً ثانیه است
					waitDuration = time.Duration(seconds) * time.Second
					logger.Warn("flood wait detected (sec)", slog.Int("seconds", int(seconds)))
				}

				time.Sleep(waitDuration)
				continue 
			}
			// اگر کاربر وسط دانلود صفحه را بست، بیخودی ارور لاگ نکن
			if errors.Is(err, context.Canceled) {
				return
			}
			logger.Error("upload.getfile.error", slog.String("err", err.Error()))
			return
		}

		if chunk, ok := res.(*tg.UploadFile); ok {
			n, err := w.Write(chunk.Bytes)
			if err != nil {
				return // کاربر اتصال را قطع کرد
			}
			offset += int64(n)

			// یک وقفه بسیار کوتاه برای جلوگیری از Flood تلگرام
			time.Sleep(10 * time.Millisecond)
		} else {
			break
		}
	}
	logger.Info("stream.finished", slog.Int64("offset", offset))
}
