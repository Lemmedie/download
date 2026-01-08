package main

import (
	"context"
	"errors"
	"fmt"
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
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", startOffset, endOffset, size))
		w.Header().Set("Content-Length", strconv.FormatInt(endOffset-startOffset+1, 10))
		w.WriteHeader(http.StatusPartialContent)
	} else {
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	}

	if r.Method == http.MethodHead {
		return
	}

	loc := &tg.InputDocumentFileLocation{
		ID: location.ID, AccessHash: int64(accessHash), FileReference: location.FileReference,
	}

	const chunkSize = 1024 * 1024 // 1MB
	offset := startOffset
	for offset <= endOffset {
		res, err := api.UploadGetFile(ctx, &tg.UploadGetFileRequest{
			Location: loc, Offset: offset, Limit: chunkSize,
		})

		if err != nil {
			if seconds, ok := tgerr.AsFloodWait(err); ok {
				d := time.Duration(seconds)
				if seconds <= 1000 {
					d *= time.Second
				}
				time.Sleep(d)
				continue
			}
			if errors.Is(err, context.Canceled) {
				return
			}
			break
		}

		if chunk, ok := res.(*tg.UploadFile); ok {
			data := chunk.Bytes
			if remaining := endOffset - offset + 1; int64(len(data)) > remaining {
				data = data[:remaining]
			}
			n, err := w.Write(data)
			if err != nil {
				return
			}
			offset += int64(n)
		} else {
			break
		}
	}
}
