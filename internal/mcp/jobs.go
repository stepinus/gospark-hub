package mcp

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

// maxJobs mirrors upstream image_service.MAX_JOBS / video_service.MAX_JOBS.
const maxJobs = 200

// jobInfo is what check_job returns upstream.
type jobInfo struct {
	JobID   string
	Status  string
	Model   string
	Kind    string
	Result  any
	Err     string
	Elapsed float64
}

type job struct {
	id      string
	status  string // "rendering" | "completed" | "failed"
	created time.Time
	model   string
	kind    string
	result  any
	err     string
	userID  int64
	done    chan struct{}
	elapsed float64
}

// jobManager runs renders out of band, so a client's timeout does not tear the
// render down; the result waits to be collected. Mirrors image_service._jobs.
type jobManager struct {
	mu   sync.Mutex
	jobs map[string]*job
}

func newJobManager() *jobManager {
	return &jobManager{jobs: map[string]*job{}}
}

func newJobID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// start runs work in its own goroutine and returns the job id at once.
func (m *jobManager) start(kind, model string, userID int64, work func() (any, error)) string {
	m.mu.Lock()
	for len(m.jobs) >= maxJobs {
		oldest := ""
		var oldestAt time.Time
		for id, j := range m.jobs {
			if oldest == "" || j.created.Before(oldestAt) {
				oldest, oldestAt = id, j.created
			}
		}
		if oldest == "" {
			break
		}
		delete(m.jobs, oldest)
	}
	j := &job{
		id: newJobID(), status: "rendering", created: time.Now(),
		model: model, kind: kind, userID: userID, done: make(chan struct{}),
	}
	m.jobs[j.id] = j
	m.mu.Unlock()

	go func() {
		defer close(j.done)
		value, err := work()
		m.mu.Lock()
		defer m.mu.Unlock()
		if err != nil {
			j.status = "failed"
			if ie, ok := err.(*ImageError); ok {
				j.err = ie.Msg
			} else {
				j.err = fmt.Sprintf("%T: %v", err, err)
			}
			// Match upstream's "<Type>: <msg>" for non-ImageError failures.
			j.err = errString(err)
		} else {
			j.status = "completed"
			j.result = value
		}
		j.elapsed = time.Since(j.created).Seconds()
	}()
	return j.id
}

// errString mirrors the upstream error stringification in start_job.
func errString(err error) string {
	if ie, ok := err.(*ImageError); ok {
		return ie.Msg
	}
	if we, ok := err.(*WebError); ok {
		return we.Msg
	}
	return fmt.Sprintf("%T: %v", err, err)
}

// owns reports whether user may collect the job.
func (m *jobManager) owns(j *job, userID int64, authEnabled bool) bool {
	if !authEnabled {
		return true
	}
	return userID != 0 && j.userID == userID
}

// check returns the job state, waiting up to wait for it to finish.
func (m *jobManager) check(id string, wait time.Duration, userID int64, authEnabled bool) (jobInfo, error) {
	m.mu.Lock()
	j, ok := m.jobs[id]
	if !ok || !m.owns(j, userID, authEnabled) {
		m.mu.Unlock()
		return jobInfo{}, imageErrorf("No job %s — it may have expired; start a new one.", id)
	}
	m.mu.Unlock()

	if wait > 0 {
		select {
		case <-j.done:
		case <-time.After(wait):
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	elapsed := j.elapsed
	if elapsed == 0 {
		elapsed = time.Since(j.created).Seconds()
	}
	return jobInfo{
		JobID: j.id, Status: j.status, Model: j.model, Kind: j.kind,
		Result: j.result, Err: j.err, Elapsed: float64(int64(elapsed + 0.5)),
	}, nil
}

// rendersOn counts jobs still rendering on slug, for stop_model's refusal.
func (m *jobManager) rendersOn(slug string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, j := range m.jobs {
		if j.model == slug && j.status == "rendering" {
			n++
		}
	}
	return n
}
