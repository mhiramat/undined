// Package patchwork interacts with the Patchwork REST API v1.4.
package patchwork

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"undined-go/internal/config"
	"undined-go/internal/constants"
	"undined-go/internal/storage/database"
	"undined-go/internal/storage/maildir"
	"undined-go/internal/storage/parser"
)

// Client coordinates Patchwork v1.4 REST API operations.
type Client struct {
	Config  *config.Config
	DB      *database.Database
	Maildir *maildir.MaildirStore

	HTTPClient *http.Client
	mu         sync.RWMutex
	cache      map[string]map[string]any
	userCache  map[string]int
}

// NewClient creates a new Patchwork Client.
func NewClient(cfg *config.Config, db *database.Database, store *maildir.MaildirStore) *Client {
	timeout := 15 * time.Second
	if cfg.Patchwork.TimeoutSeconds > 0 {
		timeout = time.Duration(cfg.Patchwork.TimeoutSeconds * float64(time.Second))
	}

	return &Client{
		Config:  cfg,
		DB:      db,
		Maildir: store,
		HTTPClient: &http.Client{
			Timeout: timeout,
		},
		cache:     make(map[string]map[string]any),
		userCache: make(map[string]int),
	}
}

// GetBaseURL constructs the base API URL (e.g. https://patchwork.kernel.org/api/1.4).
func (c *Client) GetBaseURL() string {
	rawURL := strings.TrimRight(c.Config.Patchwork.URL, "/")
	if rawURL == "" {
		rawURL = constants.DefaultPatchworkURL
	}
	ver := strings.Trim(c.Config.Patchwork.APIVersion, "/")
	if ver == "" {
		ver = constants.DefaultPatchworkAPIVersion
	}
	return fmt.Sprintf("%s/api/%s", rawURL, ver)
}

func (c *Client) newRequest(method, endpoint string, body io.Reader) (*http.Request, error) {
	reqURL := endpoint
	if !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
		reqURL = fmt.Sprintf("%s/%s", c.GetBaseURL(), strings.TrimLeft(endpoint, "/"))
	}

	req, err := http.NewRequest(method, reqURL, body)
	if err != nil {
		return nil, err
	}

	req.Header.Set("User-Agent", "undine-mua/0.1.0 (Linux Kernel Plaintext MUA)")
	req.Header.Set("Accept", "application/json")

	token := c.Config.Patchwork.GetToken()
	if token != "" {
		req.Header.Set("Authorization", fmt.Sprintf("Token %s", token))
	}

	return req, nil
}

func matchesArchive(project any, archiveName string) bool {
	if project == nil || archiveName == "" {
		return false
	}
	target := strings.ToLower(strings.TrimSpace(archiveName))
	switch p := project.(type) {
	case string:
		return strings.ToLower(strings.TrimSpace(p)) == target
	case map[string]any:
		if ln, ok := p["link_name"].(string); ok && strings.ToLower(strings.TrimSpace(ln)) == target {
			return true
		}
		if le, ok := p["list_email"].(string); ok {
			cleanLE := strings.ToLower(strings.TrimSpace(le))
			if cleanLE == target {
				return true
			}
			if idx := strings.Index(cleanLE, "@"); idx > 0 && cleanLE[:idx] == target {
				return true
			}
		}
		if name, ok := p["name"].(string); ok && strings.ToLower(strings.TrimSpace(name)) == target {
			return true
		}
		if id, ok := p["id"]; ok && fmt.Sprintf("%v", id) == target {
			return true
		}
	}
	return false
}

// ChoosePreferredItem selects the most preferred patch among duplicates according to project priority and delegation.
func (c *Client) ChoosePreferredItem(items []map[string]any, fallbackEmail string) map[string]any {
	if len(items) == 0 {
		return nil
	}
	if len(items) == 1 {
		return items[0]
	}

	preferred := c.Config.Patchwork.GetPreferredArchives()

	bestIdx := 0
	bestPrefRank := len(preferred)
	bestIsMe := 1
	bestHasDel := 1

	for idx, item := range items {
		prefRank := len(preferred)
		project := item["project"]
		for pI, pName := range preferred {
			if matchesArchive(project, pName) {
				prefRank = pI
				break
			}
		}

		isMe := 1
		if c.Config.Patchwork.IsMe(item["delegate"], fallbackEmail) {
			isMe = 0
		}

		hasDel := 1
		if item["delegate"] != nil {
			hasDel = 0
		}

		// Compare tuple: (prefRank, isMe, hasDel, idx)
		isBetter := false
		if prefRank < bestPrefRank {
			isBetter = true
		} else if prefRank == bestPrefRank {
			if isMe < bestIsMe {
				isBetter = true
			} else if isMe == bestIsMe {
				if hasDel < bestHasDel {
					isBetter = true
				}
			}
		}

		if isBetter || idx == 0 {
			bestIdx = idx
			bestPrefRank = prefRank
			bestIsMe = isMe
			bestHasDel = hasDel
		}
	}

	return items[bestIdx]
}

// ClearCache removes cached patchwork info for a message ID (or clears all if empty).
func (c *Client) ClearCache(msgID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if msgID == "" {
		c.cache = make(map[string]map[string]any)
		return
	}
	cleaned := parser.CleanMsgID(msgID)
	if cleaned == "" {
		cleaned = msgID
	}
	delete(c.cache, cleaned)
}

// GetPatchInfo queries Patchwork by message-id and optional project.
func (c *Client) GetPatchInfo(msgID string, project string) (map[string]any, error) {
	cleaned := parser.CleanMsgID(msgID)
	if cleaned == "" {
		cleaned = msgID
	}

	c.mu.RLock()
	if cached, ok := c.cache[cleaned]; ok && project == "" {
		c.mu.RUnlock()
		return cached, nil
	}
	c.mu.RUnlock()

	params := url.Values{}
	params.Set("msgid", cleaned)
	if project != "" {
		params.Set("project", project)
	}

	endpoint := fmt.Sprintf("/patches/?%s", params.Encode())
	req, err := c.newRequest("GET", endpoint, nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("patchwork request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("patchwork returned status %d", resp.StatusCode)
	}

	var items []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		return nil, fmt.Errorf("failed to decode patchwork response: %w", err)
	}

	if len(items) == 0 {
		// Fallback: try with angle brackets if cleaned didn't have them
		bracketed := fmt.Sprintf("<%s>", cleaned)
		params.Set("msgid", bracketed)
		req2, _ := c.newRequest("GET", fmt.Sprintf("/patches/?%s", params.Encode()), nil)
		resp2, err2 := c.HTTPClient.Do(req2)
		if err2 == nil {
			defer resp2.Body.Close()
			if resp2.StatusCode == http.StatusOK {
				_ = json.NewDecoder(resp2.Body).Decode(&items)
			}
		}
	}

	if len(items) == 0 {
		// Try cover-letters fallback in case message is a series cover letter
		bracketed := fmt.Sprintf("<%s>", cleaned)
		paramsCover := url.Values{}
		paramsCover.Set("msgid", bracketed)
		if project != "" {
			paramsCover.Set("project", project)
		}
		reqCover, _ := c.newRequest("GET", fmt.Sprintf("/cover-letters/?%s", paramsCover.Encode()), nil)
		if reqCover != nil {
			respCover, errCover := c.HTTPClient.Do(reqCover)
			if errCover == nil {
				defer respCover.Body.Close()
				if respCover.StatusCode == http.StatusOK {
					var coverItems []map[string]any
					if err := json.NewDecoder(respCover.Body).Decode(&coverItems); err == nil && len(coverItems) > 0 {
						for _, it := range coverItems {
							it["type"] = "cover"
						}
						items = coverItems
					}
				}
			}
		}
	}

	if len(items) == 0 {
		return nil, nil
	}

	chosen := c.ChoosePreferredItem(items, c.Config.SMTP.FromEmail)
	if chosen != nil {
		chosen["found"] = true
		chosen["enabled"] = true
		if chosen["type"] == nil {
			chosen["type"] = "patch"
		}
		chosen["is_delegated_to_me"] = c.Config.Patchwork.IsMe(chosen["delegate"], c.Config.SMTP.FromEmail)
		chosen["delegate_users"] = c.Config.Patchwork.GetDelegateUsers()
		chosen["can_update"] = c.Config.Patchwork.GetToken() != ""
		if chosen["check_summary"] == nil && chosen["check"] != nil {
			chosen["check_summary"] = chosen["check"]
		}

		// Handle series array: if series is an array of objects, extract the first series
		if sArr, ok := chosen["series"].([]any); ok && len(sArr) > 0 {
			if sMap, ok := sArr[0].(map[string]any); ok {
				chosen["series"] = sMap
				chosen["series_list"] = sArr
			}
		}

		if len(items) > 1 {
			var otherProjects []map[string]any
			for _, it := range items {
				if fmt.Sprintf("%v", it["id"]) != fmt.Sprintf("%v", chosen["id"]) {
					if proj, ok := it["project"].(map[string]any); ok {
						otherProjects = append(otherProjects, proj)
					}
				}
			}
			if len(otherProjects) > 0 {
				chosen["other_projects"] = otherProjects
			}
		}
	}

	c.mu.Lock()
	c.cache[cleaned] = chosen
	c.mu.Unlock()

	return chosen, nil
}

// GetPatch fetches a single patch by numeric Patchwork ID.
func (c *Client) GetPatch(patchID int) (map[string]any, error) {
	endpoint := fmt.Sprintf("/patches/%d/", patchID)
	req, err := c.newRequest("GET", endpoint, nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("patchwork returned status %d", resp.StatusCode)
	}

	var item map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&item); err != nil {
		return nil, err
	}
	return item, nil
}

// UpdatePatch performs a partial update (PATCH) on a patch.
// Per Rule 4: Preserves explicit null values (e.g. updates["delegate"] = nil) vs omitted fields.
func (c *Client) UpdatePatch(patchID int, updates map[string]any) (map[string]any, error) {
	token := c.Config.Patchwork.GetToken()
	if token == "" {
		return nil, fmt.Errorf("patchwork token is required to update patches")
	}

	payload := make(map[string]any)

	for k, v := range updates {
		switch k {
		case "state":
			if v != nil {
				payload["state"] = v
			}
		case "delegate":
			if v == nil || v == "" || v == "none" {
				payload["delegate"] = nil // Explicit JSON null unassigns the delegate
			} else if idNum, ok := v.(int); ok {
				payload["delegate"] = idNum
			} else if idFloat, ok := v.(float64); ok {
				payload["delegate"] = int(idFloat)
			} else if delStr, ok := v.(string); ok {
				delStr = strings.TrimSpace(delStr)
				if num, err := strconv.Atoi(delStr); err == nil {
					payload["delegate"] = num
				} else {
					uid, err := c.FindUserID(delStr)
					if err != nil || uid == nil {
						return nil, fmt.Errorf("could not find Patchwork user ID for '%s': %v", delStr, err)
					}
					payload["delegate"] = *uid
				}
			} else {
				payload["delegate"] = v
			}
		case "archived":
			payload["archived"] = v
		case "commit_ref":
			payload["commit_ref"] = v
		}
	}

	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal patch payload: %w", err)
	}

	endpoint := fmt.Sprintf("/patches/%d/", patchID)
	req, err := c.newRequest("PATCH", endpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("patchwork PATCH failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("patchwork update failed (%d): %s", resp.StatusCode, string(respBody))
	}

	var updated map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&updated); err != nil {
		return nil, err
	}

	// Invalidate cache
	if msgid, ok := updated["msgid"].(string); ok {
		clean := parser.CleanMsgID(msgid)
		c.mu.Lock()
		delete(c.cache, clean)
		c.mu.Unlock()
	}

	return updated, nil
}

// DelegateToMe assigns the patch to the current configured user.
func (c *Client) DelegateToMe(patchID int, fallbackEmail string) (map[string]any, error) {
	myUser := c.Config.Patchwork.GetMyUsername(fallbackEmail)
	myEmail := c.Config.Patchwork.GetMyEmail(fallbackEmail)

	var uid *int
	var err error
	if myUser != "" {
		uid, _ = c.FindUserID(myUser)
	}
	if uid == nil && myEmail != "" {
		uid, err = c.FindUserID(myEmail)
	}

	if uid == nil {
		return nil, fmt.Errorf("could not find Patchwork user ID for user '%s' / '%s': %v", myUser, myEmail, err)
	}

	return c.UpdatePatch(patchID, map[string]any{"delegate": *uid})
}

// FindUserID queries Patchwork users to resolve a username or email to numeric user ID.
func (c *Client) FindUserID(usernameOrEmail string) (*int, error) {
	target := strings.TrimSpace(usernameOrEmail)
	if target == "" {
		return nil, nil
	}

	c.mu.RLock()
	if id, ok := c.userCache[strings.ToLower(target)]; ok {
		c.mu.RUnlock()
		return &id, nil
	}
	c.mu.RUnlock()

	params := url.Values{}
	params.Set("q", target)
	endpoint := fmt.Sprintf("/users/?%s", params.Encode())

	req, err := c.newRequest("GET", endpoint, nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("user query returned status %d", resp.StatusCode)
	}

	var users []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&users); err != nil {
		return nil, err
	}

	targetLower := strings.ToLower(target)
	for _, u := range users {
		idVal, ok := u["id"]
		if !ok {
			continue
		}
		var idInt int
		switch idv := idVal.(type) {
		case float64:
			idInt = int(idv)
		case int:
			idInt = idv
		}

		uname, _ := u["username"].(string)
		uemail, _ := u["email"].(string)

		if strings.ToLower(uname) == targetLower || strings.ToLower(uemail) == targetLower {
			c.mu.Lock()
			c.userCache[targetLower] = idInt
			c.mu.Unlock()
			return &idInt, nil
		}
	}

	if len(users) == 1 {
		if idVal, ok := users[0]["id"].(float64); ok {
			idInt := int(idVal)
			c.mu.Lock()
			c.userCache[targetLower] = idInt
			c.mu.Unlock()
			return &idInt, nil
		}
	}

	return nil, fmt.Errorf("user '%s' not found on Patchwork", target)
}

// ListMyPatches lists patches delegated to the current user, optionally filtered by state and project.
func (c *Client) ListMyPatches(states []string, project string, limit int, fallbackEmail string) ([]map[string]any, error) {
	myUser := c.Config.Patchwork.GetMyUsername(fallbackEmail)
	myEmail := c.Config.Patchwork.GetMyEmail(fallbackEmail)

	var uid *int
	if myUser != "" {
		uid, _ = c.FindUserID(myUser)
	}
	if uid == nil && myEmail != "" {
		uid, _ = c.FindUserID(myEmail)
	}

	if uid == nil {
		return nil, fmt.Errorf("could not find Patchwork user ID for user '%s'", myUser)
	}

	if len(states) == 0 {
		states = []string{""}
	}

	seenIDs := make(map[int]bool)
	var allPatches []map[string]any

	for _, st := range states {
		params := url.Values{}
		params.Set("delegate", strconv.Itoa(*uid))
		if st != "" {
			params.Set("state", st)
		}
		if project != "" {
			params.Set("project", project)
		}

		endpoint := fmt.Sprintf("/patches/?%s", params.Encode())
		req, err := c.newRequest("GET", endpoint, nil)
		if err != nil {
			continue
		}

		resp, err := c.HTTPClient.Do(req)
		if err != nil {
			continue
		}

		var items []map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&items)
		resp.Body.Close()

		for _, item := range items {
			idVal, ok := item["id"]
			if !ok {
				continue
			}
			var pID int
			if f, ok := idVal.(float64); ok {
				pID = int(f)
			} else if i, ok := idVal.(int); ok {
				pID = i
			}
			if !seenIDs[pID] {
				seenIDs[pID] = true
				allPatches = append(allPatches, item)
			}
		}

		if limit > 0 && len(allPatches) >= limit {
			allPatches = allPatches[:limit]
			break
		}
	}

	return allPatches, nil
}

// GetSeries fetches series details by ID.
func (c *Client) GetSeries(seriesID int) (map[string]any, error) {
	endpoint := fmt.Sprintf("/series/%d/", seriesID)
	req, err := c.newRequest("GET", endpoint, nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("series request failed with status %d", resp.StatusCode)
	}

	var res map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil, err
	}
	return res, nil
}

// GetChecks fetches automated test checks for a patch.
func (c *Client) GetChecks(patchID int) ([]map[string]any, error) {
	endpoint := fmt.Sprintf("/patches/%d/checks/", patchID)
	req, err := c.newRequest("GET", endpoint, nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("checks request failed with status %d", resp.StatusCode)
	}

	var checks []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&checks); err != nil {
		return nil, err
	}
	return checks, nil
}

// DownloadPatchMbox downloads raw patch mbox from Patchwork.
func (c *Client) DownloadPatchMbox(patchID int) ([]byte, error) {
	endpoint := fmt.Sprintf("/patches/%d/mbox/", patchID)
	req, err := c.newRequest("GET", endpoint, nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("patch mbox download failed with status %d", resp.StatusCode)
	}

	return io.ReadAll(resp.Body)
}

// SyncTodoResult holds summary counts and patches updated by SyncTodoFromPatchwork.
type SyncTodoResult struct {
	SyncedCount  int              `json:"synced_count"`
	RemovedCount int              `json:"removed_count"`
	Patches      []map[string]any `json:"patches"`
}

// SyncTodoFromPatchwork synchronizes delegated patches from Patchwork into the TODO mailbox.
func (c *Client) SyncTodoFromPatchwork(states []string) (*SyncTodoResult, error) {
	if c.DB == nil || c.Maildir == nil {
		return nil, fmt.Errorf("database and maildir stores required for TODO sync")
	}

	todoFolder, err := c.DB.GetOrCreateFolder(constants.FolderTODO, false)
	if err != nil {
		return nil, err
	}
	_ = c.Maildir.EnsureFolder(constants.FolderTODO)

	if len(states) == 0 {
		states = c.Config.Patchwork.GetTodoStates()
	}

	patches, err := c.ListMyPatches(states, "", 0, c.Config.SMTP.FromEmail)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch delegated patches: %w", err)
	}

	validMsgIDs := make(map[string]bool)
	for _, p := range patches {
		if mid, ok := p["msgid"].(string); ok {
			clean := parser.CleanMsgID(mid)
			if clean != "" {
				validMsgIDs[clean] = true
			}
		}
	}

	// 1. Purge removed/completed patches from TODO folder
	existingTodoMsgs, _ := c.DB.ListMessages(&todoFolder.ID, 1000, 0)
	removedCount := 0
	for _, m := range existingTodoMsgs {
		clean := parser.CleanMsgID(m.MessageID)
		if !validMsgIDs[clean] {
			_ = c.DB.DeleteMessage(m.ID)
			if m.FilePath != "" {
				_ = c.Maildir.DeleteMessage(m.FilePath)
			}
			removedCount++
		}
	}

	// 2. Download and index new delegated patches
	syncedCount := 0
	var syncedPatches []map[string]any

	for _, p := range patches {
		mid, _ := p["msgid"].(string)
		cleanID := parser.CleanMsgID(mid)
		if cleanID == "" {
			continue
		}

		// Check if already in TODO folder
		existing, _ := c.DB.GetMessageByFolderAndMsgID(todoFolder.ID, cleanID)
		if existing != nil {
			continue
		}

		var pID int
		if f, ok := p["id"].(float64); ok {
			pID = int(f)
		} else if i, ok := p["id"].(int); ok {
			pID = i
		}

		rawBytes, err := c.DownloadPatchMbox(pID)
		if err != nil || len(rawBytes) == 0 {
			// Fallback: check if we already have the email file locally in another folder
			localMsg, _ := c.DB.GetMessageByMsgID(cleanID)
			if localMsg != nil && localMsg.FilePath != "" {
				rawBytes, _ = os.ReadFile(localMsg.FilePath)
			}
		}

		if len(rawBytes) == 0 {
			continue
		}

		savedPath, err := c.Maildir.AddMessage(constants.FolderTODO, rawBytes, constants.FlagSeen)
		if err != nil {
			continue
		}

		dbID, err := c.DB.IndexEmailFile(todoFolder.ID, savedPath)
		if err == nil {
			syncedCount++
			name, _ := p["name"].(string)
			syncedPatches = append(syncedPatches, map[string]any{
				"id":    pID,
				"name":  name,
				"msgid": cleanID,
				"db_id": dbID,
			})
		}
	}

	return &SyncTodoResult{
		SyncedCount:  syncedCount,
		RemovedCount: removedCount,
		Patches:      syncedPatches,
	}, nil
}
