package tools

import (
	"errors"
	"os"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/wsollers/command-line-mcp/internal/blob"
	"github.com/wsollers/command-line-mcp/internal/sandbox"
)

// errorCode is the fixed vocabulary from docs/api-spec.md §4. Every tool
// failure carries one, so a calling agent can branch on failure kind
// programmatically instead of pattern-matching an error message.
type errorCode string

const (
	codePathNotFound       errorCode = "PATH_NOT_FOUND"
	codePathAlreadyExists  errorCode = "PATH_ALREADY_EXISTS"
	codeNotAFile           errorCode = "NOT_A_FILE"
	codeDirectoryNotEmpty  errorCode = "DIRECTORY_NOT_EMPTY"
	codePermissionDenied   errorCode = "PERMISSION_DENIED"
	codePathOutsideRoot    errorCode = "PATH_OUTSIDE_ROOT"
	codeInvalidBase64      errorCode = "INVALID_BASE64"
	codeBothContentFields  errorCode = "BOTH_CONTENT_FIELDS_SET"
	codeNotUTF8Text        errorCode = "NOT_UTF8_TEXT"
	codeBlobNotFound       errorCode = "BLOB_NOT_FOUND"
	codeBlobExpired        errorCode = "BLOB_EXPIRED"
	codeInvalidArgument    errorCode = "INVALID_ARGUMENT"
	codeProcessStartFailed errorCode = "PROCESS_START_FAILED"
	codeIOError            errorCode = "IO_ERROR"
)

// toolError and errorResult mirror docs/api-spec.md §4's error shape:
// {"ok": false, "error": {"code", "message", "path", "details"}}.
type toolError struct {
	Code    errorCode      `json:"code"`
	Message string         `json:"message"`
	Path    string         `json:"path,omitempty"`
	Details map[string]any `json:"details,omitempty"`
}

type errorResult struct {
	OK    bool      `json:"ok"`
	Error toolError `json:"error"`
}

// codedError builds an error CallToolResult carrying code both as plain
// text (Content, for a human or anything just reading the message) and as
// structured JSON (StructuredContent, for a caller that wants to branch on
// failure kind). path and details are optional — pass "" / nil when they
// don't apply.
//
// This is returned as the *first* value from a tool handler alongside a
// nil second ("Out") value; the SDK only overwrites CallToolResult.
// StructuredContent when the handler's Out value is non-nil (see
// server.go's dispatch — AddTool's generated wrapper marshals Out into
// StructuredContent only when Out != nil), so a nil Out here leaves this
// function's StructuredContent untouched on the wire.
func codedError(code errorCode, path, message string, details map[string]any) *mcp.CallToolResult {
	out := errorResult{OK: false, Error: toolError{Code: code, Message: message, Path: path, Details: details}}
	return &mcp.CallToolResult{
		IsError:           true,
		Content:           []mcp.Content{&mcp.TextContent{Text: message}},
		StructuredContent: out,
	}
}

// errFromErr classifies a Go error surfaced from a sandbox or os
// filesystem operation and builds a codedError from it.
//
// Classification is best-effort: precise for the cases with a portable
// sentinel (errors.Is against os.ErrNotExist / os.ErrExist /
// os.ErrPermission, and this package's own sandbox.ErrOutsideRoot /
// sandbox.ErrRuntimeDisabled), a pragmatic message-substring check for
// "directory not empty" (the standard library has no portable
// cross-platform sentinel for ENOTEMPTY / ERROR_DIR_NOT_EMPTY), and
// IO_ERROR for everything else. Good enough to route the overwhelming
// majority of real failures to a precise code without hand-classifying
// every call site.
func errFromErr(err error, path string) *mcp.CallToolResult {
	return codedError(classify(err), path, err.Error(), nil)
}

func classify(err error) errorCode {
	switch {
	case errors.Is(err, sandbox.ErrOutsideRoot):
		return codePathOutsideRoot
	case errors.Is(err, sandbox.ErrRuntimeDisabled):
		return codePermissionDenied
	case errors.Is(err, os.ErrNotExist):
		return codePathNotFound
	// Checked before the os.ErrExist case below: Go's syscall.Errno.Is
	// maps ENOTEMPTY to os.ErrExist (confirmed empirically — os.Remove
	// on a non-empty directory satisfies errors.Is(err, os.ErrExist)),
	// so the more specific directory-not-empty check has to run first
	// or it's unreachable, shadowed by the generic "already exists"
	// case below.
	case strings.Contains(err.Error(), "directory not empty"):
		return codeDirectoryNotEmpty
	case errors.Is(err, os.ErrExist):
		return codePathAlreadyExists
	case errors.Is(err, os.ErrPermission):
		return codePermissionDenied
	default:
		return codeIOError
	}
}

// blobErrCode maps a blob.Store error to its errorCode.
func blobErrCode(err error) errorCode {
	switch {
	case errors.Is(err, blob.ErrExpired):
		return codeBlobExpired
	case errors.Is(err, blob.ErrNotFound):
		return codeBlobNotFound
	default:
		return codeIOError
	}
}
