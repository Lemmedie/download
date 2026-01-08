package main

import (
	"context"
	"fmt"
	"log/slog"
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

		go func(t string, c *telegram.Client) {
			short := t
			if len(t) > 8 {
				short = t[:8]
			}
			if err := c.Run(ctx, func(ctx context.Context) error {
				status, err := c.Auth().Status(ctx)
				if err != nil {
					logger.Error("auth.status.error", slog.String("bot", short), slog.String("err", err.Error()))
				}
				if !status.Authorized {
					_, err := c.Auth().Bot(ctx, t)
					if err != nil {
						logger.Error("bot.auth.failed", slog.String("bot", short), slog.String("err", err.Error()))
					} else {
						logger.Info("bot.authorized", slog.String("bot", short))
					}
				} else {
					logger.Info("bot.already.authorized", slog.String("bot", short))
				}
				<-ctx.Done()
				return nil
			}); err != nil {
				logger.Error("client.run.exit", slog.String("bot", short), slog.String("err", err.Error()))
			} else {
				logger.Info("client.run.exit", slog.String("bot", short))
			}
		}(token, client)

		pool.clients = append(pool.clients, tg.NewClient(client))
	}
	return pool, nil
}

func (p *BotPool) GetNext() *tg.Client {
	if len(p.clients) == 0 {
		return nil
	}
	idx := atomic.AddUint64(&p.index, 1) % uint64(len(p.clients))
	return p.clients[idx]
}
