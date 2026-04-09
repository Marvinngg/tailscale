package fsd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

// ACL defines file service access control.
type ACL struct {
	// Roles maps Headscale login name → role name.
	// e.g. "admin-marvin" → "admin"
	Roles map[string]string `json:"roles"`

	// Library maps directory path → list of roles that can access it.
	// e.g. "public/" → ["admin","developer","compute","member"]
	Library map[string][]string `json:"library"`

	// Broadcast lists roles that can send to all/groups.
	Broadcast []string `json:"broadcast"`
}

// Permission levels
const (
	PermNone      = ""
	PermRead      = "read"
	PermWrite     = "write"
	PermAdmin     = "admin"
	PermBroadcast = "broadcast"
)

var (
	aclOnce     sync.Once
	cachedACL   *ACL
	aclFilePath string
)

// aclConfigPath returns the path to fs-acl.json.
func aclConfigPath() string {
	if runtime.GOOS == "windows" {
		return filepath.Join(os.Getenv("ProgramData"), "Tailscale", "fs-acl.json")
	}
	// Try /Library/Tailscale first (macOS), then /etc/tailscale (Linux)
	for _, p := range []string{
		"/Library/Tailscale/fs-acl.json",
		"/etc/tailscale/fs-acl.json",
	} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return "/Library/Tailscale/fs-acl.json"
}

// LoadACL loads the ACL config. Returns nil if no config file exists
// (meaning no permission enforcement — open access).
func LoadACL() *ACL {
	path := aclConfigPath()
	data, err := os.ReadFile(path)
	if err != nil {
		return nil // no ACL file = open access
	}
	var acl ACL
	if err := json.Unmarshal(data, &acl); err != nil {
		return nil
	}
	aclFilePath = path
	return &acl
}

// GetACL returns the cached ACL, loading once.
func GetACL() *ACL {
	aclOnce.Do(func() {
		cachedACL = LoadACL()
	})
	return cachedACL
}

// ReloadACL forces a reload of the ACL config.
func ReloadACL() *ACL {
	aclOnce = sync.Once{}
	return GetACL()
}

// IsServerMode returns true if an ACL config file exists,
// meaning this node acts as a file library server.
func IsServerMode() bool {
	return GetACL() != nil
}

// RoleFor returns the role for a given Headscale login name.
func (a *ACL) RoleFor(loginName string) string {
	if a == nil {
		return PermAdmin // no ACL = full access
	}
	if role, ok := a.Roles[loginName]; ok {
		return role
	}
	// Try prefix match (e.g. "admin-marvin" matches "admin-")
	for pattern, role := range a.Roles {
		if strings.HasSuffix(pattern, "*") {
			prefix := strings.TrimSuffix(pattern, "*")
			if strings.HasPrefix(loginName, prefix) {
				return role
			}
		}
	}
	return "guest"
}

// CanAccessLibrary checks if a role can access a library path.
func (a *ACL) CanAccessLibrary(role, path string) bool {
	if a == nil || role == "admin" {
		return true
	}
	// Check each library path rule
	for dirPattern, allowedRoles := range a.Library {
		if strings.HasPrefix(path, dirPattern) || dirPattern == "*" {
			for _, r := range allowedRoles {
				if r == role || r == "*" {
					return true
				}
			}
			return false
		}
	}
	// No matching rule = deny for non-admin
	return false
}

// CanBroadcast checks if a role can send broadcast/group messages.
func (a *ACL) CanBroadcast(role string) bool {
	if a == nil || role == "admin" {
		return true
	}
	for _, r := range a.Broadcast {
		if r == role || r == "*" {
			return true
		}
	}
	return false
}

// CanUploadLibrary checks if a role can upload to the library.
func (a *ACL) CanUploadLibrary(role string) bool {
	if a == nil {
		return true
	}
	return role == "admin" || role == "developer" || role == "server"
}

// CanDeleteLibrary checks if a role can delete from the library.
func (a *ACL) CanDeleteLibrary(role string) bool {
	if a == nil {
		return true
	}
	return role == "admin" || role == "server"
}
