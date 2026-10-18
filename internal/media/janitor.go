package media

import (
	"log"
	"time"
)

// StartJanitor runs the hourly expired-link cleanup. It executes once
// immediately and then every hour, matching the upstream lifespan task.
func StartJanitor(svc *LinkService, logger *log.Logger) {
	go func() {
		for {
			if err := svc.PurgeExpired(); err != nil {
				logger.Printf("janitor: purge expired media links: %v", err)
			}
			time.Sleep(time.Hour)
		}
	}()
}
