package cleanup

import (
	"log"
	"time"
	"github.com/HoshinoNeko/YATFHS/internal/storage"
)

func Start(store *storage.Store, interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for range ticker.C {
			n := store.DeleteExpired()
			if n > 0 {
				log.Printf("[cleanup] deleted %d expired files", n)
			}
		}
	}()
}
