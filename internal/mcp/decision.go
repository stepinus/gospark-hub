package mcp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"gospark-hub/internal/store"
)

// decisionQuestionTypes is upstream QUESTION_TYPES.
var decisionQuestionTypes = map[string]bool{"choice": true, "score": true, "noul": true}

// pickDecisionBackend is upstream decision_service.pick_backend.
func (s *Server) pickDecisionBackend(ctx context.Context, model string) (string, error) {
	installed, err := s.installedSet(ctx)
	if err != nil {
		return "", err
	}
	var slugs []string
	for slug := range s.decisionBackends() {
		if installed[slug] {
			slugs = append(slugs, slug)
		}
	}
	if model != "" {
		if !contains(slugs, model) {
			return "", imageErrorf("'%s' is not an installed decision model. "+
				"Installed: %s.", model, orNone(slugs))
		}
		slugs = []string{model}
	}
	idx, err := s.pickReady(ctx, slugs, "decision model")
	if err != nil {
		return "", err
	}
	return slugs[idx], nil
}

func orNone(list []string) string {
	if len(list) == 0 {
		return "none"
	}
	return strings.Join(list, ", ")
}

// checkQuestions is upstream decision_service._check.
func checkQuestions(raw any) (map[string]any, error) {
	if text, ok := raw.(string); ok {
		var decoded any
		if err := json.Unmarshal([]byte(strings.TrimSpace(text)), &decoded); err == nil {
			raw = decoded
		}
	}
	questions, ok := raw.(map[string]any)
	if !ok || len(questions) == 0 {
		return nil, imageErrorf("'questions' must map each question id to a question.")
	}
	for qid, qAny := range questions {
		q, ok := qAny.(map[string]any)
		if !ok {
			return nil, imageErrorf("Question '%s': 'type' must be choice, score or noul.", qid)
		}
		qType, _ := q["type"].(string)
		if !decisionQuestionTypes[qType] {
			return nil, imageErrorf("Question '%s': 'type' must be choice, score or noul.", qid)
		}
		criteria := q["criteria"]
		if qType == "choice" {
			m, ok := criteria.(map[string]any)
			if !ok || len(m) < 2 {
				return nil, imageErrorf("Question '%s': a choice needs 'criteria' mapping at least two "+
					"option ids to their descriptions.", qid)
			}
		}
		if qType == "score" {
			m, ok := criteria.([]any)
			if !ok || len(m) < 2 {
				return nil, imageErrorf("Question '%s': a score needs 'criteria' listing at least two "+
					"levels, lowest first.", qid)
			}
		}
	}
	return questions, nil
}

// decide is upstream decision_service.decide.
func (s *Server) decide(ctx context.Context, args map[string]any, user *store.User) (any, error) {
	state := args["state"]
	if state == nil || state == "" {
		return nil, imageErrorf("'state' is required: the text or JSON the decision is about " +
			"(for media alone, say what the images or videos are).")
	}
	questions, err := checkQuestions(args["questions"])
	if err != nil {
		return nil, err
	}
	body := map[string]any{"state": state, "questions": questions}
	slug, err := s.pickDecisionBackend(ctx, argString(args, "model"))
	if err != nil {
		return nil, err
	}
	for _, pair := range []struct {
		key    string
		suffix string
		max    int
	}{{"images", ".png", maxInputBytes}, {"videos", ".mp4", videoMaxInputBytes}} {
		refs := argStringList(args, pair.key)
		if len(refs) == 0 {
			continue
		}
		encoded := make([]any, 0, len(refs))
		for _, ref := range refs {
			raw, err := s.loadInput(ctx, ref, user, pair.suffix, pair.max)
			if err != nil {
				return nil, err
			}
			encoded = append(encoded, base64.StdEncoding.EncodeToString(raw))
		}
		body[pair.key] = encoded
	}
	raw, _ := json.Marshal(body)
	req, err := s.newAppRequest(ctx, http.MethodPost,
		s.app.InternalURL(slug)+"/v1/systemone", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	var reply map[string]any
	status, _, err := s.doJSON(req, &reply)
	if err != nil {
		return nil, imageErrorf("%s refused the request: %v", slug, err)
	}
	if status != http.StatusOK {
		message := ""
		if errObj, ok := reply["error"].(map[string]any); ok {
			message, _ = errObj["message"].(string)
		}
		if message == "" {
			message = fmt.Sprintf("HTTP %d", status)
		}
		return nil, imageErrorf("%s refused the request: %s", slug, message)
	}
	out := newOrderedMap()
	out.Set("model", slug)
	out.Set("answers", reply["answers"])
	return out, nil
}
