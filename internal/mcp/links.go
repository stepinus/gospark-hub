package mcp

import (
	"fmt"

	"gospark-hub/internal/media"
	"gospark-hub/internal/store"
)

// linkMinutes is upstream link_service.LINK_TTL // 60.
const linkMinutes = media.LinkTTL / 60

// ownFile is upstream mcp._own_file: the caller's Hub file behind args["url"].
func (s *Server) ownFile(args map[string]any, user *store.User) (string, error) {
	if user == nil {
		return "", imageErrorf("Files need a signed-in Hub account behind this connection.")
	}
	fileName := nameInURL(argString(args, "url"))
	if fileName == "" {
		return "", imageErrorf("'url' must be a Hub image, video, 3D model or song URL " +
			"(.../images/<id>.png, /videos/<id>.mp4, /meshes/<id>.glb " +
			"or /audio/<id>.wav).")
	}
	if s.media.Find(fileName) == "" || !s.media.CanRead(fileName, user) {
		return "", imageErrorf("No Hub file %s for this account "+
			"(it may have expired or been deleted).", fileName)
	}
	return fileName, nil
}

// linkTool is upstream mcp._link_tool: create_upload / create_download.
func (s *Server) linkTool(name string, args map[string]any, origin string, user *store.User) toolResult {
	if user == nil {
		return textResult("Links need a signed-in Hub account behind this connection.", true)
	}
	links := media.NewLinkService(s.store)
	if name == "create_upload" {
		token, expires, err := links.Create("upload", user.ID, "")
		if err != nil {
			return textResult(err.Error(), true)
		}
		link := origin + media.UploadPrefix + token
		curl := fmt.Sprintf("curl -sS --data-binary @<your file> '%s'", link)
		text := fmt.Sprintf("Upload link (one file, expires in %d minutes):\n%s\n\n"+
			"Run: %s\n"+
			"It prints JSON; pass its `url` to edit_image or generate_video. Images up to "+
			"%d MB, MP4/MOV videos up to %d MB, sounds up to "+
			"%d MB. A refused file leaves the link usable.",
			linkMinutes, link, curl,
			media.MaxImageBytes/(1<<20), media.MaxVideoBytes/(1<<20), media.MaxAudioBytes/(1<<20))
		structured := newOrderedMap()
		structured.Set("upload_url", link)
		structured.Set("method", "POST")
		structured.Set("curl", curl)
		structured.Set("expires_at", expires)
		return toolResult{
			Content:           []any{textBlock{Type: "text", Text: text}},
			StructuredContent: structured,
			IsError:           false,
		}
	}

	fileName, err := s.ownFile(args, user)
	if err != nil {
		return textResult(err.Error(), true)
	}
	token, expires, err := links.Create("download", user.ID, fileName)
	if err != nil {
		return textResult(err.Error(), true)
	}
	link := origin + media.DownloadPrefix + token
	curl := fmt.Sprintf("curl -sSfL -o %s '%s'", fileName, link)
	text := fmt.Sprintf("Download link for %s (expires in %d minutes):\n%s\n\nRun: %s",
		fileName, linkMinutes, link, curl)
	structured := newOrderedMap()
	structured.Set("download_url", link)
	structured.Set("file", fileName)
	structured.Set("curl", curl)
	structured.Set("expires_at", expires)
	return toolResult{
		Content:           []any{textBlock{Type: "text", Text: text}},
		StructuredContent: structured,
		IsError:           false,
	}
}
