package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

func handleFileStream(ctx context.Context, w http.ResponseWriter, api *tg.Client, location tg.InputFileLocationClass, size int64) {
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", size))
	w.Header().Set("Accept-Ranges", "bytes")

	logger.Info("stream.started", slog.Int64("size", size))

	const chunkSize = 1024 * 1024 // 1MB
	offset := int64(0)

	for offset < size {
		res, err := api.UploadGetFile(ctx, &tg.UploadGetFileRequest{
			Location: location,
			Offset:   int64(offset),
			Limit:    chunkSize,
		})

		if err != nil {
			// چک کردن اینکه آیا ارور از نوع FLOOD_WAIT است یا خیر
			if seconds, ok := tgerr.AsFloodWait(err); ok {
				logger.Warn("flood wait detected", slog.Int("seconds", int(seconds)))

				// به اندازه ای که تلگرام دستور داده صبر کن (مثلاً ۲ ثانیه)
				time.Sleep(time.Duration(seconds) * time.Second)
				continue // دوباره همان چانک قبلی را درخواست بده
			}

			logger.Error("upload.getfile.error", slog.String("err", err.Error()))
			return
		}

		if chunk, ok := res.(*tg.UploadFile); ok {
			_, err := w.Write(chunk.Bytes)
			if err != nil {
				return // قطع اتصال از سمت کاربر
			}
			offset += int64(len(chunk.Bytes))

			// یک استراحت بسیار کوتاه برای اینکه تلگرام شک نکند
			time.Sleep(10 * time.Millisecond)
		} else {
			break
		}
	}
	logger.Info("stream.finished", slog.Int64("size", size))
}
