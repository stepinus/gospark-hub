// Package auth routes wires the auth/admin HTTP API into the web router.
package auth

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"sort"
	"strconv"

	"gospark-hub/internal/auth"
	"gospark-hub/internal/config"
	"gospark-hub/internal/store"
	"gospark-hub/internal/web"
)

// Deps is the same shape as auth.Deps; re-exported here so callers only need
// to import this package.
type Deps = auth.Deps

// Register mounts auth and admin routes on r and installs the auth middleware.
func Register(r *web.Router, deps Deps) {
	mw := auth.NewMiddleware(deps)
	r.Use(mw.Handler)

	h := &handler{deps: deps, cfg: deps.Config, log: deps.Logger}

	// Public auth routes.
	r.Handle("GET", "/api/auth/state", h.state)
	r.Handle("POST", "/api/auth/register", h.register)
	r.Handle("POST", "/api/auth/login", h.login)
	r.Handle("POST", "/api/auth/logout", h.logout)

	// Authenticated auth routes.
	r.Handle("GET", "/api/auth/me", h.me)
	r.Handle("PATCH", "/api/auth/me", h.updateMe)
	r.Handle("POST", "/api/auth/me/api-key/rotate", h.rotateKey)
	r.Handle("GET", "/api/auth/me/usage", h.myUsage)

	// Admin routes.
	r.Handle("GET", "/api/admin/users", h.listUsers)
	r.Handle("POST", "/api/admin/users", h.createUser)
	r.Handle("PATCH", "/api/admin/users/{id}", h.updateUser)
	r.Handle("DELETE", "/api/admin/users/{id}", h.deleteUser)
	r.Handle("GET", "/api/admin/users/{id}/usage", h.userUsage)
}

type handler struct {
	deps Deps
	cfg  config.Config
	log  *log.Logger
}

func (h *handler) state(w http.ResponseWriter, r *http.Request) {
	count, err := h.deps.Store.UserCount()
	if err != nil {
		h.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	u := auth.CurrentUser(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"auth_enabled": h.cfg.AuthEnabled,
		"needs_setup":  count == 0,
		"user":         auth.PublicUser(u),
	})
}

type credentials struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	Name     string `json:"name"`
}

func (h *handler) register(w http.ResponseWriter, r *http.Request) {
	var body credentials
	if err := decodeRequired(r, &body, []string{"email", "password"}); err != nil {
		h.writeErr(w, http.StatusUnprocessableEntity, err)
		return
	}
	email, err := auth.ValidateEmail(body.Email)
	if err != nil {
		h.writeErr(w, http.StatusBadRequest, err)
		return
	}
	if _, err := auth.ValidatePassword(body.Password); err != nil {
		h.writeErr(w, http.StatusBadRequest, err)
		return
	}
	hash, err := auth.HashPassword(body.Password)
	if err != nil {
		h.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	key, err := auth.NewAPIKey()
	if err != nil {
		h.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	user, err := h.deps.Store.CreateUser(email, body.Name, hash, "", "", key)
	if err != nil {
		h.writeErr(w, http.StatusBadRequest, err)
		return
	}
	if user.Status != "active" {
		writeJSON(w, http.StatusOK, map[string]any{
			"status":  "pending",
			"message": "Your account was created and is waiting for an administrator to approve it.",
			"user":    auth.PublicUser(user),
		})
		return
	}
	token, err := h.deps.Store.CreateSession(user.ID, r.UserAgent(), auth.ClientIP(r), h.cfg.SessionTTLDays)
	if err != nil {
		h.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	auth.SetSessionCookie(w, r, h.cfg, token)
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "active",
		"user":   auth.PublicUser(user),
	})
}

type loginBody struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (h *handler) login(w http.ResponseWriter, r *http.Request) {
	var body loginBody
	if err := decodeRequired(r, &body, []string{"email", "password"}); err != nil {
		h.writeErr(w, http.StatusUnprocessableEntity, err)
		return
	}
	user, err := h.deps.Store.GetUserByEmail(body.Email)
	if err != nil {
		h.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if user == nil || !auth.VerifyPassword(body.Password, user.PasswordHash) {
		h.writeErr(w, http.StatusUnauthorized, errors.New("Incorrect email or password."))
		return
	}
	if user.Status == "pending" {
		h.writeErr(w, http.StatusForbidden, errors.New("Your account is waiting for an administrator to approve it."))
		return
	}
	if user.Status != "active" {
		h.writeErr(w, http.StatusForbidden, errors.New("This account has been disabled."))
		return
	}
	token, err := h.deps.Store.CreateSession(user.ID, r.UserAgent(), auth.ClientIP(r), h.cfg.SessionTTLDays)
	if err != nil {
		h.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	auth.SetSessionCookie(w, r, h.cfg, token)
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "active",
		"user":   auth.PublicUser(user),
	})
}

func (h *handler) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(h.cfg.SessionCookieName); err == nil && c.Value != "" {
		_ = h.deps.Store.DestroySession(c.Value)
	}
	auth.ClearSessionCookie(w, r, h.cfg)
	writeJSON(w, http.StatusOK, map[string]any{"status": "signed_out"})
}

func (h *handler) me(w http.ResponseWriter, r *http.Request) {
	u := auth.CurrentUser(r)
	if u == nil {
		h.writeErr(w, http.StatusUnauthorized, errors.New("Authentication required."))
		return
	}
	summary, err := h.deps.Store.UsageSummary(&u.ID)
	if err != nil {
		h.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	usage := emptyUsage()
	if v, ok := summary[u.ID]; ok {
		usage = v
	}
	writeJSON(w, http.StatusOK, mergeMaps(auth.PublicUser(u), map[string]any{
		"api_key": u.APIKey,
		"usage":   usage,
	}))
}

type updateMeBody struct {
	Name            *string `json:"name"`
	Email           *string `json:"email"`
	CurrentPassword *string `json:"current_password"`
	NewPassword     *string `json:"new_password"`
}

func (h *handler) updateMe(w http.ResponseWriter, r *http.Request) {
	u := auth.CurrentUser(r)
	if u == nil {
		h.writeErr(w, http.StatusUnauthorized, errors.New("Authentication required."))
		return
	}
	var body updateMeBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		h.writeErr(w, http.StatusUnprocessableEntity, err)
		return
	}
	fields := map[string]any{}
	if body.Name != nil {
		fields["name"] = *body.Name
	}
	wantsSensitive := body.Email != nil || body.NewPassword != nil
	if wantsSensitive {
		pw := ""
		if body.CurrentPassword != nil {
			pw = *body.CurrentPassword
		}
		if !auth.VerifyPassword(pw, u.PasswordHash) {
			h.writeErr(w, http.StatusForbidden, errors.New("Your current password is incorrect."))
			return
		}
		if body.Email != nil {
			if _, err := auth.ValidateEmail(*body.Email); err != nil {
				h.writeErr(w, http.StatusBadRequest, err)
				return
			}
			fields["email"] = *body.Email
		}
		if body.NewPassword != nil {
			if _, err := auth.ValidatePassword(*body.NewPassword); err != nil {
				h.writeErr(w, http.StatusBadRequest, err)
				return
			}
			hash, err := auth.HashPassword(*body.NewPassword)
			if err != nil {
				h.writeErr(w, http.StatusInternalServerError, err)
				return
			}
			fields["password_hash"] = hash
		}
	}
	updated, err := h.deps.Store.UpdateUser(u.ID, fields)
	if err != nil {
		h.writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, auth.PublicUser(updated))
}

func (h *handler) rotateKey(w http.ResponseWriter, r *http.Request) {
	u := auth.CurrentUser(r)
	if u == nil {
		h.writeErr(w, http.StatusUnauthorized, errors.New("Authentication required."))
		return
	}
	key, err := auth.NewAPIKey()
	if err != nil {
		h.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	updated, err := h.deps.Store.UpdateUser(u.ID, map[string]any{"api_key": key})
	if err != nil {
		h.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"api_key": updated.APIKey})
}

func (h *handler) myUsage(w http.ResponseWriter, r *http.Request) {
	u := auth.CurrentUser(r)
	if u == nil {
		h.writeErr(w, http.StatusUnauthorized, errors.New("Authentication required."))
		return
	}
	summary, err := h.deps.Store.UsageSummary(&u.ID)
	if err != nil {
		h.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	usage := emptyUsage()
	if v, ok := summary[u.ID]; ok {
		usage = v
	}
	models, err := h.deps.Store.TopModels(&u.ID, 5)
	if err != nil {
		h.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"usage":  usage,
		"models": models,
	})
}

// Admin handlers.

type userView struct {
	User    *store.User
	Usage   store.Usage
	Storage store.Storage
}

func (h *handler) listUsers(w http.ResponseWriter, r *http.Request) {
	admin, err := auth.RequireAdmin(r)
	if err != nil {
		h.writeErr(w, errStatus(err), err)
		return
	}
	_ = admin
	users, err := h.deps.Store.AllUsers()
	if err != nil {
		h.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	summary, err := h.deps.Store.UsageSummary(nil)
	if err != nil {
		h.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	space, err := h.deps.Store.SpaceByUser()
	if err != nil {
		h.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	models, err := h.deps.Store.TopModels(nil, 5)
	if err != nil {
		h.writeErr(w, http.StatusInternalServerError, err)
		return
	}

	views := make([]userView, 0, len(users))
	for _, u := range users {
		usage := emptyUsage()
		if v, ok := summary[u.ID]; ok {
			usage = v
		}
		st := store.Storage{Bytes: 0, Files: 0}
		if v, ok := space[u.ID]; ok {
			st = v
		}
		views = append(views, userView{User: u, Usage: usage, Storage: st})
	}

	sort.Slice(views, func(i, j int) bool {
		pi := 0
		if views[i].User.Status != "pending" {
			pi = 1
		}
		pj := 0
		if views[j].User.Status != "pending" {
			pj = 1
		}
		if pi != pj {
			return pi < pj
		}
		if views[i].Usage.TotalTokens != views[j].Usage.TotalTokens {
			return views[i].Usage.TotalTokens > views[j].Usage.TotalTokens
		}
		return views[i].User.Email < views[j].User.Email
	})

	ordered := make([]map[string]any, 0, len(views))
	for _, v := range views {
		ordered = append(ordered, mergeMaps(auth.PublicUser(v.User), map[string]any{
			"usage":   v.Usage,
			"storage": v.Storage,
		}))
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"users":  ordered,
		"totals": calcTotals(views, space),
		"models": models,
	})
}

func calcTotals(views []userView, space map[int64]store.Storage) map[string]any {
	var pending, active, requests, totalTokens, storageBytes int64
	for _, v := range views {
		if v.User.Status == "pending" {
			pending++
		}
		if v.User.Status == "active" {
			active++
		}
		requests += v.Usage.Requests
		totalTokens += v.Usage.TotalTokens
	}
	for _, s := range space {
		storageBytes += s.Bytes
	}
	return map[string]any{
		"users":         len(views),
		"pending":       pending,
		"active":        active,
		"requests":      requests,
		"total_tokens":  totalTokens,
		"storage_bytes": storageBytes,
	}
}

type createUserBody struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	Name     string `json:"name"`
	Role     string `json:"role"`
	Status   string `json:"status"`
}

func (h *handler) createUser(w http.ResponseWriter, r *http.Request) {
	if _, err := auth.RequireAdmin(r); err != nil {
		h.writeErr(w, errStatus(err), err)
		return
	}
	var body createUserBody
	if err := decodeRequired(r, &body, []string{"email", "password"}); err != nil {
		h.writeErr(w, http.StatusUnprocessableEntity, err)
		return
	}
	if err := validateEnums(body.Role, body.Status); err != nil {
		h.writeErr(w, http.StatusBadRequest, err)
		return
	}
	if body.Role == "" {
		body.Role = "user"
	}
	if body.Status == "" {
		body.Status = "active"
	}
	if _, err := auth.ValidateEmail(body.Email); err != nil {
		h.writeErr(w, http.StatusBadRequest, err)
		return
	}
	if _, err := auth.ValidatePassword(body.Password); err != nil {
		h.writeErr(w, http.StatusBadRequest, err)
		return
	}
	hash, err := auth.HashPassword(body.Password)
	if err != nil {
		h.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	key, err := auth.NewAPIKey()
	if err != nil {
		h.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	user, err := h.deps.Store.CreateUser(body.Email, body.Name, hash, body.Role, body.Status, key)
	if err != nil {
		h.writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusCreated, mergeMaps(auth.PublicUser(user), map[string]any{
		"usage":   emptyUsage(),
		"storage": store.Storage{Bytes: 0, Files: 0},
	}))
}

type updateUserBody struct {
	Name     *string `json:"name"`
	Email    *string `json:"email"`
	Password *string `json:"password"`
	Role     *string `json:"role"`
	Status   *string `json:"status"`
}

func (h *handler) updateUser(w http.ResponseWriter, r *http.Request) {
	if _, err := auth.RequireAdmin(r); err != nil {
		h.writeErr(w, errStatus(err), err)
		return
	}
	id, err := pathID(r)
	if err != nil {
		h.writeErr(w, http.StatusUnprocessableEntity, err)
		return
	}
	var body updateUserBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		h.writeErr(w, http.StatusUnprocessableEntity, err)
		return
	}
	role := ""
	status := ""
	if body.Role != nil {
		role = *body.Role
	}
	if body.Status != nil {
		status = *body.Status
	}
	if err := validateEnums(role, status); err != nil {
		h.writeErr(w, http.StatusBadRequest, err)
		return
	}

	target, err := h.deps.Store.GetUserByID(id)
	if err != nil {
		h.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if target == nil {
		h.writeErr(w, http.StatusNotFound, errors.New("No such account."))
		return
	}

	// Refuse change that removes the only active admin.
	losingAdmin := target.Role == "admin" && target.Status == "active" &&
		(body.Role != nil && *body.Role == "user" || (body.Status != nil && (*body.Status == "pending" || *body.Status == "rejected")))
	if losingAdmin {
		exclude := id
		if count, err := h.deps.Store.AdminCount(&exclude); err == nil && count == 0 {
			h.writeErr(w, http.StatusBadRequest, errors.New("This is the only administrator — promote someone else first."))
			return
		}
	}

	fields := map[string]any{}
	if body.Name != nil {
		fields["name"] = *body.Name
	}
	if body.Email != nil {
		if _, err := auth.ValidateEmail(*body.Email); err != nil {
			h.writeErr(w, http.StatusBadRequest, err)
			return
		}
		fields["email"] = *body.Email
	}
	if body.Password != nil {
		if _, err := auth.ValidatePassword(*body.Password); err != nil {
			h.writeErr(w, http.StatusBadRequest, err)
			return
		}
		hash, err := auth.HashPassword(*body.Password)
		if err != nil {
			h.writeErr(w, http.StatusInternalServerError, err)
			return
		}
		fields["password_hash"] = hash
	}
	if body.Role != nil {
		fields["role"] = *body.Role
	}
	if body.Status != nil {
		fields["status"] = *body.Status
		if *body.Status != target.Status {
			fields["reviewed_at"] = auth.NowISO()
		}
	}

	updated, err := h.deps.Store.UpdateUser(id, fields)
	if err != nil {
		h.writeErr(w, http.StatusBadRequest, err)
		return
	}
	if updated.Status != "active" || body.Password != nil {
		_ = h.deps.Store.DestroySessionsForUser(id)
	}

	summary, err := h.deps.Store.UsageSummary(&id)
	if err != nil {
		h.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	usage := emptyUsage()
	if v, ok := summary[id]; ok {
		usage = v
	}
	writeJSON(w, http.StatusOK, mergeMaps(auth.PublicUser(updated), map[string]any{
		"usage": usage,
	}))
}

func (h *handler) deleteUser(w http.ResponseWriter, r *http.Request) {
	admin, err := auth.RequireAdmin(r)
	if err != nil {
		h.writeErr(w, errStatus(err), err)
		return
	}
	id, err := pathID(r)
	if err != nil {
		h.writeErr(w, http.StatusUnprocessableEntity, err)
		return
	}
	if id == admin.ID {
		h.writeErr(w, http.StatusBadRequest, errors.New("You cannot delete your own account."))
		return
	}
	target, err := h.deps.Store.GetUserByID(id)
	if err != nil {
		h.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if target == nil {
		h.writeErr(w, http.StatusNotFound, errors.New("No such account."))
		return
	}
	if target.Role == "admin" && target.Status == "active" {
		exclude := id
		if count, err := h.deps.Store.AdminCount(&exclude); err == nil && count == 0 {
			h.writeErr(w, http.StatusBadRequest, errors.New("This is the only administrator — promote someone else first."))
			return
		}
	}
	if err := h.deps.Store.DeleteUser(id); err != nil {
		h.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "deleted"})
}

func (h *handler) userUsage(w http.ResponseWriter, r *http.Request) {
	if _, err := auth.RequireAdmin(r); err != nil {
		h.writeErr(w, errStatus(err), err)
		return
	}
	id, err := pathID(r)
	if err != nil {
		h.writeErr(w, http.StatusUnprocessableEntity, err)
		return
	}
	target, err := h.deps.Store.GetUserByID(id)
	if err != nil {
		h.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if target == nil {
		h.writeErr(w, http.StatusNotFound, errors.New("No such account."))
		return
	}
	summary, err := h.deps.Store.UsageSummary(&id)
	if err != nil {
		h.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	usage := emptyUsage()
	if v, ok := summary[id]; ok {
		usage = v
	}
	models, err := h.deps.Store.TopModels(&id, 5)
	if err != nil {
		h.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"usage":  usage,
		"models": models,
	})
}

// Helpers.

func (h *handler) writeErr(w http.ResponseWriter, status int, err error) {
	if status == http.StatusInternalServerError && h.log != nil {
		h.log.Printf("auth handler error: %v", err)
	}
	if ve, ok := err.(*validationError); ok {
		writeJSON(w, status, map[string]any{"detail": ve.Detail})
		return
	}
	writeJSON(w, status, map[string]any{"detail": err.Error()})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func emptyUsage() store.Usage {
	return store.Usage{
		Requests:         0,
		PromptTokens:     0,
		CompletionTokens: 0,
		TotalTokens:      0,
		LastUsedAt:       nil,
		Requests30d:      0,
		Tokens30d:        0,
		Requests24h:      0,
		Tokens24h:        0,
	}
}

func mergeMaps(a, b map[string]any) map[string]any {
	out := make(map[string]any, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

// validationDetail mirrors FastAPI's 422 body shape closely enough for
// clients that read detail[0].type and detail[0].loc.
type validationDetail struct {
	Type  string   `json:"type"`
	Loc   []string `json:"loc"`
	Msg   string   `json:"msg"`
	Input any      `json:"input"`
}

type validationError struct {
	Detail []validationDetail `json:"detail"`
}

func (v *validationError) Error() string { return "validation error" }

// decodeRequired decodes a JSON object into dst and reports the upstream 422
// "missing" detail for any field listed in required that is absent or null.
func decodeRequired(r *http.Request, dst any, required []string) error {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return &validationError{Detail: []validationDetail{{Type: "json_invalid", Loc: []string{"body"}, Msg: err.Error()}}}
	}
	if len(raw) == 0 {
		raw = []byte("{}")
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(raw, &probe); err != nil {
		return &validationError{Detail: []validationDetail{{Type: "json_invalid", Loc: []string{"body"}, Msg: err.Error()}}}
	}
	var details []validationDetail
	for _, f := range required {
		v, has := probe[f]
		if !has || string(v) == "null" || string(v) == `""` {
			details = append(details, validationDetail{Type: "missing", Loc: []string{"body", f}, Msg: "Field required"})
		}
	}
	if len(details) > 0 {
		return &validationError{Detail: details}
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return &validationError{Detail: []validationDetail{{Type: "json_invalid", Loc: []string{"body"}, Msg: err.Error()}}}
	}
	return nil
}

func validateEnums(role, status string) error {
	if role != "" && role != "admin" && role != "user" {
		return errors.New("Role must be admin or user.")
	}
	if status != "" && status != "pending" && status != "active" && status != "rejected" {
		return errors.New("Status must be pending, active or rejected.")
	}
	return nil
}

func pathID(r *http.Request) (int64, error) {
	s := r.PathValue("id")
	if s == "" {
		return 0, errors.New("missing id")
	}
	id, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, errors.New("invalid id")
	}
	return id, nil
}

func errStatus(err error) int {
	switch err.Error() {
	case "Authentication required.":
		return http.StatusUnauthorized
	case "Only an administrator can do that.":
		return http.StatusForbidden
	}
	return http.StatusBadRequest
}
