package database

import (
	"regexp"
	"sort"
	"strings"

	"undined-go/internal/storage/parser"
)

// ThreadNode represents a node in the message conversation tree.
type ThreadNode struct {
	Message  *Message
	Children []*ThreadNode
	Parent   *ThreadNode
	Depth    int
}

// FindParentID finds the parent Message-ID prioritizing In-Reply-To, then References.
func FindParentID(msg *Message, msgMap map[string]*Message) string {
	selfID := parser.CleanMsgID(msg.MessageID)

	// 1. In-Reply-To
	if msg.InReplyTo != "" {
		candidates := parser.ExtractMsgIDs(msg.InReplyTo)
		for i := len(candidates) - 1; i >= 0; i-- {
			cand := candidates[i]
			if cand != selfID {
				if _, ok := msgMap[cand]; ok {
					return cand
				}
			}
		}
	}

	// 2. References (reversed search for nearest ancestor)
	if msg.ReferencesHeader != "" {
		candidates := parser.ExtractMsgIDs(msg.ReferencesHeader)
		for i := len(candidates) - 1; i >= 0; i-- {
			cand := candidates[i]
			if cand != selfID {
				if _, ok := msgMap[cand]; ok {
					return cand
				}
			}
		}
	}

	return ""
}

// ThreadMessages builds the conversation tree and returns a flattened, ordered slice.
func ThreadMessages(messages []*Message, sortBy, sortOrder string) []*Message {
	if len(messages) == 0 {
		return nil
	}

	msgMap := make(map[string]*Message, len(messages))
	nodes := make(map[string]*ThreadNode, len(messages))

	for _, m := range messages {
		mid := parser.CleanMsgID(m.MessageID)
		if mid == "" {
			mid = fmtKey(m.ID)
		}
		msgMap[mid] = m
		nodes[mid] = &ThreadNode{Message: m}
	}

	var rootNodes []*ThreadNode

	for mid, node := range nodes {
		parentID := FindParentID(node.Message, msgMap)
		if parentID != "" && parentID != mid {
			if parentNode, ok := nodes[parentID]; ok {
				// Cycle detection
				curr := parentNode
				isCycle := false
				for curr != nil {
					if curr == node {
						isCycle = true
						break
					}
					curr = curr.Parent
				}

				if !isCycle {
					node.Parent = parentNode
					parentNode.Children = append(parentNode.Children, node)
					continue
				}
			}
		}
		rootNodes = append(rootNodes, node)
	}

	var rePrefix = regexp.MustCompile(`^(?i)(re:\s*|fwd:\s*)+`)
	cleanSubject := func(s string) string {
		return strings.ToLower(rePrefix.ReplaceAllString(s, ""))
	}

	// Helper to calculate thread timestamp for sorting
	getThreadTimestamp := func(root *ThreadNode) int64 {
		maxTS := root.Message.EpochTimestamp
		var walk func(n *ThreadNode)
		walk = func(n *ThreadNode) {
			if n.Message.EpochTimestamp > maxTS {
				maxTS = n.Message.EpochTimestamp
			}
			for _, child := range n.Children {
				walk(child)
			}
		}
		walk(root)
		return maxTS
	}

	isDesc := strings.ToLower(sortOrder) != "asc"

	// Sort root nodes
	sort.SliceStable(rootNodes, func(i, j int) bool {
		mI := rootNodes[i].Message
		mJ := rootNodes[j].Message
		var less bool
		switch strings.ToLower(sortBy) {
		case "subject":
			less = cleanSubject(mI.Subject) < cleanSubject(mJ.Subject)
		case "from":
			fromI := strings.ToLower(mI.FromName)
			if fromI == "" {
				fromI = strings.ToLower(mI.FromAddr)
			}
			fromJ := strings.ToLower(mJ.FromName)
			if fromJ == "" {
				fromJ = strings.ToLower(mJ.FromAddr)
			}
			less = fromI < fromJ
		case "tag":
			tagI := ""
			if mI.TagName != nil {
				tagI = *mI.TagName
			}
			tagJ := ""
			if mJ.TagName != nil {
				tagJ = *mJ.TagName
			}
			less = strings.ToLower(tagI) < strings.ToLower(tagJ)
		default: // "date"
			tsI := getThreadTimestamp(rootNodes[i])
			tsJ := getThreadTimestamp(rootNodes[j])
			less = tsI < tsJ
		}
		if isDesc {
			return !less
		}
		return less
	})

	// Sort children recursively
	var sortChildren func(n *ThreadNode)
	sortChildren = func(n *ThreadNode) {
		sort.SliceStable(n.Children, func(i, j int) bool {
			mI := n.Children[i].Message
			mJ := n.Children[j].Message
			var less bool
			switch strings.ToLower(sortBy) {
			case "subject":
				less = cleanSubject(mI.Subject) < cleanSubject(mJ.Subject)
			case "from":
				fromI := strings.ToLower(mI.FromName)
				if fromI == "" {
					fromI = strings.ToLower(mI.FromAddr)
				}
				fromJ := strings.ToLower(mJ.FromName)
				if fromJ == "" {
					fromJ = strings.ToLower(mJ.FromAddr)
				}
				less = fromI < fromJ
			case "tag":
				tagI := ""
				if mI.TagName != nil {
					tagI = *mI.TagName
				}
				tagJ := ""
				if mJ.TagName != nil {
					tagJ = *mJ.TagName
				}
				less = strings.ToLower(tagI) < strings.ToLower(tagJ)
			default: // "date"
				less = mI.EpochTimestamp < mJ.EpochTimestamp
			}
			if isDesc {
				return !less
			}
			return less
		})
		for _, child := range n.Children {
			sortChildren(child)
		}
	}
	for _, root := range rootNodes {
		sortChildren(root)
	}

	// Flatten tree pre-order with branch prefixes
	var result []*Message
	var dfs func(node *ThreadNode, depth int, hasMoreAncestors []bool)
	dfs = func(node *ThreadNode, depth int, hasMoreAncestors []bool) {
		node.Depth = depth
		var prefix strings.Builder
		for k := 0; k < depth-1; k++ {
			if hasMoreAncestors[k] {
				prefix.WriteString("│  ")
			} else {
				prefix.WriteString("   ")
			}
		}
		if depth > 0 {
			isLast := !hasMoreAncestors[depth-1]
			if isLast {
				prefix.WriteString("└─ ")
			} else {
				prefix.WriteString("├─ ")
			}
		}

		mCopy := *node.Message
		mCopy.ThreadDepth = depth
		mCopy.ThreadPrefix = prefix.String()
		mCopy.HasChildren = len(node.Children) > 0
		result = append(result, &mCopy)

		for i, child := range node.Children {
			hasMore := i < len(node.Children)-1
			dfs(child, depth+1, append(hasMoreAncestors, hasMore))
		}
	}

	for _, root := range rootNodes {
		dfs(root, 0, nil)
	}

	return result
}

func fmtKey(id int64) string {
	return strings.TrimSpace(string(rune(id)))
}

// GetThreadHierarchy retrieves all messages in the conversation thread of msgID.
func (d *Database) GetThreadHierarchy(msgID string) ([]*Message, error) {
	orig, err := d.GetMessageByMsgID(msgID)
	if err != nil {
		return nil, err
	}

	// Follow references and in-reply-to to identify root
	rootID := parser.CleanMsgID(orig.MessageID)
	refs := parser.ExtractMsgIDs(orig.ReferencesHeader)
	if len(refs) > 0 {
		rootID = refs[0]
	} else if orig.InReplyTo != "" {
		inReplies := parser.ExtractMsgIDs(orig.InReplyTo)
		if len(inReplies) > 0 {
			rootID = inReplies[0]
		}
	}

	// Query all messages that match rootID or reference it
	query := `
	SELECT ` + messageSelectColumns + `
	FROM messages m
	JOIN folders f ON m.folder_id = f.id
	WHERE m.message_id = ?
	   OR m.in_reply_to LIKE ?
	   OR m.references_header LIKE ?
	ORDER BY m.epoch_timestamp ASC;
	`
	pattern := "%" + rootID + "%"
	rows, err := d.DB.Query(query, rootID, pattern, pattern)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var thread []*Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		thread = append(thread, m)
	}

	// If no other messages found, return at least the target message
	if len(thread) == 0 {
		thread = append(thread, orig)
	}

	return thread, nil
}

// ParentInfo describes the detected parent message context.
type ParentInfo struct {
	ID           int64  `json:"id"`
	MessageID    string `json:"message_id"`
	FolderID     int64  `json:"folder_id"`
	FolderName   string `json:"folder_name"`
	Subject      string `json:"subject"`
	IsInB4       bool   `json:"is_in_b4"`
	IsSameFolder bool   `json:"is_same_folder"`
}

// FindParentInfo finds parent message context across all folders including b4-imports.
func (d *Database) FindParentInfo(
	inReplyTo, referencesHeader string,
	currentFolderID *int64,
	currentMsgID string,
) (missingParent bool, parentMsgID string, parentInfo *ParentInfo) {
	var candidates []string
	if inReplyTo != "" {
		candidates = append(candidates, parser.ExtractMsgIDs(inReplyTo)...)
		cleaned := parser.CleanMsgID(inReplyTo)
		if cleaned != "" {
			already := false
			for _, c := range candidates {
				if c == cleaned {
					already = true
					break
				}
			}
			if !already {
				candidates = append(candidates, cleaned)
			}
		}
	}
	if referencesHeader != "" {
		refs := parser.ExtractMsgIDs(referencesHeader)
		for i := len(refs) - 1; i >= 0; i-- {
			r := refs[i]
			already := false
			for _, c := range candidates {
				if c == r {
					already = true
					break
				}
			}
			if !already {
				candidates = append(candidates, r)
			}
		}
	}

	if len(candidates) == 0 {
		return false, "", nil
	}

	cleanedCurr := parser.CleanMsgID(currentMsgID)
	primaryParentID := candidates[0]

	// 1. First check if parent exists in b4-imports
	b4Folder, _ := d.GetFolderByName("b4-imports")
	var parentMsg *Message

	if b4Folder != nil {
		for _, cand := range candidates {
			if cleanedCurr != "" && cand == cleanedCurr {
				continue
			}
			m, err := d.GetMessageByFolderAndMsgID(b4Folder.ID, cand)
			if err == nil && m != nil {
				parentMsg = m
				break
			}
		}
	}

	// 2. Next check any folder in the database
	if parentMsg == nil {
		for _, cand := range candidates {
			if cleanedCurr != "" && cand == cleanedCurr {
				continue
			}
			m, err := d.GetMessageByMsgID(cand)
			if err == nil && m != nil {
				parentMsg = m
				break
			}
		}
	}

	if parentMsg == nil {
		return true, primaryParentID, nil
	}

	fName := parentMsg.FolderName
	isInB4 := (fName == "b4-imports")
	isSame := (currentFolderID != nil && parentMsg.FolderID == *currentFolderID)

	info := &ParentInfo{
		ID:           parentMsg.ID,
		MessageID:    parentMsg.MessageID,
		FolderID:     parentMsg.FolderID,
		FolderName:   fName,
		Subject:      parentMsg.Subject,
		IsInB4:       isInB4,
		IsSameFolder: isSame,
	}

	return false, primaryParentID, info
}
