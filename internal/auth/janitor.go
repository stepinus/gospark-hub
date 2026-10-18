package auth

import (
	"log"
	"time"

	"gospark-hub/internal/store"
)

// StartJanitor runs the hourly expired-session cleanup. It executes once
// immediately and then every hour, matching the upstream lifespan task.
func StartJanitor(db *store.DB, logger *log.Logger) {
	go func() {
		for {
			if err := db.PurgeExpiredSessions(); err != nil {
				logger.Printf("janitor: purge expired sessions: %v", err)
			}
			time.Sleep(time.Hour)
		}
	}()
}
