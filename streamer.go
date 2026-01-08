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

// پارامتر accessHash حذف شد چون در خودِ location موجود است
func handleFileStream(ctx context.Context, w http.ResponseWriter, r *http.Request, api *tg.Client, location *tg.InputDocumentFileLocation, size int64) error {

	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Content-Type", "application/octet-stream")

	startOffset, endOffset := int64(0), size-1
	rangeHeader := r.Header.Get("Range")

	if rangeHeader != "" && strings.HasPrefix(rangeHeader, "bytes=") {
		parts := strings.Split(strings.TrimPrefix(rangeHeader, "bytes="), "-")
		if s, err := strconv.ParseInt(parts[0], 10, 64); err == nil {
			startOffset = s
		}
		if len(parts) > 1 && parts[1] != "" {
			if e, err := strconv.ParseInt(parts[1], 10, 64); err == nil {
				endOffset = e
			}
		}
		if endOffset >= size {
			endOffset = size - 1
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", startOffset, endOffset, size))
		w.Header().Set("Content-Length", strconv.FormatInt(endOffset-startOffset+1, 10))
		w.WriteHeader(http.StatusPartialContent)
	} else {
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	}

	if r.Method == http.MethodHead {
		return nil
	}

	logger.Info("streaming.started",
		slog.Int64("file_id", location.ID),
		slog.Int64("offset", startOffset),
	)

	const chunkSize = 1024 * 1024 // 1MB chunks
	offset := startOffset

	for offset <= endOffset {
		// استفاده مستقیم از پارامتر ورودی location
		res, err := api.UploadGetFile(ctx, &tg.UploadGetFileRequest{
			Location: location,
			Offset:   offset,
			Limit:    chunkSize,
		})

		if err != nil {
			if seconds, ok := tgerr.AsFloodWait(err); ok {
				d := time.Duration(seconds) * time.Second
				time.Sleep(d)
				continue
			}
			return err
		}

		if chunk, ok := res.(*tg.UploadFile); ok {
			data := chunk.Bytes
			remainingBytes := endOffset - offset + 1
			if int64(len(data)) > remainingBytes {
				data = data[:remainingBytes]
			}

			n, err := w.Write(data)
			if err != nil {
				// احتمالاً قطع اتصال توسط کاربر (Broken Pipe)
				return err
			}
			offset += int64(n)

			// فلاش کردن داده‌ها برای استریم روان‌تر در صورت پشتیبانی ResponseWriter
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		} else {
			break
		}
	}
	return nil
}
