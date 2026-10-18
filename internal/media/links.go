package media

import (
	"crypto/sha256"
	"encoding/hex"
	"time"

	"gospark-hub/internal/store"
)

const (
	// LinkTTL matches daemon/services/link_service.py.
	LinkTTL          = 15 * 60
	UploadPrefix     = "/link/upload/"
	DownloadPrefix   = "/link/download/"
)

// LinkService manages one-time upload and short-lived download tokens.
type LinkService struct {
	db *store.DB
}

// NewLinkService builds a LinkService.
func NewLinkService(db *store.DB) *LinkService {
	return &LinkService{db: db}
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Create mints a new link and returns the raw token and unix expiration.
func (s *LinkService) Create(kind string, userID int64, name string) (string, int64, error) {
	token, err := store.NewRandomToken(32)
	if err != nil {
		return "", 0, err
	}
	expires := time.Now().Unix() + LinkTTL
	if err := s.db.CreateMediaLink(hashToken(token), kind, userID, name, expires); err != nil {
		return "", 0, err
	}
	return token, expires, nil
}

// ClaimUpload reserves an upload link and returns its owner.
func (s *LinkService) ClaimUpload(token string) (*store.User, error) {
	now := time.Now().Unix()
	tokenHash := hashToken(token)
	ok, err := s.db.ClaimMediaUpload(tokenHash, now)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	owner, err := s.db.MediaLinkOwner(tokenHash)
	if err != nil {
		return nil, err
	}
	if owner == nil {
		_ = s.db.ReleaseMediaUpload(tokenHash)
		return nil, nil
	}
	return owner, nil
}

// ReleaseUpload makes an upload link usable again after a failed upload.
func (s *LinkService) ReleaseUpload(token string) error {
	return s.db.ReleaseMediaUpload(hashToken(token))
}

// DownloadTarget returns the file name and owner for a live download link.
func (s *LinkService) DownloadTarget(token string) (string, *store.User, error) {
	tokenHash := hashToken(token)
	now := time.Now().Unix()
	name, err := s.db.MediaDownloadTarget(tokenHash, now)
	if err != nil {
		return "", nil, err
	}
	if name == "" {
		return "", nil, nil
	}
	owner, err := s.db.MediaLinkOwner(tokenHash)
	if err != nil {
		return "", nil, err
	}
	if owner == nil {
		return "", nil, nil
	}
	return name, owner, nil
}

// PurgeExpired removes expired links.
func (s *LinkService) PurgeExpired() error {
	return s.db.PurgeExpiredMediaLinks(time.Now().Unix())
}
