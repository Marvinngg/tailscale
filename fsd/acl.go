package fsd

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

// ACL defines file service access control policy.
//
// Roles are derived from Headscale Node Tags (tag:fs-admin, tag:fs-agent).
// This file only defines WHAT each role can do, not WHO has which role.
//
// Three roles (hierarchical):
//   admin  — system administrator, full access (tag:fs-admin)
//   agent  — middle management, most access (tag:fs-agent)
//   user   — regular user, minimal access (no fs tag)
type ACL struct {
	// Library maps directory → list of roles that can see/download it.
	Library map[string][]string `json:"library"`

	// Upload lists roles that can upload to the library.
	Upload []string `json:"upload"`

	// Delete lists roles that can delete from the library.
	Delete []string `json:"delete"`

	// Broadcast lists roles that can broadcast to all.
	Broadcast []string `json:"broadcast"`

	// BroadcastGroup lists roles that can broadcast to a specific group.
	BroadcastGroup []string `json:"broadcast_group"`
}

var (
	aclMu      sync.RWMutex
	cachedACL  *ACL
	aclPath    string
	aclModTime time.Time
)

// aclConfigPaths returns possible paths to fs-acl.json.
func aclConfigPaths() []string {
	if runtime.GOOS == "windows" {
		return []string{
			filepath.Join(os.Getenv("ProgramData"), "Tailscale", "fs-acl.json"),
		}
	}
	return []string{
		"/etc/tailscale/fs-acl.json",
		"/Library/Tailscale/fs-acl.json",
	}
}

func loadACL() (*ACL, string) {
	for _, p := range aclConfigPaths() {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var acl ACL
		if err := json.Unmarshal(data, &acl); err != nil {
			log.Printf("fsd: ACL parse error in %s: %v", p, err)
			continue
		}
		return &acl, p
	}
	return nil, ""
}

// GetACL returns the current ACL policy. Auto-reloads on file change.
// Returns nil if no ACL file exists (open access, everyone is admin).
func GetACL() *ACL {
	aclMu.RLock()
	if aclPath != "" {
		if info, err := os.Stat(aclPath); err == nil && info.ModTime().Equal(aclModTime) {
			defer aclMu.RUnlock()
			return cachedACL
		}
	}
	aclMu.RUnlock()

	aclMu.Lock()
	defer aclMu.Unlock()

	acl, path := loadACL()
	if acl != nil {
		if path != aclPath {
			log.Printf("fsd: ACL loaded from %s", path)
		} else {
			log.Printf("fsd: ACL reloaded from %s", path)
		}
		cachedACL = acl
		aclPath = path
		if info, err := os.Stat(path); err == nil {
			aclModTime = info.ModTime()
		}
	} else {
		cachedACL = nil
		aclPath = ""
	}
	return cachedACL
}

// RoleFromTags derives the fsd role from Headscale node tags.
//
//   tag:fs-admin → "admin"
//   tag:fs-agent → "agent"
//   (no fs tag)  → "user"
//
// This is the single source of truth for role assignment.
// Tags are set when creating preauthkeys or via headscale nodes tag.
func RoleFromTags(tags []string) string {
	for _, t := range tags {
		if t == "tag:fs-admin" {
			return "admin"
		}
	}
	for _, t := range tags {
		if t == "tag:fs-agent" {
			return "agent"
		}
	}
	return "user"
}

// CanAccessLibrary checks if a role can browse/download a library path.
func CanAccessLibrary(acl *ACL, role, path string) bool {
	if acl == nil || role == "admin" {
		return true
	}

	// Find the best matching directory rule
	bestMatch := ""
	for dirPattern := range acl.Library {
		dirClean := dirPattern
		if dirClean != "" && dirClean[len(dirClean)-1] == '/' {
			dirClean = dirClean[:len(dirClean)-1]
		}
		if path == dirClean || hasPrefix(path, dirPattern) {
			if len(dirPattern) > len(bestMatch) {
				bestMatch = dirPattern
			}
		}
	}

	if bestMatch == "" {
		return role == "admin"
	}

	for _, r := range acl.Library[bestMatch] {
		if r == role {
			return true
		}
	}
	return false
}

func hasPrefix(path, prefix string) bool {
	if prefix == "" {
		return true
	}
	if prefix[len(prefix)-1] != '/' {
		prefix += "/"
	}
	return len(path) >= len(prefix) && path[:len(prefix)] == prefix
}

// CanUpload checks if a role can upload to the library.
func CanUpload(acl *ACL, role string) bool {
	if acl == nil || role == "admin" {
		return true
	}
	for _, r := range acl.Upload {
		if r == role {
			return true
		}
	}
	return false
}

// CanDelete checks if a role can delete from the library.
func CanDelete(acl *ACL, role string) bool {
	if acl == nil || role == "admin" {
		return true
	}
	for _, r := range acl.Delete {
		if r == role {
			return true
		}
	}
	return false
}

// CanBroadcastAll checks if a role can broadcast to everyone.
func CanBroadcastAll(acl *ACL, role string) bool {
	if acl == nil || role == "admin" {
		return true
	}
	for _, r := range acl.Broadcast {
		if r == role {
			return true
		}
	}
	return false
}

// CanBroadcastGroup checks if a role can broadcast to a specific group.
func CanBroadcastGroup(acl *ACL, role string) bool {
	if acl == nil || role == "admin" {
		return true
	}
	for _, r := range acl.BroadcastGroup {
		if r == role {
			return true
		}
	}
	return false
}
