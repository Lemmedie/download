package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"time"
)

var logger *slog.Logger

func init() {
	handler := slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{AddSource: false})
	logger = slog.New(handler)
}

func generateRequestID() string {
	b := make([]byte, 8)
	_, err := rand.Read(b)
	if err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
