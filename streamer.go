package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

func handleFileStream(ctx context.Context, w http.ResponseWriter, r *http.Request, api *tg.Client, botID int64, accessHash uint64, location *tg.InputDocumentFileLocation, size int64) {
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Content-Type", "application/octet-stream")

	startOffset, endOffset := int64(0), size-1
	
	// ۱. مدیریت دقیق Range
	rangeHeader := r.Header.Get("Range")
	if rangeHeader != "" && strings.HasPrefix(rangeHeader, "bytes=") {
		parts := strings.Split(strings.TrimPrefix(rangeHeader, "bytes="), "-")
		if s, err := strconv.ParseInt(parts[0], 10, 64); err == nil { startOffset = s }
		if len(parts) > 1 && parts[1] != "" {
			if e, err := strconv.ParseInt(parts[1], 10, 64); err == nil { endOffset = e }
		}
		// اطمینان از اینکه endOffset از حجم فایل بیشتر نشود
		if endOffset >= size { endOffset = size - 1 }
		
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", startOffset, endOffset, size))
		w.Header().Set("Content-Length", strconv.FormatInt(endOffset-startOffset+1, 10))
		w.WriteHeader(http.StatusPartialContent)
	} else {
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	}

	if r.Method == http.MethodHead { return }

	loc := &tg.InputDocumentFileLocation{
		ID: location.ID, AccessHash: int64(accessHash), FileReference: location.FileReference,
	}

	const chunkSize = 1024 * 1024 // 1MB
	offset := startOffset

	for offset <= endOffset {
		// ۲. محاسبه دقیق لیمیت برای تلگرام (نباید از انتهای بازه فراتر برود)
		// تلگرام لیمیت غیر مضرب ۱۰۲۴ را رد می‌کند، پس همیشه ۱ مگابایت می‌گیریم
		// اما فقط بخش مورد نیاز را به کلاینت می‌فرستیم
		res, err := api.UploadGetFile(ctx, &tg.UploadGetFileRequest{
			Location: loc,
			Offset:   offset,
			Limit:    chunkSize,
		})

		if err != nil {
			// مدیریت FloodWait
			if seconds, ok := tgerr.AsFloodWait(err); ok {
				d := time.Duration(seconds); if seconds <= 1000 { d *= time.Second }
				time.Sleep(d)
				continue 
			}
			logger.Error("telegram.fetch.error", slog.String("err", err.Error()))
			return // اینجا EOF رخ می‌دهد چون Content-Length کامل پر نشده
		}

		if chunk, ok := res.(*tg.UploadFile); ok {
			data := chunk.Bytes
			
			// ۳. برش حیاتی دیتا (Trim)
			// اگر دیتای دریافتی بیشتر از چیزی است که تا انتهای فایل/بازه مانده
			remainingBytes := endOffset - offset + 1
			if int64(len(data)) > remainingBytes {
				data = data[:remainingBytes]
			}

			n, err := w.Write(data)
			if err != nil {
				return // کلاینت قطع شده
			}
			offset += int64(n)
			
			// اگر به پایان بازه رسیدیم، حلقه را بشکن
			if offset > endOffset { break }
		} else {
			break
		}
	}
}