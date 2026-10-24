// Package imap implements selective incremental IMAP synchronization for LKML mailboxes.
package imap

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"

	"undined-go/internal/config"
	"undined-go/internal/constants"
	"undined-go/internal/storage/database"
	"undined-go/internal/storage/maildir"
	"undined-go/internal/storage/parser"
)

// SyncStatus holds progress information for folder synchronization.
type SyncStatus struct {
	FolderID   int64  `json:"folder_id"`
	FolderName string `json:"folder_name"`
	IsSyncing  bool   `json:"is_syncing"`
	Total      int    `json:"total"`
	Synced     int    `json:"synced"`
	Removed    int    `json:"removed"`
	Status     string `json:"status"` // "idle", "connecting", "syncing", "completed", "cancelled", "error"
	Error      string `json:"error,omitempty"`
}

// Poller manages incremental selective IMAP mailbox synchronization.
type Poller struct {
	Config  *config.Config
	Maildir *maildir.MaildirStore
	DB      *database.Database

	mu              sync.RWMutex
	syncProgress    map[int64]*SyncStatus
	cancelRequested map[int64]bool
	running         bool
}

// NewPoller creates a new IMAP Poller.
func NewPoller(cfg *config.Config, store *maildir.MaildirStore, db *database.Database) *Poller {
	return &Poller{
		Config:          cfg,
		Maildir:         store,
		DB:              db,
		syncProgress:    make(map[int64]*SyncStatus),
		cancelRequested: make(map[int64]bool),
	}
}

func (p *Poller) getTLSConfig() *tls.Config {
	cfg := p.Config.IMAP
	tlsConf := &tls.Config{
		ServerName:         cfg.Host,
		InsecureSkipVerify: !cfg.VerifyCert,
	}

	if cfg.VerifyCert && cfg.CACertPath != "" {
		caPath := config.ExpandPath(cfg.CACertPath)
		caCert, err := os.ReadFile(caPath)
		if err == nil {
			caCertPool := x509.NewCertPool()
			caCertPool.AppendCertsFromPEM(caCert)
			tlsConf.RootCAs = caCertPool
		}
	}

	return tlsConf
}

// Connect dials and authenticates with the configured IMAP server.
func (p *Poller) Connect() (*client.Client, error) {
	cfg := p.Config.IMAP
	port := cfg.Port
	if cfg.UseSSL && port == 143 {
		port = 993
	}
	addr := fmt.Sprintf("%s:%d", cfg.Host, port)
	tlsConf := p.getTLSConfig()

	var c *client.Client
	var err error

	if cfg.UseSSL {
		c, err = client.DialTLS(addr, tlsConf)
		if err != nil {
			return nil, fmt.Errorf("IMAPS TLS connection to %s failed: %w", addr, err)
		}
	} else {
		conn, dialErr := net.DialTimeout("tcp", addr, 15*time.Second)
		if dialErr != nil {
			return nil, fmt.Errorf("TCP connection to %s failed: %w", addr, dialErr)
		}
		c, err = client.New(conn)
		if err != nil {
			conn.Close()
			return nil, fmt.Errorf("IMAP client initialization failed: %w", err)
		}
	}

	c.Timeout = 30 * time.Second

	password := cfg.GetPassword()
	if cfg.Username != "" && password != "" {
		if err := c.Login(cfg.Username, password); err != nil {
			c.Close()
			return nil, fmt.Errorf("IMAP login failed for user %s: %w", cfg.Username, err)
		}
	}

	return c, nil
}

// IsFolderSyncing checks if a folder sync is actively running.
func (p *Poller) IsFolderSyncing(folderID int64) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	st, exists := p.syncProgress[folderID]
	return exists && st.IsSyncing
}

// GetSyncStatus returns the sync progress for a given folder.
func (p *Poller) GetSyncStatus(folderID int64) SyncStatus {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if st, ok := p.syncProgress[folderID]; ok {
		return *st
	}
	return SyncStatus{
		FolderID:  folderID,
		IsSyncing: false,
		Status:    "idle",
	}
}

// CancelFolderSync requests cancellation of an active folder sync.
func (p *Poller) CancelFolderSync(folderID int64) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if st, ok := p.syncProgress[folderID]; ok && st.IsSyncing {
		p.cancelRequested[folderID] = true
		return true
	}
	return false
}

func (p *Poller) isSyncCancelled(folderID int64) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.cancelRequested[folderID]
}

// SyncFolder performs incremental synchronization fetching lightweight header summaries.
func (p *Poller) SyncFolder(c *client.Client, folderID int64, folderName string, lastSyncedUID uint32) (uint32, error) {
	_, err := c.Select(folderName, true)
	if err != nil {
		return lastSyncedUID, fmt.Errorf("failed to select IMAP folder %s: %w", folderName, err)
	}

	criteria := imap.NewSearchCriteria()
	if lastSyncedUID > 0 {
		seqSet := new(imap.SeqSet)
		seqSet.AddRange(lastSyncedUID+1, 0)
		criteria.Uid = seqSet
	}

	uids, err := c.UidSearch(criteria)
	if err != nil {
		return lastSyncedUID, fmt.Errorf("UID search in %s failed: %w", folderName, err)
	}

	// Filter and sort UIDs
	var newUIDs []uint32
	for _, u := range uids {
		if u > lastSyncedUID {
			newUIDs = append(newUIDs, u)
		}
	}
	sort.Slice(newUIDs, func(i, j int) bool { return newUIDs[i] < newUIDs[j] })

	if len(newUIDs) == 0 {
		return lastSyncedUID, nil
	}

	// On initial sync of large archives, limit to latest 100 summaries for responsiveness
	uidsToFetch := newUIDs
	if lastSyncedUID == 0 && len(newUIDs) > 100 {
		uidsToFetch = newUIDs[len(newUIDs)-100:]
	}

	maxUID := lastSyncedUID
	headerSection := &imap.BodySectionName{
		Peek: true,
		BodyPartName: imap.BodyPartName{
			Specifier: imap.HeaderSpecifier,
			Fields: []string{
				"Message-ID", "Date", "From", "To", "Cc", "Subject", "In-Reply-To", "References", "Review-Commit-ID",
			},
		},
	}
	fetchItems := []imap.FetchItem{imap.FetchUid, imap.FetchFlags, headerSection.FetchItem()}

	// Fetch in batches of 50
	const batchSize = 50
	for i := 0; i < len(uidsToFetch); i += batchSize {
		end := i + batchSize
		if end > len(uidsToFetch) {
			end = len(uidsToFetch)
		}
		batch := uidsToFetch[i:end]

		seqSet := new(imap.SeqSet)
		for _, u := range batch {
			seqSet.AddNum(u)
		}

		messages := make(chan *imap.Message, len(batch))
		done := make(chan error, 1)
		go func() {
			done <- c.UidFetch(seqSet, fetchItems, messages)
		}()

		for msg := range messages {
			if msg == nil {
				continue
			}
			isSeen := false
			var flagList []string
			for _, f := range msg.Flags {
				flagList = append(flagList, f)
				if strings.EqualFold(f, imap.SeenFlag) {
					isSeen = true
				}
			}
			flagsStr := strings.Join(flagList, " ")

			for _, literal := range msg.Body {
				headerBytes, _ := io.ReadAll(literal)
				if len(headerBytes) > 0 {
					_, _ = p.DB.InsertHeaderSummary(folderID, msg.Uid, headerBytes, isSeen, flagsStr)
					if msg.Uid > maxUID {
						maxUID = msg.Uid
					}
				}
			}
		}

		if err := <-done; err != nil {
			log.Printf("Batch UID FETCH warning in %s: %v", folderName, err)
		}
	}

	if maxUID > lastSyncedUID {
		_ = p.DB.UpdateFolderLastSyncedUID(folderID, maxUID)
	}
	return maxUID, nil
}

// SyncFullFolder downloads all message summaries not yet cached, tracking progress and supporting cancellation.
func (p *Poller) SyncFullFolder(folderID int64, folderName string) (int, error) {
	if p.Config.IMAP.Username == "" {
		return 0, fmt.Errorf("IMAP username is not configured")
	}

	if p.IsFolderSyncing(folderID) {
		return 0, fmt.Errorf("summary sync already in progress for folder %s", folderName)
	}

	p.mu.Lock()
	p.cancelRequested[folderID] = false
	p.syncProgress[folderID] = &SyncStatus{
		FolderID:   folderID,
		FolderName: folderName,
		IsSyncing:  true,
		Status:     "connecting",
	}
	p.mu.Unlock()

	c, err := p.Connect()
	if err != nil {
		p.mu.Lock()
		p.syncProgress[folderID].IsSyncing = false
		p.syncProgress[folderID].Status = "error"
		p.syncProgress[folderID].Error = err.Error()
		p.mu.Unlock()
		return 0, err
	}
	defer c.Logout()

	_, err = c.Select(folderName, true)
	if err != nil {
		p.mu.Lock()
		p.syncProgress[folderID].IsSyncing = false
		p.syncProgress[folderID].Status = "error"
		p.syncProgress[folderID].Error = err.Error()
		p.mu.Unlock()
		return 0, fmt.Errorf("could not select IMAP folder %s: %w", folderName, err)
	}

	// Search all server UIDs
	criteria := imap.NewSearchCriteria()
	serverUIDs, err := c.UidSearch(criteria)
	if err != nil {
		p.mu.Lock()
		p.syncProgress[folderID].IsSyncing = false
		p.syncProgress[folderID].Status = "error"
		p.syncProgress[folderID].Error = err.Error()
		p.mu.Unlock()
		return 0, fmt.Errorf("could not search UIDs in %s: %w", folderName, err)
	}

	serverUIDSet := make(map[uint32]bool, len(serverUIDs))
	for _, u := range serverUIDs {
		serverUIDSet[u] = true
	}

	localUIDs, _ := p.DB.GetFolderIMAPUIDs(folderID)
	localUIDSet := make(map[uint32]bool, len(localUIDs))
	for _, u := range localUIDs {
		localUIDSet[u] = true
	}

	// Detect purged/removed messages on server
	var removedUIDs []uint32
	for _, u := range localUIDs {
		if !serverUIDSet[u] {
			removedUIDs = append(removedUIDs, u)
		}
	}

	removedCount := 0
	if len(removedUIDs) > 0 {
		deletedMsgs, _ := p.DB.DeleteMessagesByIMAPUIDs(folderID, removedUIDs)
		for _, m := range deletedMsgs {
			if m.FilePath != "" && p.Maildir != nil {
				_ = p.Maildir.DeleteMessage(m.FilePath)
			}
		}
		removedCount = len(deletedMsgs)
	}

	// Missing UIDs to fetch
	var missingUIDs []uint32
	for _, u := range serverUIDs {
		if !localUIDSet[u] {
			missingUIDs = append(missingUIDs, u)
		}
	}
	sort.Slice(missingUIDs, func(i, j int) bool { return missingUIDs[i] < missingUIDs[j] })

	p.mu.Lock()
	p.syncProgress[folderID].Total = len(missingUIDs)
	p.syncProgress[folderID].Removed = removedCount
	if len(missingUIDs) == 0 {
		p.syncProgress[folderID].IsSyncing = false
		p.syncProgress[folderID].Status = "completed"
		p.mu.Unlock()
		if len(serverUIDs) > 0 {
			sort.Slice(serverUIDs, func(i, j int) bool { return serverUIDs[i] < serverUIDs[j] })
			_ = p.DB.UpdateFolderLastSyncedUID(folderID, serverUIDs[len(serverUIDs)-1])
		}
		return 0, nil
	}
	p.syncProgress[folderID].Status = "syncing"
	p.mu.Unlock()

	headerSection := &imap.BodySectionName{
		Peek: true,
		BodyPartName: imap.BodyPartName{
			Specifier: imap.HeaderSpecifier,
			Fields: []string{
				"Message-ID", "Date", "From", "To", "Cc", "Subject", "In-Reply-To", "References", "Review-Commit-ID",
			},
		},
	}
	fetchItems := []imap.FetchItem{imap.FetchUid, imap.FetchFlags, headerSection.FetchItem()}

	syncedCount := 0
	var maxSyncedUID uint32
	const batchSize = 50

	for i := 0; i < len(missingUIDs); i += batchSize {
		if p.isSyncCancelled(folderID) {
			p.mu.Lock()
			p.syncProgress[folderID].IsSyncing = false
			p.syncProgress[folderID].Status = "cancelled"
			p.syncProgress[folderID].Synced = syncedCount
			p.mu.Unlock()
			if maxSyncedUID > 0 {
				_ = p.DB.UpdateFolderLastSyncedUID(folderID, maxSyncedUID)
			}
			return syncedCount, nil
		}

		end := i + batchSize
		if end > len(missingUIDs) {
			end = len(missingUIDs)
		}
		batch := missingUIDs[i:end]

		seqSet := new(imap.SeqSet)
		for _, u := range batch {
			seqSet.AddNum(u)
		}

		messages := make(chan *imap.Message, len(batch))
		done := make(chan error, 1)
		go func() {
			done <- c.UidFetch(seqSet, fetchItems, messages)
		}()

		for msg := range messages {
			if msg == nil {
				continue
			}
			isSeen := false
			var flagList []string
			for _, f := range msg.Flags {
				flagList = append(flagList, f)
				if strings.EqualFold(f, imap.SeenFlag) {
					isSeen = true
				}
			}
			flagsStr := strings.Join(flagList, " ")

			for _, literal := range msg.Body {
				headerBytes, _ := io.ReadAll(literal)
				if len(headerBytes) > 0 {
					_, _ = p.DB.InsertHeaderSummary(folderID, msg.Uid, headerBytes, isSeen, flagsStr)
					syncedCount++
					if msg.Uid > maxSyncedUID {
						maxSyncedUID = msg.Uid
					}
				}
			}
		}

		_ = <-done

		p.mu.Lock()
		p.syncProgress[folderID].Synced = syncedCount
		p.mu.Unlock()
	}

	if maxSyncedUID > 0 {
		_ = p.DB.UpdateFolderLastSyncedUID(folderID, maxSyncedUID)
	} else if len(serverUIDs) > 0 {
		sort.Slice(serverUIDs, func(i, j int) bool { return serverUIDs[i] < serverUIDs[j] })
		_ = p.DB.UpdateFolderLastSyncedUID(folderID, serverUIDs[len(serverUIDs)-1])
	}

	p.mu.Lock()
	p.syncProgress[folderID].IsSyncing = false
	p.syncProgress[folderID].Status = "completed"
	p.syncProgress[folderID].Synced = syncedCount
	p.mu.Unlock()

	return syncedCount, nil
}

// FetchAndHydrateMessage downloads the complete RFC822 message from IMAP on demand, stores in Maildir, and updates DB.
func (p *Poller) FetchAndHydrateMessage(folderName string, folderID int64, imapUID uint32, messageDBID int64) (*database.Message, error) {
	if p.Config.IMAP.Username == "" {
		return nil, fmt.Errorf("IMAP username is not configured")
	}

	c, err := p.Connect()
	if err != nil {
		return nil, err
	}
	defer c.Logout()

	_, err = c.Select(folderName, true)
	if err != nil {
		return nil, fmt.Errorf("could not select folder %s: %w", folderName, err)
	}

	seqSet := new(imap.SeqSet)
	seqSet.AddNum(imapUID)

	section := &imap.BodySectionName{Peek: true}
	fetchItems := []imap.FetchItem{imap.FetchUid, imap.FetchFlags, section.FetchItem()}

	messages := make(chan *imap.Message, 1)
	done := make(chan error, 1)
	go func() {
		done <- c.UidFetch(seqSet, fetchItems, messages)
	}()

	var rawBytes []byte
	var flags []string
	for msg := range messages {
		if msg != nil {
			flags = msg.Flags
			for _, literal := range msg.Body {
				b, _ := io.ReadAll(literal)
				if len(b) > len(rawBytes) {
					rawBytes = b
				}
			}
		}
	}

	if err := <-done; err != nil {
		return nil, fmt.Errorf("failed to fetch full email body: %w", err)
	}

	if len(rawBytes) == 0 {
		return nil, fmt.Errorf("no payload returned for UID %d in folder %s", imapUID, folderName)
	}

	// Determine read flag
	isSeen := false
	for _, f := range flags {
		if strings.EqualFold(f, imap.SeenFlag) {
			isSeen = true
			break
		}
	}
	maildirFlags := ""
	if isSeen {
		maildirFlags = constants.FlagSeen
	}

	// Save to Maildir
	_ = p.Maildir.EnsureFolder(folderName)
	savedPath, err := p.Maildir.AddMessage(folderName, rawBytes, maildirFlags)
	if err != nil {
		return nil, fmt.Errorf("failed to save hydrated email to maildir: %w", err)
	}

	// Parse body plain and patch
	parsed, err := parser.ParseEmail(rawBytes)
	bodyPlain := ""
	isPatch := false
	if err == nil {
		bodyPlain = parsed.BodyPlain
		isPatch = parsed.IsPatch
	}

	// Hydrate in database
	if messageDBID > 0 {
		if err := p.DB.HydrateMessageBody(messageDBID, savedPath, bodyPlain, isPatch); err != nil {
			return nil, fmt.Errorf("failed to hydrate message body in DB: %w", err)
		}
		return p.DB.GetMessageByID(messageDBID)
	}

	// Direct hydration without prior summary: index the saved email file
	dbID, err := p.DB.IndexEmailFile(folderID, savedPath, imapUID)
	if err != nil {
		return nil, fmt.Errorf("failed to index hydrated email in DB: %w", err)
	}
	return p.DB.GetMessageByID(dbID)
}

// MarkIMAPMessageRead updates the \Seen flag on the IMAP server for a message.
func (p *Poller) MarkIMAPMessageRead(folderName string, imapUID uint32, isRead bool) error {
	var addFlags, removeFlags []string
	if isRead {
		addFlags = []string{imap.SeenFlag}
	} else {
		removeFlags = []string{imap.SeenFlag}
	}
	return p.UpdateIMAPMessageFlags(folderName, imapUID, addFlags, removeFlags)
}

// MarkIMAPMessageReplied updates the \Answered flag on the IMAP server for a message.
func (p *Poller) MarkIMAPMessageReplied(folderName string, imapUID uint32, isReplied bool) error {
	var addFlags, removeFlags []string
	if isReplied {
		addFlags = []string{imap.AnsweredFlag}
	} else {
		removeFlags = []string{imap.AnsweredFlag}
	}
	return p.UpdateIMAPMessageFlags(folderName, imapUID, addFlags, removeFlags)
}

// UpdateIMAPMessageCustomTags synchronizes custom tag flag changes to the IMAP server.
func (p *Poller) UpdateIMAPMessageCustomTags(folderName string, imapUID uint32, addTags, removeTags []string) error {
	return p.UpdateIMAPMessageFlags(folderName, imapUID, addTags, removeTags)
}

// UpdateIMAPMessageFlags adds or removes flags on the IMAP server.
func (p *Poller) UpdateIMAPMessageFlags(folderName string, imapUID uint32, addFlags, removeFlags []string) error {
	if p == nil || p.Config == nil || p.Config.IMAP.Username == "" {
		return nil
	}
	if imapUID == 0 || folderName == "" {
		return nil
	}
	if len(addFlags) == 0 && len(removeFlags) == 0 {
		return nil
	}

	c, err := p.Connect()
	if err != nil {
		return err
	}
	defer c.Logout()

	_, err = c.Select(folderName, false)
	if err != nil {
		return fmt.Errorf("failed to select folder %s: %w", folderName, err)
	}

	seqSet := new(imap.SeqSet)
	seqSet.AddNum(imapUID)

	if len(removeFlags) > 0 {
		var flagItems []interface{}
		for _, f := range removeFlags {
			flagItems = append(flagItems, f)
		}
		if err := c.UidStore(seqSet, imap.RemoveFlags, flagItems, nil); err != nil {
			return fmt.Errorf("failed to remove flags %v: %w", removeFlags, err)
		}
	}

	if len(addFlags) > 0 {
		var flagItems []interface{}
		for _, f := range addFlags {
			flagItems = append(flagItems, f)
		}
		if err := c.UidStore(seqSet, imap.AddFlags, flagItems, nil); err != nil {
			return fmt.Errorf("failed to add flags %v: %w", addFlags, err)
		}
	}

	return nil
}

// PollOnce polls all configured sync_folders once.
func (p *Poller) PollOnce() error {
	if p.Config.IMAP.Username == "" || len(p.Config.IMAP.SyncFolders) == 0 {
		return nil
	}

	c, err := p.Connect()
	if err != nil {
		return err
	}
	defer c.Logout()

	for _, folderName := range p.Config.IMAP.SyncFolders {
		folder, err := p.DB.GetOrCreateFolder(folderName, true)
		if err != nil {
			continue
		}
		_, _ = p.SyncFolder(c, folder.ID, folderName, folder.LastSyncedUID)
	}
	return nil
}

// Start launches a periodic polling background loop until ctx is cancelled.
func (p *Poller) Start(ctx context.Context) {
	interval := time.Duration(p.Config.IMAP.PollIntervalSeconds) * time.Second
	if interval < constants.MinIMAPPollIntervalSeconds*time.Second {
		interval = constants.MinIMAPPollIntervalSeconds * time.Second
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Initial poll on startup
	_ = p.PollOnce()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = p.PollOnce()
		}
	}
}
