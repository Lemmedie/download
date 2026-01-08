package main

import (
	"context"
	"fmt"
	"log"
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
					log.Printf("auth status error for bot %s: %v", short, err)
				}
				if !status.Authorized {
					_, err := c.Auth().Bot(ctx, t)
					if err != nil {
						log.Printf("bot auth failed for %s: %v", short, err)
					} else {
						log.Printf("bot authorized for %s", short)
					}
				} else {
					log.Printf("bot already authorized for %s", short)
				}
				<-ctx.Done()
				return nil
			}); err != nil {
				log.Printf("client.Run exited for %s: %v", short, err)
			} else {
				log.Printf("client.Run exited for %s", short)
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
