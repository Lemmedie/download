package main

import (
	"context"
	"fmt"
	"log"
	"net/http"

	"github.com/gotd/td/tg"
)

func handleFileStream(ctx context.Context, w http.ResponseWriter, api *tg.Client, location tg.InputFileLocationClass, size int64) {
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", size))
	w.Header().Set("Accept-Ranges", "bytes")

	log.Printf("stream started size=%d", size)

	const chunkSize = 1024 * 1024 // 1MB
	offset := int64(0)

	for offset < size {
		res, err := api.UploadGetFile(ctx, &tg.UploadGetFileRequest{
			Location: location,
			Offset:   int64(offset),
			Limit:    chunkSize,
		})
		if err != nil {
			log.Printf("UploadGetFile error at offset=%d: %v", offset, err)
			return
		}

		if chunk, ok := res.(*tg.UploadFile); ok {
			_, err := w.Write(chunk.Bytes)
			if err != nil {
				log.Printf("write to response failed at offset=%d: %v", offset, err)
				return
			}
			offset += int64(len(chunk.Bytes))
			log.Printf("streamed chunk offset=%d size=%d", offset, len(chunk.Bytes))
		} else {
			log.Printf("unexpected UploadGetFile response type at offset=%d", offset)
			break
		}
	}
	log.Printf("stream finished size=%d", size)
}
