package main

import (
	"context"
	"fmt"
	"net/http"

	"github.com/gotd/td/tg"
)

func handleFileStream(ctx context.Context, w http.ResponseWriter, api *tg.Client, location tg.InputFileLocationClass, size int64) {
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", size))
	w.Header().Set("Accept-Ranges", "bytes")

	const chunkSize = 1024 * 1024 // 1MB
	offset := int64(0)

	for offset < size {
		res, err := api.UploadGetFile(ctx, &tg.UploadGetFileRequest{
			Location: location,
			Offset:   int64(offset),
			Limit:    chunkSize,
		})
		if err != nil {
			return
		}

		if chunk, ok := res.(*tg.UploadFile); ok {
			_, _ = w.Write(chunk.Bytes)
			offset += int64(len(chunk.Bytes))
		} else {
			break
		}
	}
}
