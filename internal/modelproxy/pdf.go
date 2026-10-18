package modelproxy

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// normalizePDFFileParts converts file parts with PDF data URLs into text parts.
func normalizePDFFileParts(body map[string]any) bool {
	msgs, ok := body["messages"].([]any)
	if !ok {
		return false
	}
	mutated := false
	for _, m := range msgs {
		msg, ok := m.(map[string]any)
		if !ok {
			continue
		}
		content, ok := msg["content"].([]any)
		if !ok {
			continue
		}
		for i, part := range content {
			p, ok := part.(map[string]any)
			if !ok || p["type"] != "file" {
				continue
			}
			file, ok := p["file"].(map[string]any)
			if !ok {
				continue
			}
			dataURL, _ := file["file_data"].(string)
			text := extractPDFTextFromDataURL(dataURL)
			if text == "" {
				continue
			}
			filename, _ := file["filename"].(string)
			if filename == "" {
				filename = "attachment.pdf"
			}
			content[i] = map[string]any{
				"type": "text",
				"text": fmt.Sprintf("[PDF attachment: %s]\n%s", filename, text),
			}
			mutated = true
		}
	}
	return mutated
}

func extractPDFTextFromDataURL(url string) string {
	prefix := "data:application/pdf;base64,"
	if !strings.HasPrefix(url, prefix) {
		return ""
	}
	if _, err := exec.LookPath("pdftotext"); err != nil {
		return ""
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(url, prefix))
	if err != nil {
		return ""
	}
	f, err := os.CreateTemp("", "*.pdf")
	if err != nil {
		return ""
	}
	name := f.Name()
	_, writeErr := f.Write(raw)
	closeErr := f.Close()
	defer os.Remove(name)
	if writeErr != nil || closeErr != nil {
		return ""
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "pdftotext", "-layout", name, "-").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
