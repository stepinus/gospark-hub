package mcp

import "fmt"

// ImageError is a failure worth showing to the model verbatim, mirroring the
// upstream image_service.ImageError / WebError handling in call_tool.
type ImageError struct{ Msg string }

func (e *ImageError) Error() string { return e.Msg }

func imageErrorf(format string, args ...any) *ImageError {
	return &ImageError{Msg: fmt.Sprintf(format, args...)}
}

// WebError is a search/fetch failure shown verbatim.
type WebError struct{ Msg string }

func (e *WebError) Error() string { return e.Msg }

func webErrorf(format string, args ...any) *WebError {
	return &WebError{Msg: fmt.Sprintf(format, args...)}
}
