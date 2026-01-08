package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/gotd/td/tg"
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
			logger.Error("upload.getfile.error", slog.Int64("offset", offset), slog.String("err", err.Error()))
			return
		}

		if chunk, ok := res.(*tg.UploadFile); ok {
			_, err := w.Write(chunk.Bytes)
			if err != nil {
				logger.Error("response.write.error", slog.Int64("offset", offset), slog.String("err", err.Error()))
				return
			}
			offset += int64(len(chunk.Bytes))
			logger.Info("stream.chunk", slog.Int64("offset", offset), slog.Int("size", len(chunk.Bytes)))
		} else {
			logger.Error("upload.getfile.unexpected_response", slog.Int64("offset", offset))
			break
		}
	}
	logger.Info("stream.finished", slog.Int64("size", size))
}
