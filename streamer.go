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

func handleFileStream(ctx context.Context, w http.ResponseWriter, r *http.Request, api *tg.Client, location *tg.InputDocumentFileLocation, size int64) error {

	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Content-Type", "application/octet-stream")

	var startOffset, endOffset int64
	endOffset = size - 1

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

		// برای جلوگیری از Mismatch، طول محتوا باید دقیقاً برابر بازه درخواستی باشد
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", startOffset, endOffset, size))
		w.Header().Set("Content-Length", strconv.FormatInt(endOffset-startOffset+1, 10))
		w.WriteHeader(http.StatusPartialContent)
	} else {
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	}

	// اگر کلاینت فقط هدر خواست، بدنه ارسال نشود
	if r.Method == http.MethodHead {
		return nil
	}

	const chunkSize = 1024 * 1024 // 1MB
	offset := startOffset

	for offset <= endOffset {
		// دریافت تکه‌ای از فایل از تلگرام
		res, err := api.UploadGetFile(ctx, &tg.UploadGetFileRequest{
			Location: location,
			Offset:   offset,
			Limit:    chunkSize,
		})

		if err != nil {
			if seconds, ok := tgerr.AsFloodWait(err); ok {
				// مدیریت محدودیت زمانی تلگرام
				wait := time.Duration(seconds) * time.Second
				logger.Warn("Flood wait triggered", slog.Duration("duration", wait))
				time.Sleep(wait)
				continue
			}
			return err
		}

		if chunk, ok := res.(*tg.UploadFile); ok {
			data := chunk.Bytes

			// اطمینان از اینکه بیشتر از محدوده درخواستی ارسال نشود
			remaining := endOffset - offset + 1
			if int64(len(data)) > remaining {
				data = data[:remaining]
			}

			n, err := w.Write(data)
			if err != nil {
				// معمولاً به معنی قطع اتصال توسط کلاینت است
				return nil
			}
			offset += int64(n)

			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		} else {
			break
		}
	}

	return nil
}
