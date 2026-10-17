package cleanup

import "time"

func Start(clean func(), interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for range ticker.C {
			clean()
		}
	}()
}
