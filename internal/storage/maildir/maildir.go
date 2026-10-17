// Package maildir implements RFC-compliant Maildir directory operations for UndineD.
package maildir

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"undined-go/internal/constants"
)

var (
	seqCounter uint64
	hostName   string
	hostOnce   sync.Once
)

func getHostname() string {
	hostOnce.Do(func() {
		h, err := os.Hostname()
		if err != nil || h == "" {
			h = "localhost"
		}
		// Clean hostname
		h = strings.ReplaceAll(h, "/", "_")
		h = strings.ReplaceAll(h, ":", "_")
		hostName = h
	})
	return hostName
}

// NormalizeFlags deduplicates and sorts standard Maildir flags.
func NormalizeFlags(flags string) string {
	seen := make(map[rune]struct{})
	var unique []rune
	for _, r := range flags {
		if _, ok := seen[r]; !ok {
			seen[r] = struct{}{}
			unique = append(unique, r)
		}
	}
	sort.Slice(unique, func(i, j int) bool {
		return unique[i] < unique[j]
	})
	return string(unique)
}

// MaildirStore manages local Maildir directory trees.
type MaildirStore struct {
	BasePath string
	mu       sync.RWMutex
}

// NewMaildirStore creates a new store at the given base directory.
func NewMaildirStore(basePath string) *MaildirStore {
	return &MaildirStore{BasePath: basePath}
}

// EnsureFolder creates cur, new, and tmp subdirectories for a folder.
func (m *MaildirStore) EnsureFolder(folder string) error {
	for _, sub := range []string{"cur", "new", "tmp"} {
		p := filepath.Join(m.BasePath, folder, sub)
		if err := os.MkdirAll(p, 0700); err != nil {
			return fmt.Errorf("failed to create maildir directory %s: %w", p, err)
		}
	}
	return nil
}

// AddMessage atomically writes a new email into folder with specified flags.
// Returns the absolute file path of the newly created message.
func (m *MaildirStore) AddMessage(folder string, content []byte, flags string) (string, error) {
	if err := m.EnsureFolder(folder); err != nil {
		return "", err
	}

	seq := atomic.AddUint64(&seqCounter, 1)
	now := time.Now()
	pid := os.Getpid()
	host := getHostname()

	tmpName := fmt.Sprintf("%d.M%06dP%dQ%d.%s,S=%d",
		now.Unix(), now.Nanosecond()/1000, pid, seq, host, len(content))
	tmpPath := filepath.Join(m.BasePath, folder, "tmp", tmpName)

	if err := os.WriteFile(tmpPath, content, 0600); err != nil {
		return "", fmt.Errorf("failed to write message to tmp file %s: %w", tmpPath, err)
	}

	normFlags := NormalizeFlags(flags)
	var finalSubdir, finalName string
	if normFlags != "" {
		finalSubdir = "cur"
		finalName = fmt.Sprintf("%s:2,%s", tmpName, normFlags)
	} else {
		finalSubdir = "new"
		finalName = tmpName
	}

	finalPath := filepath.Join(m.BasePath, folder, finalSubdir, finalName)
	if err := os.Rename(tmpPath, finalPath); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("failed to move message from tmp to final path %s: %w", finalPath, err)
	}

	return finalPath, nil
}

// ReadMessage reads the raw RFC822 content from the given file path.
func (m *MaildirStore) ReadMessage(filePath string) ([]byte, error) {
	return os.ReadFile(filePath)
}

// UpdateFlags modifies the flags on an existing maildir file via atomic rename.
// Returns the new absolute file path.
func (m *MaildirStore) UpdateFlags(filePath string, flags string) (string, error) {
	dir := filepath.Dir(filePath)
	base := filepath.Base(filePath)

	// Strip existing :2,... suffix if present
	cleanBase := base
	if idx := strings.Index(base, ":2,"); idx != -1 {
		cleanBase = base[:idx]
	}

	normFlags := NormalizeFlags(flags)
	folderDir := filepath.Dir(dir)

	var targetSubdir, newBase string
	if normFlags != "" {
		targetSubdir = "cur"
		newBase = fmt.Sprintf("%s:2,%s", cleanBase, normFlags)
	} else {
		targetSubdir = "new"
		newBase = cleanBase
	}

	newPath := filepath.Join(folderDir, targetSubdir, newBase)
	if filePath == newPath {
		return filePath, nil
	}

	if err := os.Rename(filePath, newPath); err != nil {
		return "", fmt.Errorf("failed to update flags for %s to %s: %w", filePath, newPath, err)
	}
	return newPath, nil
}

// AddFlag adds a flag (e.g. FlagSeen) to the message.
func (m *MaildirStore) AddFlag(filePath string, flag string) (string, error) {
	currentFlags := ExtractFlags(filePath)
	if strings.Contains(currentFlags, flag) {
		return filePath, nil
	}
	return m.UpdateFlags(filePath, currentFlags+flag)
}

// RemoveFlag removes a flag from the message.
func (m *MaildirStore) RemoveFlag(filePath string, flag string) (string, error) {
	currentFlags := ExtractFlags(filePath)
	if !strings.Contains(currentFlags, flag) {
		return filePath, nil
	}
	newFlags := strings.ReplaceAll(currentFlags, flag, "")
	return m.UpdateFlags(filePath, newFlags)
}

// MoveMessage moves a message to a new folder, optionally overriding flags.
func (m *MaildirStore) MoveMessage(filePath, destFolder string, flags ...string) (string, error) {
	if err := m.EnsureFolder(destFolder); err != nil {
		return "", err
	}

	base := filepath.Base(filePath)
	cleanBase := base
	currentFlags := ""
	if idx := strings.Index(base, ":2,"); idx != -1 {
		cleanBase = base[:idx]
		currentFlags = base[idx+3:]
	}

	targetFlags := currentFlags
	if len(flags) > 0 {
		targetFlags = flags[0]
	}
	normFlags := NormalizeFlags(targetFlags)

	var targetSubdir, newBase string
	if normFlags != "" {
		targetSubdir = "cur"
		newBase = fmt.Sprintf("%s:2,%s", cleanBase, normFlags)
	} else {
		targetSubdir = "new"
		newBase = cleanBase
	}

	newPath := filepath.Join(m.BasePath, destFolder, targetSubdir, newBase)
	if filePath == newPath {
		return filePath, nil
	}

	if err := os.Rename(filePath, newPath); err != nil {
		// Fallback for cross-device moves: copy then delete
		data, readErr := os.ReadFile(filePath)
		if readErr != nil {
			return "", fmt.Errorf("failed to read source for move %s: %w", filePath, readErr)
		}
		if writeErr := os.WriteFile(newPath, data, 0600); writeErr != nil {
			return "", fmt.Errorf("failed to write dest for move %s: %w", newPath, writeErr)
		}
		_ = os.Remove(filePath)
	}

	return newPath, nil
}

// DeleteMessage deletes the message file from storage.
func (m *MaildirStore) DeleteMessage(filePath string) error {
	if err := os.Remove(filePath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to delete message %s: %w", filePath, err)
	}
	return nil
}

// ExtractFlags extracts the Maildir flag string from a filename.
func ExtractFlags(filePath string) string {
	base := filepath.Base(filePath)
	if idx := strings.Index(base, ":2,"); idx != -1 {
		return base[idx+3:]
	}
	return ""
}

// HasFlag checks if the message has the given Maildir flag.
func HasFlag(filePath, flag string) bool {
	return strings.Contains(ExtractFlags(filePath), flag)
}

// IsRead checks if the message has the FlagSeen ('S') flag.
func IsRead(filePath string) bool {
	return HasFlag(filePath, constants.FlagSeen)
}

// IsReplied checks if the message has the FlagReplied ('R') flag.
func IsReplied(filePath string) bool {
	return HasFlag(filePath, constants.FlagReplied)
}
