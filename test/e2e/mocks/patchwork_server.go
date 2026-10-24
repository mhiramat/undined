package mocks

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
)

// MockPatchworkPatch represents a patch on the mock Patchwork server.
type MockPatchworkPatch struct {
	ID        int            `json:"id"`
	MsgID     string         `json:"msgid"`
	Name      string         `json:"name"`
	State     string         `json:"state"`
	Delegate  map[string]any `json:"delegate"`
	Project   map[string]any `json:"project"`
	Checks    []any          `json:"checks"`
	Series    []any          `json:"series"`
	Mbox      string         `json:"-"`
	CommitRef string         `json:"commit_ref,omitempty"`
	WebURL    string         `json:"web_url,omitempty"`
}

// MockPatchworkServer simulates patchwork.kernel.org REST API v1.4.
type MockPatchworkServer struct {
	Server  *httptest.Server
	URL     string
	mu      sync.Mutex
	patches map[int]*MockPatchworkPatch
	users   map[string]int // username/email -> id
}

// NewMockPatchworkServer initializes an in-memory Patchwork REST API v1.4 server.
func NewMockPatchworkServer() *MockPatchworkServer {
	mp := &MockPatchworkServer{
		patches: make(map[int]*MockPatchworkPatch),
		users: map[string]int{
			"dev":               1,
			"dev@example.com":   1,
			"maintainer":        2,
			"maintainer2":       3,
			"reviewer":          4,
		},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/1.4/patches/", mp.handlePatches)
	mux.HandleFunc("/api/1.4/patches", mp.handlePatches)
	mux.HandleFunc("/patches/", mp.handlePatches)
	mux.HandleFunc("/patches", mp.handlePatches)
	mux.HandleFunc("/api/1.4/users/", mp.handleUsers)
	mux.HandleFunc("/api/1.4/users", mp.handleUsers)
	mux.HandleFunc("/api/1.4/series/", mp.handleSeries)
	mux.HandleFunc("/api/1.4/projects/", mp.handleProjects)
	mux.HandleFunc("/api/1.4/projects", mp.handleProjects)

	mp.Server = httptest.NewServer(mux)
	mp.URL = mp.Server.URL
	return mp
}

// AddPatch adds or updates a mock patch in the server.
func (mp *MockPatchworkServer) AddPatch(p *MockPatchworkPatch) {
	mp.mu.Lock()
	defer mp.mu.Unlock()
	if p.Project == nil {
		p.Project = map[string]any{
			"id":        1,
			"link_name": "linux-trace",
			"name":      "Linux Trace",
		}
	}
	if p.Checks == nil {
		p.Checks = []any{
			map[string]any{
				"id":          10,
				"context":     "checkpatch",
				"state":       "success",
				"description": "0 errors, 0 warnings",
			},
		}
	}
	if p.WebURL == "" {
		p.WebURL = fmt.Sprintf("https://patchwork.kernel.org/patch/%d/", p.ID)
	}
	mp.patches[p.ID] = p
}

func (mp *MockPatchworkServer) handlePatches(w http.ResponseWriter, r *http.Request) {
	mp.mu.Lock()
	defer mp.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")

	path := strings.TrimPrefix(r.URL.Path, "/api/1.4")
	path = strings.TrimPrefix(path, "/patches")
	path = strings.Trim(path, "/")

	// Sub-resource e.g. /patches/{id}/checks/ or /patches/{id}/mbox/ or /patches/{id}/
	if path != "" {
		parts := strings.Split(path, "/")
		id, err := strconv.Atoi(parts[0])
		if err != nil {
			http.Error(w, `{"detail": "Not found"}`, http.StatusNotFound)
			return
		}
		patch, exists := mp.patches[id]
		if !exists {
			http.Error(w, `{"detail": "Not found"}`, http.StatusNotFound)
			return
		}

		if len(parts) > 1 {
			switch parts[1] {
			case "checks":
				_ = json.NewEncoder(w).Encode(patch.Checks)
				return
			case "mbox":
				w.Header().Set("Content-Type", "text/plain; charset=utf-8")
				w.Write([]byte(patch.Mbox))
				return
			}
		}

		if r.Method == "PATCH" {
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, `{"detail": "Invalid JSON"}`, http.StatusBadRequest)
				return
			}
			if st, ok := body["state"].(string); ok {
				patch.State = st
			}
			if val, hasDelegate := body["delegate"]; hasDelegate {
				if val == nil {
					patch.Delegate = nil
				} else if dNum, ok := val.(float64); ok {
					patch.Delegate = map[string]any{
						"id":       int(dNum),
						"username": mp.findUsernameByID(int(dNum)),
					}
				} else if dStr, ok := val.(string); ok {
					uid := mp.users[dStr]
					patch.Delegate = map[string]any{
						"id":       uid,
						"username": dStr,
					}
				}
			}
			if ref, ok := body["commit_ref"].(string); ok {
				patch.CommitRef = ref
			}
			_ = json.NewEncoder(w).Encode(patch)
			return
		}

		_ = json.NewEncoder(w).Encode(patch)
		return
	}

	// Collection: /patches/?msgid=... or /patches/?delegate=...
	msgID := r.URL.Query().Get("msgid")
	cleanMsgID := strings.Trim(msgID, "<>")
	delegateStr := r.URL.Query().Get("delegate")

	var matches []*MockPatchworkPatch
	for _, p := range mp.patches {
		pClean := strings.Trim(p.MsgID, "<>")
		if msgID != "" && pClean != cleanMsgID && p.MsgID != msgID {
			continue
		}
		if delegateStr != "" {
			dID, _ := strconv.Atoi(delegateStr)
			if p.Delegate == nil {
				continue
			}
			if pDID, ok := p.Delegate["id"].(int); !ok || pDID != dID {
				continue
			}
		}
		matches = append(matches, p)
	}

	if matches == nil {
		matches = make([]*MockPatchworkPatch, 0)
	}
	_ = json.NewEncoder(w).Encode(matches)
}

func (mp *MockPatchworkServer) handleUsers(w http.ResponseWriter, r *http.Request) {
	mp.mu.Lock()
	defer mp.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	username := r.URL.Query().Get("username")
	email := r.URL.Query().Get("email")
	q := r.URL.Query().Get("q")

	query := username
	if query == "" {
		query = email
	}
	if query == "" {
		query = q
	}

	if id, exists := mp.users[query]; exists {
		res := []map[string]any{
			{
				"id":       id,
				"username": query,
				"email":    query,
			},
		}
		_ = json.NewEncoder(w).Encode(res)
		return
	}

	_ = json.NewEncoder(w).Encode([]any{})
}

func (mp *MockPatchworkServer) handleSeries(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	seriesObj := map[string]any{
		"id":      10,
		"version": 2,
		"name":    "[PATCH v2 1/2] trace/kprobes: test series",
		"total":   2,
		"patches": []map[string]any{
			{"id": 42, "name": "[PATCH v2 1/2] trace/kprobes: part 1"},
			{"id": 43, "name": "[PATCH v2 2/2] trace/kprobes: part 2"},
		},
	}
	_ = json.NewEncoder(w).Encode(seriesObj)
}

func (mp *MockPatchworkServer) handleProjects(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	projects := []map[string]any{
		{
			"id":        1,
			"link_name": "linux-trace",
			"name":      "Linux Trace",
		},
	}
	_ = json.NewEncoder(w).Encode(projects)
}

func (mp *MockPatchworkServer) findUsernameByID(id int) string {
	for name, uID := range mp.users {
		if uID == id && !strings.Contains(name, "@") {
			return name
		}
	}
	return "user"
}

// Close terminates the mock Patchwork HTTP server.
func (mp *MockPatchworkServer) Close() {
	mp.Server.Close()
}
