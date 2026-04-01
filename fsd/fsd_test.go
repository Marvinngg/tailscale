package fsd

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGetFile tests reading a file via HTTP GET.
func TestGetFile(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hello world"), 0644)

	h := NewHandlers(Config{SharedDir: dir, InboxDir: filepath.Join(dir, "inbox")}, nil)
	req := httptest.NewRequest("GET", "/files/hello.txt", nil)
	w := httptest.NewRecorder()
	h.HandleFiles(w, req)

	if w.Code != 200 {
		t.Fatalf("status %d", w.Code)
	}
	if w.Body.String() != "hello world" {
		t.Fatalf("body = %q", w.Body.String())
	}
}

// TestPutFile tests writing a file via HTTP PUT.
func TestPutFile(t *testing.T) {
	dir := t.TempDir()
	h := NewHandlers(Config{SharedDir: dir, InboxDir: filepath.Join(dir, "inbox")}, nil)

	body := strings.NewReader("new file content")
	req := httptest.NewRequest("PUT", "/files/newfile.txt", body)
	w := httptest.NewRecorder()
	h.HandleFiles(w, req)

	if w.Code != 200 {
		t.Fatalf("status %d, body: %s", w.Code, w.Body.String())
	}

	data, err := os.ReadFile(filepath.Join(dir, "newfile.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "new file content" {
		t.Fatalf("file content = %q", string(data))
	}
}

// TestListDir tests directory listing.
func TestListDir(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("aaa"), 0644)
	os.WriteFile(filepath.Join(dir, "b.md"), []byte("bb"), 0644)
	os.Mkdir(filepath.Join(dir, "subdir"), 0755)

	h := NewHandlers(Config{SharedDir: dir, InboxDir: filepath.Join(dir, "inbox")}, nil)
	req := httptest.NewRequest("GET", "/files/", nil)
	w := httptest.NewRecorder()
	h.HandleFiles(w, req)

	if w.Code != 200 {
		t.Fatalf("status %d", w.Code)
	}

	var entries []struct {
		Name  string `json:"name"`
		IsDir bool   `json:"isDir"`
		Size  int64  `json:"size"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &entries); err != nil {
		t.Fatal(err)
	}

	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}

	names := make(map[string]bool)
	for _, e := range entries {
		names[e.Name] = true
	}
	if !names["a.txt"] || !names["b.md"] || !names["subdir"] {
		t.Fatalf("unexpected entries: %v", entries)
	}
}

// TestPutSubdir tests writing to a nested path (auto-creates parent dirs).
func TestPutSubdir(t *testing.T) {
	dir := t.TempDir()
	h := NewHandlers(Config{SharedDir: dir, InboxDir: filepath.Join(dir, "inbox")}, nil)

	body := strings.NewReader("nested content")
	req := httptest.NewRequest("PUT", "/files/deep/nested/file.txt", body)
	w := httptest.NewRecorder()
	h.HandleFiles(w, req)

	if w.Code != 200 {
		t.Fatalf("status %d", w.Code)
	}

	data, _ := os.ReadFile(filepath.Join(dir, "deep", "nested", "file.txt"))
	if string(data) != "nested content" {
		t.Fatalf("content = %q", string(data))
	}
}

// TestPathTraversal tests that ../ paths are rejected.
func TestPathTraversal(t *testing.T) {
	dir := t.TempDir()
	h := NewHandlers(Config{SharedDir: dir, InboxDir: filepath.Join(dir, "inbox")}, nil)

	req := httptest.NewRequest("GET", "/files/../../../etc/passwd", nil)
	w := httptest.NewRecorder()
	h.HandleFiles(w, req)

	if w.Code != 400 {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

// TestInbox tests receiving and listing inbox files.
func TestInbox(t *testing.T) {
	dir := t.TempDir()
	inboxDir := filepath.Join(dir, "inbox")
	h := NewHandlers(Config{SharedDir: dir, InboxDir: inboxDir}, nil)

	// PUT a file to inbox (simulating receive from another node)
	body := strings.NewReader("received file")
	req := httptest.NewRequest("PUT", "/inbox/report.pdf", body)
	w := httptest.NewRecorder()
	h.HandleInbox(w, req)

	if w.Code != 200 {
		t.Fatalf("PUT inbox status %d", w.Code)
	}

	// List inbox
	req = httptest.NewRequest("GET", "/inbox", nil)
	w = httptest.NewRecorder()
	h.HandleInbox(w, req)

	if w.Code != 200 {
		t.Fatalf("LIST inbox status %d", w.Code)
	}

	var entries []struct{ Name string }
	json.Unmarshal(w.Body.Bytes(), &entries)
	if len(entries) != 1 || entries[0].Name != "report.pdf" {
		t.Fatalf("inbox entries: %v", entries)
	}

	// GET inbox file
	req = httptest.NewRequest("GET", "/inbox/report.pdf", nil)
	w = httptest.NewRecorder()
	h.HandleInbox(w, req)

	if w.Body.String() != "received file" {
		t.Fatalf("inbox file content = %q", w.Body.String())
	}
}

// TestDeleteFile tests file deletion.
func TestDeleteFile(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "todelete.txt"), []byte("bye"), 0644)

	h := NewHandlers(Config{SharedDir: dir, InboxDir: filepath.Join(dir, "inbox")}, nil)
	req := httptest.NewRequest("DELETE", "/files/todelete.txt", nil)
	w := httptest.NewRecorder()
	h.HandleFiles(w, req)

	if w.Code != 200 {
		t.Fatalf("status %d", w.Code)
	}
	if _, err := os.Stat(filepath.Join(dir, "todelete.txt")); !os.IsNotExist(err) {
		t.Fatal("file should be deleted")
	}
}

// TestNotFound tests 404 for missing files.
func TestNotFound(t *testing.T) {
	dir := t.TempDir()
	h := NewHandlers(Config{SharedDir: dir, InboxDir: filepath.Join(dir, "inbox")}, nil)

	req := httptest.NewRequest("GET", "/files/nonexistent.txt", nil)
	w := httptest.NewRecorder()
	h.HandleFiles(w, req)

	if w.Code != 404 {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

// TestSendBetweenNodes simulates sending a file between two fsd instances.
func TestSendBetweenNodes(t *testing.T) {
	// Node B: receiver
	dirB := t.TempDir()
	inboxB := filepath.Join(dirB, "inbox")
	hB := NewHandlers(Config{SharedDir: dirB, InboxDir: inboxB, Port: 7700}, nil)
	muxB := http.NewServeMux()
	muxB.HandleFunc("/inbox/", hB.HandleInbox)
	serverB := httptest.NewServer(muxB)
	defer serverB.Close()

	// Node A: sender
	dirA := t.TempDir()
	os.WriteFile(filepath.Join(dirA, "send_me.txt"), []byte("hello from A"), 0644)

	// Simulate send: A pushes file to B's inbox
	fileData, _ := os.ReadFile(filepath.Join(dirA, "send_me.txt"))
	req, _ := http.NewRequest("PUT", serverB.URL+"/inbox/send_me.txt", bytes.NewReader(fileData))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("send status %d", resp.StatusCode)
	}

	// Verify B received the file
	data, err := os.ReadFile(filepath.Join(inboxB, "send_me.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello from A" {
		t.Fatalf("received = %q", string(data))
	}
}
