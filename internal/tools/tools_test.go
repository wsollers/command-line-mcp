package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/wsollers/command-line-mcp/internal/sandbox"
)

// testServer wires Register's tools to a live in-process MCP client over
// InMemoryTransport, so these tests exercise the real request/response
// path (JSON marshal/unmarshal of arguments and results through the SDK)
// rather than calling handler functions directly.
func testServer(t *testing.T, root string) *mcp.ClientSession {
	t.Helper()
	sb, err := sandbox.New([]string{root}, false)
	if err != nil {
		t.Fatalf("sandbox.New: %v", err)
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "shellmcp-test", Version: "test"}, nil)
	Register(server, sb)

	clientTransport, serverTransport := mcp.NewInMemoryTransports()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	if _, err := server.Connect(ctx, serverTransport, nil); err != nil {
		t.Fatalf("server.Connect: %v", err)
	}

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "test"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client.Connect: %v", err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

func callTool(t *testing.T, session *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(%s, %v): %v", name, args, err)
	}
	return res
}

func textOf(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	if len(res.Content) == 0 {
		t.Fatalf("result has no content")
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("result content[0] is %T, not *mcp.TextContent", res.Content[0])
	}
	return tc.Text
}

func structuredAs(t *testing.T, res *mcp.CallToolResult, v any) {
	t.Helper()
	b, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshaling StructuredContent: %v", err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("unmarshaling StructuredContent into %T: %v", v, err)
	}
}

func TestReadFileTextAuto(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "hello.txt"), []byte("hello, world"), 0o644); err != nil {
		t.Fatal(err)
	}
	session := testServer(t, root)

	res := callTool(t, session, "read_file", map[string]any{"path": "hello.txt"})
	if res.IsError {
		t.Fatalf("unexpected error: %s", textOf(t, res))
	}
	if got := textOf(t, res); got != "hello, world" {
		t.Errorf("Content text = %q, want %q", got, "hello, world")
	}
	var out readFileResult
	structuredAs(t, res, &out)
	if out.Encoding != "text" {
		t.Errorf("Encoding = %q, want %q", out.Encoding, "text")
	}
	if out.Text != "hello, world" {
		t.Errorf("StructuredContent.Text = %q, want %q", out.Text, "hello, world")
	}
	if out.ContentBase64 != "" {
		t.Errorf("StructuredContent.ContentBase64 should be empty for a text result, got %q", out.ContentBase64)
	}
}

func TestReadFileBinaryAuto(t *testing.T) {
	root := t.TempDir()
	raw := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0xff, 0xd8, 0xfe}
	if err := os.WriteFile(filepath.Join(root, "img.bin"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	session := testServer(t, root)

	res := callTool(t, session, "read_file", map[string]any{"path": "img.bin"})
	if res.IsError {
		t.Fatalf("unexpected error: %s", textOf(t, res))
	}
	var out readFileResult
	structuredAs(t, res, &out)
	if out.Encoding != "base64" {
		t.Fatalf("Encoding = %q, want %q", out.Encoding, "base64")
	}
	decoded, err := base64.StdEncoding.DecodeString(out.ContentBase64)
	if err != nil {
		t.Fatalf("StructuredContent.ContentBase64 is not valid base64: %v", err)
	}
	if string(decoded) != string(raw) {
		t.Errorf("round-tripped bytes = %x, want %x", decoded, raw)
	}
	// The Content block (what a caller sees without touching
	// StructuredContent) must carry the same base64 text, not the raw
	// (corrupting) bytes.
	if textOf(t, res) != out.ContentBase64 {
		t.Errorf("Content text does not match StructuredContent.ContentBase64")
	}
}

func TestReadFileForceTextOnBinaryErrors(t *testing.T) {
	root := t.TempDir()
	raw := []byte{0xff, 0xfe, 0x00, 0x01}
	if err := os.WriteFile(filepath.Join(root, "bad.bin"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	session := testServer(t, root)

	res := callTool(t, session, "read_file", map[string]any{"path": "bad.bin", "encoding": "text"})
	if !res.IsError {
		t.Fatalf("expected an error result for encoding=text on non-UTF-8 content")
	}
}

func TestReadFileForceBase64OnText(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "hello.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	session := testServer(t, root)

	res := callTool(t, session, "read_file", map[string]any{"path": "hello.txt", "encoding": "base64"})
	if res.IsError {
		t.Fatalf("unexpected error: %s", textOf(t, res))
	}
	var out readFileResult
	structuredAs(t, res, &out)
	if out.Encoding != "base64" {
		t.Errorf("Encoding = %q, want %q", out.Encoding, "base64")
	}
	decoded, err := base64.StdEncoding.DecodeString(out.ContentBase64)
	if err != nil || string(decoded) != "hi" {
		t.Errorf("decoded content = %q, err %v; want %q", decoded, err, "hi")
	}
}

func TestReadFileUnknownEncoding(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "hello.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	session := testServer(t, root)

	res := callTool(t, session, "read_file", map[string]any{"path": "hello.txt", "encoding": "rot13"})
	if !res.IsError {
		t.Fatalf("expected an error result for an unknown encoding value")
	}
}

func TestWriteFileText(t *testing.T) {
	root := t.TempDir()
	session := testServer(t, root)

	res := callTool(t, session, "write_file", map[string]any{"path": "out.txt", "content": "plain text"})
	if res.IsError {
		t.Fatalf("unexpected error: %s", textOf(t, res))
	}
	got, err := os.ReadFile(filepath.Join(root, "out.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "plain text" {
		t.Errorf("file content = %q, want %q", got, "plain text")
	}
}

func TestWriteFileBase64RoundTrip(t *testing.T) {
	root := t.TempDir()
	raw := []byte{0x00, 0x89, 0xff, 0x10, 0x20, 0x7f}
	if err := os.WriteFile(filepath.Join(root, "src.bin"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	session := testServer(t, root)

	// Read it back as base64 (as a real caller would for a binary
	// file), then write that exact blob to a new path, simulating the
	// intended relay pattern: the model never constructs the base64
	// text itself, only copies what read_file already gave it.
	readRes := callTool(t, session, "read_file", map[string]any{"path": "src.bin"})
	var readOut readFileResult
	structuredAs(t, readRes, &readOut)
	if readOut.Encoding != "base64" {
		t.Fatalf("expected base64 encoding for binary source, got %q", readOut.Encoding)
	}

	writeRes := callTool(t, session, "write_file", map[string]any{
		"path":           "dst.bin",
		"content_base64": readOut.ContentBase64,
	})
	if writeRes.IsError {
		t.Fatalf("unexpected error: %s", textOf(t, writeRes))
	}

	got, err := os.ReadFile(filepath.Join(root, "dst.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(raw) {
		t.Errorf("round-tripped file = %x, want %x", got, raw)
	}
}

func TestWriteFileRejectsBothContentFields(t *testing.T) {
	root := t.TempDir()
	session := testServer(t, root)

	res := callTool(t, session, "write_file", map[string]any{
		"path":           "out.txt",
		"content":        "text",
		"content_base64": base64.StdEncoding.EncodeToString([]byte("bytes")),
	})
	if !res.IsError {
		t.Fatalf("expected an error when both content and content_base64 are set")
	}
}

func TestWriteFileRejectsInvalidBase64(t *testing.T) {
	root := t.TempDir()
	session := testServer(t, root)

	res := callTool(t, session, "write_file", map[string]any{
		"path":           "out.bin",
		"content_base64": "not valid base64!!",
	})
	if !res.IsError {
		t.Fatalf("expected an error for invalid base64 content")
	}
}

func TestCopyFile(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "src.txt"), []byte("copy me"), 0o644); err != nil {
		t.Fatal(err)
	}
	session := testServer(t, root)

	res := callTool(t, session, "copy_file", map[string]any{"src": "src.txt", "dst": "dst.txt"})
	if res.IsError {
		t.Fatalf("unexpected error: %s", textOf(t, res))
	}
	got, err := os.ReadFile(filepath.Join(root, "dst.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "copy me" {
		t.Errorf("copied content = %q, want %q", got, "copy me")
	}
	// Original must be untouched.
	orig, err := os.ReadFile(filepath.Join(root, "src.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(orig) != "copy me" {
		t.Errorf("source content changed: %q", orig)
	}
}

func TestCopyFileBinaryIntegrity(t *testing.T) {
	root := t.TempDir()
	raw := make([]byte, 256)
	for i := range raw {
		raw[i] = byte(i)
	}
	if err := os.WriteFile(filepath.Join(root, "src.bin"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	session := testServer(t, root)

	res := callTool(t, session, "copy_file", map[string]any{"src": "src.bin", "dst": "dst.bin"})
	if res.IsError {
		t.Fatalf("unexpected error: %s", textOf(t, res))
	}
	got, err := os.ReadFile(filepath.Join(root, "dst.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(raw) {
		t.Errorf("copied binary content does not match byte-for-byte")
	}
}

func TestCopyFileRefusesExistingWithoutOverwrite(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "src.txt"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "dst.txt"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	session := testServer(t, root)

	res := callTool(t, session, "copy_file", map[string]any{"src": "src.txt", "dst": "dst.txt"})
	if !res.IsError {
		t.Fatalf("expected an error when dst exists and overwrite is not set")
	}
	got, err := os.ReadFile(filepath.Join(root, "dst.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "old" {
		t.Errorf("dst was modified despite missing overwrite=true: %q", got)
	}
}

func TestCopyFileOverwrite(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "src.txt"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "dst.txt"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	session := testServer(t, root)

	res := callTool(t, session, "copy_file", map[string]any{"src": "src.txt", "dst": "dst.txt", "overwrite": true})
	if res.IsError {
		t.Fatalf("unexpected error: %s", textOf(t, res))
	}
	got, err := os.ReadFile(filepath.Join(root, "dst.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new" {
		t.Errorf("dst content = %q, want %q", got, "new")
	}
}

func TestCopyFileRefusesDirectorySource(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "adir"), 0o755); err != nil {
		t.Fatal(err)
	}
	session := testServer(t, root)

	res := callTool(t, session, "copy_file", map[string]any{"src": "adir", "dst": "dst.txt"})
	if !res.IsError {
		t.Fatalf("expected an error when src is a directory")
	}
}

// TestStructuredContentIsAlwaysAnObject guards against the class of bug
// found earlier in ls/list_allowed_dirs: a client that validates
// structuredContent against the tool's outputSchema (as real clients do)
// rejects a bare JSON array. Every tool that returns structured content
// must marshal to a JSON object.
func TestStructuredContentIsAlwaysAnObject(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "hello.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "adir"), 0o755); err != nil {
		t.Fatal(err)
	}
	session := testServer(t, root)

	calls := []struct {
		name string
		args map[string]any
	}{
		{"read_file", map[string]any{"path": "hello.txt"}},
		{"write_file", map[string]any{"path": "out.txt", "content": "x"}},
		{"copy_file", map[string]any{"src": "hello.txt", "dst": "hello2.txt"}},
		{"ls", map[string]any{"path": "."}},
		{"list_allowed_dirs", map[string]any{}},
	}
	for _, c := range calls {
		t.Run(c.name, func(t *testing.T) {
			res := callTool(t, session, c.name, c.args)
			if res.IsError {
				t.Fatalf("unexpected error: %s", textOf(t, res))
			}
			if res.StructuredContent == nil {
				t.Fatalf("%s: StructuredContent is nil", c.name)
			}
			b, err := json.Marshal(res.StructuredContent)
			if err != nil {
				t.Fatalf("%s: marshal StructuredContent: %v", c.name, err)
			}
			var raw json.RawMessage = b
			trimmed := raw
			for len(trimmed) > 0 && (trimmed[0] == ' ' || trimmed[0] == '\t' || trimmed[0] == '\n') {
				trimmed = trimmed[1:]
			}
			if len(trimmed) == 0 || trimmed[0] != '{' {
				t.Errorf("%s: StructuredContent marshaled to non-object: %s", c.name, b)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// structured error codes
// ---------------------------------------------------------------------------

func errCodeOf(t *testing.T, res *mcp.CallToolResult) errorCode {
	t.Helper()
	var out errorResult
	structuredAs(t, res, &out)
	if out.OK {
		t.Fatalf("errorResult.OK = true on an IsError result")
	}
	return out.Error.Code
}

func TestErrorCodePathNotFound(t *testing.T) {
	root := t.TempDir()
	session := testServer(t, root)
	res := callTool(t, session, "read_file", map[string]any{"path": "does-not-exist.txt"})
	if !res.IsError {
		t.Fatalf("expected an error result")
	}
	if code := errCodeOf(t, res); code != codePathNotFound {
		t.Errorf("code = %q, want %q", code, codePathNotFound)
	}
}

func TestErrorCodePathOutsideRoot(t *testing.T) {
	root := t.TempDir()
	session := testServer(t, root)
	res := callTool(t, session, "read_file", map[string]any{"path": "../../../etc/passwd"})
	if !res.IsError {
		t.Fatalf("expected an error result")
	}
	if code := errCodeOf(t, res); code != codePathOutsideRoot {
		t.Errorf("code = %q, want %q", code, codePathOutsideRoot)
	}
}

func TestErrorCodeBothContentFields(t *testing.T) {
	root := t.TempDir()
	session := testServer(t, root)
	res := callTool(t, session, "write_file", map[string]any{
		"path": "out.txt", "content": "a", "content_base64": base64.StdEncoding.EncodeToString([]byte("b")),
	})
	if !res.IsError {
		t.Fatalf("expected an error result")
	}
	if code := errCodeOf(t, res); code != codeBothContentFields {
		t.Errorf("code = %q, want %q", code, codeBothContentFields)
	}
}

func TestErrorCodeAllThreeContentFieldsSet(t *testing.T) {
	root := t.TempDir()
	session := testServer(t, root)
	res := callTool(t, session, "write_file", map[string]any{
		"path": "out.txt", "content": "a", "content_base64": "Yg==", "blob_handle": "blob_x",
	})
	if !res.IsError {
		t.Fatalf("expected an error result")
	}
	if code := errCodeOf(t, res); code != codeBothContentFields {
		t.Errorf("code = %q, want %q", code, codeBothContentFields)
	}
}

func TestErrorCodeNotUTF8Text(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "bad.bin"), []byte{0xff, 0xfe}, 0o644); err != nil {
		t.Fatal(err)
	}
	session := testServer(t, root)
	res := callTool(t, session, "read_file", map[string]any{"path": "bad.bin", "encoding": "text"})
	if !res.IsError {
		t.Fatalf("expected an error result")
	}
	if code := errCodeOf(t, res); code != codeNotUTF8Text {
		t.Errorf("code = %q, want %q", code, codeNotUTF8Text)
	}
}

func TestErrorCodeInvalidBase64(t *testing.T) {
	root := t.TempDir()
	session := testServer(t, root)
	res := callTool(t, session, "write_file", map[string]any{"path": "out.bin", "content_base64": "not valid base64!!"})
	if !res.IsError {
		t.Fatalf("expected an error result")
	}
	if code := errCodeOf(t, res); code != codeInvalidBase64 {
		t.Errorf("code = %q, want %q", code, codeInvalidBase64)
	}
}

func TestErrorCodePathAlreadyExistsOnCopy(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "src.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "dst.txt"), []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}
	session := testServer(t, root)
	res := callTool(t, session, "copy_file", map[string]any{"src": "src.txt", "dst": "dst.txt"})
	if !res.IsError {
		t.Fatalf("expected an error result")
	}
	if code := errCodeOf(t, res); code != codePathAlreadyExists {
		t.Errorf("code = %q, want %q", code, codePathAlreadyExists)
	}
}

func TestErrorCodeNotAFileOnCopy(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "adir"), 0o755); err != nil {
		t.Fatal(err)
	}
	session := testServer(t, root)
	res := callTool(t, session, "copy_file", map[string]any{"src": "adir", "dst": "dst.txt"})
	if !res.IsError {
		t.Fatalf("expected an error result")
	}
	if code := errCodeOf(t, res); code != codeNotAFile {
		t.Errorf("code = %q, want %q", code, codeNotAFile)
	}
}

func TestErrorCodeRmRefusesRoot(t *testing.T) {
	root := t.TempDir()
	session := testServer(t, root)
	res := callTool(t, session, "rm", map[string]any{"path": ".", "recursive": true})
	if !res.IsError {
		t.Fatalf("expected an error result")
	}
	if code := errCodeOf(t, res); code != codePermissionDenied {
		t.Errorf("code = %q, want %q", code, codePermissionDenied)
	}
}

func TestErrorCodeRmDirectoryNotEmpty(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "adir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "adir", "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	session := testServer(t, root)
	res := callTool(t, session, "rm", map[string]any{"path": "adir"})
	if !res.IsError {
		t.Fatalf("expected an error result")
	}
	if code := errCodeOf(t, res); code != codeDirectoryNotEmpty {
		t.Errorf("code = %q, want %q", code, codeDirectoryNotEmpty)
	}
}

func TestErrorCodeExecEmptyCommand(t *testing.T) {
	root := t.TempDir()
	session := testServer(t, root)
	res := callTool(t, session, "exec", map[string]any{"command": ""})
	if !res.IsError {
		t.Fatalf("expected an error result")
	}
	if code := errCodeOf(t, res); code != codeInvalidArgument {
		t.Errorf("code = %q, want %q", code, codeInvalidArgument)
	}
}

func TestErrorCodeExecStartFailed(t *testing.T) {
	root := t.TempDir()
	session := testServer(t, root)
	res := callTool(t, session, "exec", map[string]any{"command": "/no/such/binary-xyz"})
	if !res.IsError {
		t.Fatalf("expected an error result")
	}
	if code := errCodeOf(t, res); code != codeProcessStartFailed {
		t.Errorf("code = %q, want %q", code, codeProcessStartFailed)
	}
}

// TestClassifyRuntimeDisabled checks sandbox.ErrRuntimeDisabled classifies
// as PERMISSION_DENIED. Exercised at the sandbox level directly rather
// than through add_allowed_dir/remove_allowed_dir, since testServer wires
// up a runtime-disabled sandbox and those tools aren't even registered on
// it (see Register's RuntimeAllowed gate).
func TestClassifyRuntimeDisabled(t *testing.T) {
	sb, err := sandbox.New([]string{t.TempDir()}, false)
	if err != nil {
		t.Fatal(err)
	}
	_, err = sb.AddRuntimeRoot(t.TempDir())
	if err == nil {
		t.Fatal("expected an error from AddRuntimeRoot on a runtime-disabled sandbox")
	}
	if code := classify(err); code != codePermissionDenied {
		t.Errorf("classify(runtime-disabled error) = %q, want %q", code, codePermissionDenied)
	}
}

// ---------------------------------------------------------------------------
// blob handles
// ---------------------------------------------------------------------------

func TestReadFileSmallBinaryStaysInline(t *testing.T) {
	root := t.TempDir()
	raw := []byte{0x00, 0x01, 0x02, 0xff}
	if err := os.WriteFile(filepath.Join(root, "small.bin"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	session := testServer(t, root)
	res := callTool(t, session, "read_file", map[string]any{"path": "small.bin"})
	if res.IsError {
		t.Fatalf("unexpected error: %s", textOf(t, res))
	}
	var out readFileResult
	structuredAs(t, res, &out)
	if out.Encoding != "base64" {
		t.Errorf("Encoding = %q, want %q (small file should stay inline)", out.Encoding, "base64")
	}
	if out.BlobHandle != "" {
		t.Errorf("BlobHandle unexpectedly set for a small file: %q", out.BlobHandle)
	}
}

// largeBinaryFixture returns size bytes guaranteed to be invalid UTF-8
// (0xFF is never a valid byte anywhere in a UTF-8 sequence, so its mere
// presence makes the whole buffer invalid regardless of what surrounds
// it), rather than relying on an incidental byte pattern that might
// happen to decode as valid UTF-8 and silently fail to exercise the
// binary/blob code path at all. An earlier version of these tests used
// an all-zero buffer for exactly this reason — all-zero bytes are valid
// UTF-8 (NUL, U+0000), so that fixture never actually triggered the blob
// path it was meant to test.
func largeBinaryFixture(size int) []byte {
	raw := make([]byte, size)
	for i := range raw {
		raw[i] = byte(i) // varied content, not that it matters for validity
	}
	raw[0] = 0xff // guarantees invalid UTF-8 regardless of the rest
	return raw
}

func TestReadFileLargeBinaryReturnsBlobHandle(t *testing.T) {
	root := t.TempDir()
	raw := largeBinaryFixture(blobThresholdBytes + 1)
	if err := os.WriteFile(filepath.Join(root, "large.bin"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	session := testServer(t, root)
	res := callTool(t, session, "read_file", map[string]any{"path": "large.bin"})
	if res.IsError {
		t.Fatalf("unexpected error: %s", textOf(t, res))
	}
	var out readFileResult
	structuredAs(t, res, &out)
	if out.Encoding != "blob" {
		t.Fatalf("Encoding = %q, want %q", out.Encoding, "blob")
	}
	if out.BlobHandle == "" {
		t.Fatal("BlobHandle is empty")
	}
	if out.ContentBase64 != "" {
		t.Errorf("ContentBase64 unexpectedly set alongside a blob handle")
	}
	if out.SizeBytes != int64(len(raw)) {
		t.Errorf("SizeBytes = %d, want %d", out.SizeBytes, len(raw))
	}
	// The Content block should be a human-readable note, not the raw
	// bytes or a giant base64 string — this is the entire point of the
	// blob mechanism (see docs/api-spec.md §18): never inline a large
	// payload into the caller's context.
	if len(textOf(t, res)) > 500 {
		t.Errorf("Content text is %d bytes long; expected a short note, not inlined content", len(textOf(t, res)))
	}
}

func TestWriteFileFromBlobHandle(t *testing.T) {
	root := t.TempDir()
	raw := largeBinaryFixture(blobThresholdBytes + 100)
	if err := os.WriteFile(filepath.Join(root, "large.bin"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	session := testServer(t, root)

	readRes := callTool(t, session, "read_file", map[string]any{"path": "large.bin"})
	var readOut readFileResult
	structuredAs(t, readRes, &readOut)
	if readOut.Encoding != "blob" {
		t.Fatalf("expected a blob handle, got encoding %q", readOut.Encoding)
	}

	writeRes := callTool(t, session, "write_file", map[string]any{
		"path": "copy.bin", "blob_handle": readOut.BlobHandle,
	})
	if writeRes.IsError {
		t.Fatalf("unexpected error: %s", textOf(t, writeRes))
	}

	got, err := os.ReadFile(filepath.Join(root, "copy.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(raw) {
		t.Errorf("written file does not match the original byte-for-byte")
	}
}

func TestWriteFileUnknownBlobHandle(t *testing.T) {
	root := t.TempDir()
	session := testServer(t, root)
	res := callTool(t, session, "write_file", map[string]any{"path": "out.bin", "blob_handle": "blob_never_issued"})
	if !res.IsError {
		t.Fatalf("expected an error result")
	}
	if code := errCodeOf(t, res); code != codeBlobNotFound {
		t.Errorf("code = %q, want %q", code, codeBlobNotFound)
	}
}

func TestReleaseBlob(t *testing.T) {
	root := t.TempDir()
	raw := largeBinaryFixture(blobThresholdBytes + 1)
	if err := os.WriteFile(filepath.Join(root, "large.bin"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	session := testServer(t, root)

	readRes := callTool(t, session, "read_file", map[string]any{"path": "large.bin"})
	var readOut readFileResult
	structuredAs(t, readRes, &readOut)

	releaseRes := callTool(t, session, "release_blob", map[string]any{"handle": readOut.BlobHandle})
	if releaseRes.IsError {
		t.Fatalf("unexpected error: %s", textOf(t, releaseRes))
	}
	var releaseOut map[string]any
	structuredAs(t, releaseRes, &releaseOut)
	if released, _ := releaseOut["released"].(bool); !released {
		t.Errorf("release_blob reported released=false for a live handle")
	}

	// The handle is now gone: a subsequent write_file against it fails.
	writeRes := callTool(t, session, "write_file", map[string]any{"path": "out.bin", "blob_handle": readOut.BlobHandle})
	if !writeRes.IsError {
		t.Fatalf("expected write_file to fail against a released handle")
	}
	if code := errCodeOf(t, writeRes); code != codeBlobNotFound {
		t.Errorf("code = %q, want %q", code, codeBlobNotFound)
	}
}

func TestReleaseBlobUnknownHandle(t *testing.T) {
	root := t.TempDir()
	session := testServer(t, root)
	res := callTool(t, session, "release_blob", map[string]any{"handle": "blob_never_issued"})
	if res.IsError {
		t.Fatalf("release_blob on an unknown handle should not itself be an error: %s", textOf(t, res))
	}
	var out map[string]any
	structuredAs(t, res, &out)
	if released, _ := out["released"].(bool); released {
		t.Errorf("release_blob reported released=true for an unknown handle")
	}
}

// ---------------------------------------------------------------------------
// find
// ---------------------------------------------------------------------------

func mustWriteFile(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestFindByNameGlob(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, root, "a.go", "package a")
	mustWriteFile(t, root, "b.txt", "not go")
	mustWriteFile(t, root, "sub/c.go", "package sub")
	session := testServer(t, root)

	res := callTool(t, session, "find", map[string]any{"root": ".", "name_glob": "**/*.go"})
	if res.IsError {
		t.Fatalf("unexpected error: %s", textOf(t, res))
	}
	var out findResult
	structuredAs(t, res, &out)
	if out.Count != 2 {
		t.Fatalf("Count = %d, want 2 (matches: %+v)", out.Count, out.Matches)
	}
}

func TestFindByContentRegex(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, root, "has.txt", "TODO: fix this")
	mustWriteFile(t, root, "hasnot.txt", "all done")
	session := testServer(t, root)

	res := callTool(t, session, "find", map[string]any{"root": ".", "content_regex": `\bTODO\b`})
	if res.IsError {
		t.Fatalf("unexpected error: %s", textOf(t, res))
	}
	var out findResult
	structuredAs(t, res, &out)
	if out.Count != 1 || out.Matches[0].Path != "has.txt" {
		t.Errorf("Matches = %+v, want just has.txt", out.Matches)
	}
}

func TestFindInvalidNameGlob(t *testing.T) {
	root := t.TempDir()
	session := testServer(t, root)
	res := callTool(t, session, "find", map[string]any{"root": ".", "name_glob": "[unterminated"})
	if !res.IsError {
		t.Fatal("expected an error for an invalid name_glob pattern")
	}
	if code := errCodeOf(t, res); code != codeInvalidGlob {
		t.Errorf("code = %q, want %q", code, codeInvalidGlob)
	}
}

func TestFindInvalidContentRegex(t *testing.T) {
	root := t.TempDir()
	session := testServer(t, root)
	res := callTool(t, session, "find", map[string]any{"root": ".", "content_regex": "("})
	if !res.IsError {
		t.Fatal("expected an error for an invalid content_regex pattern")
	}
	if code := errCodeOf(t, res); code != codeInvalidRegex {
		t.Errorf("code = %q, want %q", code, codeInvalidRegex)
	}
}

func TestFindRootNotADirectory(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, root, "file.txt", "x")
	session := testServer(t, root)
	res := callTool(t, session, "find", map[string]any{"root": "file.txt"})
	if !res.IsError {
		t.Fatal("expected an error when root is a file, not a directory")
	}
	if code := errCodeOf(t, res); code != codeNotADirectory {
		t.Errorf("code = %q, want %q", code, codeNotADirectory)
	}
}

func TestFindMaxResultsTruncates(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		mustWriteFile(t, root, name, "x")
	}
	session := testServer(t, root)
	res := callTool(t, session, "find", map[string]any{"root": ".", "max_results": 2})
	if res.IsError {
		t.Fatalf("unexpected error: %s", textOf(t, res))
	}
	var out findResult
	structuredAs(t, res, &out)
	if out.Count != 2 || !out.Truncated {
		t.Errorf("Count = %d, Truncated = %v, want 2, true", out.Count, out.Truncated)
	}
}

// ---------------------------------------------------------------------------
// replace
// ---------------------------------------------------------------------------

func TestReplaceLiteralDryRun(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, root, "a.txt", "OldName is OldName")
	session := testServer(t, root)

	res := callTool(t, session, "replace", map[string]any{
		"root": ".", "search": "OldName", "replace": "NewName", "dry_run": true,
	})
	if res.IsError {
		t.Fatalf("unexpected error: %s", textOf(t, res))
	}
	var out replaceResult
	structuredAs(t, res, &out)
	if out.TotalReplacements != 2 || out.FilesChanged != 1 {
		t.Fatalf("TotalReplacements = %d, FilesChanged = %d, want 2, 1", out.TotalReplacements, out.FilesChanged)
	}
	got, err := os.ReadFile(filepath.Join(root, "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "OldName is OldName" {
		t.Errorf("dry_run modified the file: content = %q", got)
	}
}

func TestReplaceLiteralWrites(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, root, "a.txt", "OldName is OldName")
	mustWriteFile(t, root, "b.txt", "unrelated content")
	session := testServer(t, root)

	res := callTool(t, session, "replace", map[string]any{
		"root": ".", "search": "OldName", "replace": "NewName",
	})
	if res.IsError {
		t.Fatalf("unexpected error: %s", textOf(t, res))
	}
	var out replaceResult
	structuredAs(t, res, &out)
	if out.TotalReplacements != 2 || out.FilesChanged != 1 {
		t.Fatalf("TotalReplacements = %d, FilesChanged = %d, want 2, 1", out.TotalReplacements, out.FilesChanged)
	}
	got, err := os.ReadFile(filepath.Join(root, "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "NewName is NewName" {
		t.Errorf("content = %q, want %q", got, "NewName is NewName")
	}
	unrelated, err := os.ReadFile(filepath.Join(root, "b.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(unrelated) != "unrelated content" {
		t.Errorf("replace touched a file with no match: content = %q", unrelated)
	}
}

func TestReplaceRegexWithCaptureGroup(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, root, "a.txt", "call foo(1, 2) and foo(3, 4)")
	session := testServer(t, root)

	res := callTool(t, session, "replace", map[string]any{
		"root": ".", "search": `foo\(([0-9]+), ([0-9]+)\)`, "replace": "bar($2, $1)", "is_regex": true,
	})
	if res.IsError {
		t.Fatalf("unexpected error: %s", textOf(t, res))
	}
	got, err := os.ReadFile(filepath.Join(root, "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	want := "call bar(2, 1) and bar(4, 3)"
	if string(got) != want {
		t.Errorf("content = %q, want %q", got, want)
	}
}

func TestReplaceHonorsNameGlob(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, root, "a.go", "OldName")
	mustWriteFile(t, root, "b.txt", "OldName")
	session := testServer(t, root)

	res := callTool(t, session, "replace", map[string]any{
		"root": ".", "name_glob": "**/*.go", "search": "OldName", "replace": "NewName",
	})
	if res.IsError {
		t.Fatalf("unexpected error: %s", textOf(t, res))
	}
	var out replaceResult
	structuredAs(t, res, &out)
	if out.FilesChanged != 1 || out.Files[0].Path != "a.go" {
		t.Fatalf("Files = %+v, want just a.go changed", out.Files)
	}
	txt, err := os.ReadFile(filepath.Join(root, "b.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(txt) != "OldName" {
		t.Errorf("replace touched b.txt despite name_glob excluding it: content = %q", txt)
	}
}

func TestReplaceSkipsBinaryFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "bin.dat"), []byte{0xff, 0x00, 'O', 'l', 'd'}, 0o644); err != nil {
		t.Fatal(err)
	}
	session := testServer(t, root)
	res := callTool(t, session, "replace", map[string]any{"root": ".", "search": "Old", "replace": "New"})
	if res.IsError {
		t.Fatalf("unexpected error: %s", textOf(t, res))
	}
	var out replaceResult
	structuredAs(t, res, &out)
	if out.FilesChanged != 0 {
		t.Errorf("FilesChanged = %d, want 0 (binary file must be skipped, not corrupted)", out.FilesChanged)
	}
}

func TestReplaceEmptySearchRejected(t *testing.T) {
	root := t.TempDir()
	session := testServer(t, root)
	res := callTool(t, session, "replace", map[string]any{"root": ".", "search": "", "replace": "x"})
	if !res.IsError {
		t.Fatal("expected an error for an empty search string")
	}
	if code := errCodeOf(t, res); code != codeInvalidArgument {
		t.Errorf("code = %q, want %q", code, codeInvalidArgument)
	}
}

func TestReplaceInvalidRegex(t *testing.T) {
	root := t.TempDir()
	session := testServer(t, root)
	res := callTool(t, session, "replace", map[string]any{"root": ".", "search": "(", "replace": "x", "is_regex": true})
	if !res.IsError {
		t.Fatal("expected an error for an invalid regex search pattern")
	}
	if code := errCodeOf(t, res); code != codeInvalidRegex {
		t.Errorf("code = %q, want %q", code, codeInvalidRegex)
	}
}
