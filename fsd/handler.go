package fsd

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

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

// HandleInbox handles GET /inbox and GET /inbox/{file}
func (h *Handlers) HandleInbox(w http.ResponseWriter, r *http.Request) {
	subpath := strings.TrimPrefix(r.URL.Path, "/inbox")
	subpath = strings.TrimPrefix(subpath, "/")

	if subpath == "" {
		// List inbox
		h.listDir(w, h.cfg.InboxDir)
		return
	}

	path := filepath.Join(h.cfg.InboxDir, filepath.Clean(subpath))
	if strings.Contains(subpath, "..") {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodGet:
		http.ServeFile(w, r, path)
	case http.MethodPut:
		// Receiving a file from another node's /send
		os.MkdirAll(h.cfg.InboxDir, 0755)
		f, err := os.Create(path)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer f.Close()
		io.Copy(f, r.Body)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "received"})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
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
