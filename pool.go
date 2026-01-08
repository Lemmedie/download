package main

import (
	"context"
	"fmt"
	"sync/atomic"

	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"
)

type BotPool struct {
	clients []*tg.Client
	index   uint64
}

func NewBotPool(ctx context.Context, apiID int, apiHash string, tokens []string) (*BotPool, error) {
	pool := &BotPool{}
	for _, token := range tokens {
		sessionPath := fmt.Sprintf("session_%s.json", token[:8])

		client := telegram.NewClient(apiID, apiHash, telegram.Options{
			SessionStorage: &session.FileStorage{Path: sessionPath},
			NoUpdates:      true,
		})

		go func(t string) {
			_ = client.Run(ctx, func(ctx context.Context) error {
				status, _ := client.Auth().Status(ctx)
				if !status.Authorized {
					_, _ = client.Auth().Bot(ctx, t)
				}
				<-ctx.Done()
				return nil
			})
		}(token)

		pool.clients = append(pool.clients, tg.NewClient(client))
	}
	return pool, nil
}

func (p *BotPool) GetNext() *tg.Client {
	idx := atomic.AddUint64(&p.index, 1) % uint64(len(p.clients))
	return p.clients[idx]
}
