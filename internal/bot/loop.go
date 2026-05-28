package bot

import (
	"context"
	"errors"
	"log"
	"os"
	"time"
)

func (b *Bot) runWSLoop(ctx context.Context, sig <-chan os.Signal) error {
	backoff := time.Second
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-sig:
			return context.Canceled
		default:
		}

		err := b.consumeOnce(ctx, sig)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err == nil || errors.Is(err, context.Canceled) {
			return err
		}

		log.Printf("ws disconnected: %v; reconnect in %s", err, backoff)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-sig:
			return context.Canceled
		case <-time.After(backoff):
		}
		if backoff < 15*time.Second {
			backoff *= 2
			if backoff > 15*time.Second {
				backoff = 15 * time.Second
			}
		}
	}
}

