package fsd

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"tailscale.com/client/tailscale"
)

// Handlers implements the HTTP API.
type Handlers struct {
	cfg Config
	lc  *tailscale.LocalClient
}

func NewHandlers(cfg Config, lc *tailscale.LocalClient) *Handlers {
	return &Handlers{cfg: cfg, lc: lc}
}

// HandleFiles handles GET/PUT/DELETE on /files/{path}
func (h *Handlers) HandleFiles(w http.ResponseWriter, r *http.Request) {
	// /files/shared/foo.txt → subpath = shared/foo.txt
	subpath := strings.TrimPrefix(r.URL.Path, "/files/")
	subpath = filepath.Clean(subpath)

	// Prevent path traversal
	if strings.Contains(subpath, "..") {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	localPath := filepath.Join(h.cfg.SharedDir, subpath)

	switch r.Method {
	case http.MethodGet:
		h.getFile(w, r, localPath)
	case http.MethodPut:
		h.putFile(w, r, localPath)
	case http.MethodDelete:
		h.deleteFile(w, r, localPath)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *Handlers) getFile(w http.ResponseWriter, r *http.Request, path string) {
	info, err := os.Stat(path)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	if info.IsDir() {
		h.listDir(w, path)
		return
	}

	http.ServeFile(w, r, path)
}

func (h *Handlers) listDir(w http.ResponseWriter, path string) {
	entries, err := os.ReadDir(path)
	if err != nil {
		http.Error(w, "cannot list directory", http.StatusInternalServerError)
		return
	}

	type entry struct {
		Name  string `json:"name"`
		IsDir bool   `json:"isDir"`
		Size  int64  `json:"size"`
	}

	var result []entry
	for _, e := range entries {
		info, _ := e.Info()
		size := int64(0)
		if info != nil {
			size = info.Size()
		}
		result = append(result, entry{
			Name:  e.Name(),
			IsDir: e.IsDir(),
			Size:  size,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func (h *Handlers) putFile(w http.ResponseWriter, r *http.Request, path string) {
	// Ensure parent directory exists
	os.MkdirAll(filepath.Dir(path), 0755)

	f, err := os.Create(path)
	if err != nil {
		http.Error(w, fmt.Sprintf("cannot create file: %v", err), http.StatusInternalServerError)
		return
	}
	defer f.Close()

	n, err := io.Copy(f, r.Body)
	if err != nil {
		http.Error(w, fmt.Sprintf("write error: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status": "ok",
		"path":   path,
		"size":   n,
	})
}

func (h *Handlers) deleteFile(w http.ResponseWriter, r *http.Request, path string) {
	if err := os.Remove(path); err != nil {
		http.Error(w, fmt.Sprintf("cannot delete: %v", err), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "deleted"})
}

// HandleInbox handles GET /inbox and PUT /inbox/{file}
//
// PUT: receives a file from another node's /send, saves directly to
// ReceiveDir (e.g. ~/Downloads/) and records metadata in InboxDir.
// GET: lists receive history (metadata), not the files themselves.
func (h *Handlers) HandleInbox(w http.ResponseWriter, r *http.Request) {
	subpath := strings.TrimPrefix(r.URL.Path, "/inbox")
	subpath = strings.TrimPrefix(subpath, "/")

	if strings.Contains(subpath, "..") {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodGet:
		if subpath == "" {
			h.listInbox(w)
		} else {
			// Download from ReceiveDir if it exists there
			path := filepath.Join(h.cfg.ReceiveDir, filepath.Clean(subpath))
			http.ServeFile(w, r, path)
		}
	case http.MethodPut:
		if subpath == "" {
			http.Error(w, "filename required", http.StatusBadRequest)
			return
		}
		h.receiveFile(w, r, subpath)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// receiveFile saves the uploaded file directly to ReceiveDir and writes
// a metadata entry to InboxDir.
func (h *Handlers) receiveFile(w http.ResponseWriter, r *http.Request, fileName string) {
	fileName = filepath.Clean(fileName)

	// Save file to ReceiveDir (e.g. ~/Downloads/file.txt)
	os.MkdirAll(h.cfg.ReceiveDir, 0755)
	destPath := filepath.Join(h.cfg.ReceiveDir, fileName)

	// Handle name collision: append (1), (2), etc.
	if _, err := os.Stat(destPath); err == nil {
		ext := filepath.Ext(fileName)
		base := strings.TrimSuffix(fileName, ext)
		for i := 1; ; i++ {
			destPath = filepath.Join(h.cfg.ReceiveDir, fmt.Sprintf("%s(%d)%s", base, i, ext))
			if _, err := os.Stat(destPath); os.IsNotExist(err) {
				break
			}
		}
	}

	f, err := os.Create(destPath)
	if err != nil {
		http.Error(w, fmt.Sprintf("create file: %v", err), http.StatusInternalServerError)
		return
	}
	defer f.Close()

	n, err := io.Copy(f, r.Body)
	if err != nil {
		http.Error(w, fmt.Sprintf("write: %v", err), http.StatusInternalServerError)
		return
	}

	// Record metadata in InboxDir for history
	sender := r.Header.Get("X-Sender")
	if sender == "" {
		sender = r.RemoteAddr
	}
	os.MkdirAll(h.cfg.InboxDir, 0755)
	meta := map[string]interface{}{
		"file": fileName,
		"size": n,
		"from": sender,
		"time": time.Now().Format(time.RFC3339),
		"path": destPath,
	}
	metaJSON, _ := json.Marshal(meta)
	metaFile := filepath.Join(h.cfg.InboxDir, fileName+".json")
	os.WriteFile(metaFile, metaJSON, 0644)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status": "received",
		"path":   destPath,
		"size":   n,
	})
}

// listInbox reads metadata files from InboxDir and returns receive history.
func (h *Handlers) listInbox(w http.ResponseWriter) {
	entries, err := os.ReadDir(h.cfg.InboxDir)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]interface{}{})
		return
	}

	type inboxEntry struct {
		File string `json:"file"`
		Size int64  `json:"size"`
		From string `json:"from"`
		Time string `json:"time"`
		Path string `json:"path"`
	}

	var result []inboxEntry
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(h.cfg.InboxDir, e.Name()))
		if err != nil {
			continue
		}
		var entry inboxEntry
		if json.Unmarshal(data, &entry) == nil {
			result = append(result, entry)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// HandleSend handles POST /send
func (h *Handlers) HandleSend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}

	var req SendRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid request: %v", err), http.StatusBadRequest)
		return
	}

	results, err := Send(r.Context(), h.lc, h.cfg, req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(results)
}
