package mail

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"sync"
	"time"

	"outlook-mail-manager/internal/accounts"
)

const (
	defaultGraphBaseURL = "https://graph.microsoft.com/v1.0"
	defaultSyncWindow   = 10 * time.Minute
	defaultWorkers      = 4
)

var (
	ErrMessageNotFound      = errors.New("message not found")
	ErrNoActiveAccounts     = errors.New("no active Microsoft accounts")
	ErrRuleNotFound         = errors.New("classification rule not found")
	ErrInvalidRule          = errors.New("invalid classification rule")
	ErrInvalidCategory      = errors.New("invalid message category")
	ErrCleanupNotFound      = errors.New("cleanup action not found")
	ErrInvalidCleanupState  = errors.New("invalid cleanup state")
	ErrCleanupStateConflict = errors.New("cleanup action state conflict")
	ErrCleanupProtected     = errors.New("message is protected from cleanup")
	ErrPersonalRuleNotFound = errors.New("personal inbox rule not found")
	ErrInvalidPersonalRule  = errors.New("invalid personal inbox rule")
)

type Options struct {
	DataDir         string
	GraphBaseURL    string
	HTTPClient      *http.Client
	Now             func() time.Time
	Random          io.Reader
	DiskUsage       func(string) (int, error)
	SchedulerWindow time.Duration
	Workers         int
	Notifier        NotificationSink
}

type NotificationSink interface {
	EnqueueMessage(context.Context, string) error
	EnqueueSystem(context.Context, string, string) error
}

type Service struct {
	db              *sql.DB
	graph           *graphClient
	dataDir         string
	now             func() time.Time
	random          io.Reader
	diskUsage       func(string) (int, error)
	schedulerWindow time.Duration
	workers         int
	highPriority    chan syncJob
	background      chan syncJob
	backgroundJobs  sync.Map
	accountLocks    sync.Map
	ctx             context.Context
	cancel          context.CancelFunc
	startOnce       sync.Once
	closeOnce       sync.Once
	wait            sync.WaitGroup
	settingsChanged chan struct{}
	notifier        NotificationSink
}

type Status struct {
	Disk           DiskState `json:"disk"`
	HighPriority   int       `json:"high_priority_queue"`
	Background     int       `json:"background_queue"`
	ActiveAccounts int       `json:"active_accounts"`
}

type syncJob struct {
	accountID int64
	manual    bool
	result    chan error
}

type scheduledAccount struct {
	id     int64
	offset time.Duration
}

func New(db *sql.DB, tokens *accounts.TokenManager, options Options) (*Service, error) {
	if db == nil || tokens == nil {
		return nil, errors.New("mail database and token manager are required")
	}
	if options.DataDir == "" {
		return nil, errors.New("mail data directory is required")
	}
	if options.GraphBaseURL == "" {
		options.GraphBaseURL = defaultGraphBaseURL
	}
	if options.HTTPClient == nil {
		options.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Random == nil {
		options.Random = rand.Reader
	}
	if options.DiskUsage == nil {
		options.DiskUsage = filesystemUsedPercent
	}
	if options.Workers <= 0 {
		options.Workers = defaultWorkers
	}
	graph, err := newGraphClient(options.GraphBaseURL, tokens, options.HTTPClient, options.Now)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Service{
		db: db, graph: graph, dataDir: options.DataDir, now: options.Now, random: options.Random,
		diskUsage: options.DiskUsage, schedulerWindow: options.SchedulerWindow, workers: options.Workers,
		highPriority: make(chan syncJob, 64), background: make(chan syncJob, 1024),
		ctx: ctx, cancel: cancel, settingsChanged: make(chan struct{}, 1),
		notifier: options.Notifier,
	}, nil
}

func (s *Service) Start() {
	s.startOnce.Do(func() {
		s.wait.Add(1)
		go func() {
			defer s.wait.Done()
			_ = s.ReclassifyAll(s.ctx)
		}()
		for index := 0; index < s.workers; index++ {
			s.wait.Add(1)
			go s.worker()
		}
		s.wait.Add(1)
		go s.scheduler()
		s.wait.Add(1)
		go s.cleanupScheduler()
	})
}

func (s *Service) Close() {
	s.closeOnce.Do(func() {
		s.cancel()
		s.wait.Wait()
	})
}

func (s *Service) SyncAccount(ctx context.Context, publicID string) error {
	var accountID int64
	err := s.db.QueryRowContext(ctx, "SELECT id FROM accounts WHERE public_id = ?", publicID).Scan(&accountID)
	if errors.Is(err, sql.ErrNoRows) {
		return accounts.ErrAccountNotFound
	}
	if err != nil {
		return fmt.Errorf("load sync account: %w", err)
	}
	result := make(chan error, 1)
	job := syncJob{accountID: accountID, manual: true, result: result}
	select {
	case s.highPriority <- job:
	case <-ctx.Done():
		return ctx.Err()
	case <-s.ctx.Done():
		return errors.New("mail sync service is stopping")
	}
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-s.ctx.Done():
		return errors.New("mail sync service is stopping")
	}
}

func (s *Service) EnqueueAll(ctx context.Context) (int, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id FROM accounts WHERE status IN ('active', 'degraded') ORDER BY id
	`)
	if err != nil {
		return 0, fmt.Errorf("list sync accounts: %w", err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var accountID int64
		if err := rows.Scan(&accountID); err != nil {
			return count, fmt.Errorf("scan sync account: %w", err)
		}
		select {
		case s.highPriority <- syncJob{accountID: accountID, manual: true}:
			count++
		case <-ctx.Done():
			return count, ctx.Err()
		case <-s.ctx.Done():
			return count, errors.New("mail sync service is stopping")
		}
	}
	if err := rows.Err(); err != nil {
		return count, fmt.Errorf("iterate sync accounts: %w", err)
	}
	if count == 0 {
		return 0, ErrNoActiveAccounts
	}
	return count, nil
}

func (s *Service) Status(ctx context.Context) (Status, error) {
	disk, err := s.DiskState()
	if err != nil {
		return Status{}, err
	}
	if s.notifier != nil && disk.Level != "normal" {
		_ = s.notifier.EnqueueSystem(ctx, "disk."+disk.Level,
			fmt.Sprintf("数据磁盘占用 %d%%，当前状态：%s。", disk.UsedPercent, disk.Level))
	}
	var active int
	if err := s.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM accounts WHERE status IN ('active', 'degraded')",
	).Scan(&active); err != nil {
		return Status{}, fmt.Errorf("count active accounts: %w", err)
	}
	return Status{
		Disk: disk, HighPriority: len(s.highPriority), Background: len(s.background), ActiveAccounts: active,
	}, nil
}

func (s *Service) DiskState() (DiskState, error) {
	usedPercent, err := s.diskUsage(s.dataDir)
	if err != nil {
		return DiskState{}, fmt.Errorf("read data filesystem usage: %w", err)
	}
	return classifyDisk(usedPercent, s.now().UTC()), nil
}

func (s *Service) worker() {
	defer s.wait.Done()
	for {
		var job syncJob
		select {
		case job = <-s.highPriority:
		default:
			select {
			case job = <-s.highPriority:
			case job = <-s.background:
			case <-s.ctx.Done():
				return
			}
		}
		err := s.syncAccount(s.ctx, job.accountID)
		if !job.manual {
			s.backgroundJobs.Delete(job.accountID)
		}
		if job.result != nil {
			job.result <- err
		}
	}
}

func (s *Service) scheduler() {
	defer s.wait.Done()
schedule:
	for {
		cycleStart := s.now().UTC()
		window := s.schedulerCycleWindow(s.ctx)
		accounts, err := s.scheduledAccounts(s.ctx, window)
		if err == nil {
			for _, account := range accounts {
				wait := cycleStart.Add(account.offset).Sub(s.now().UTC())
				if wait > 0 {
					changed, stopped := s.waitForSchedule(wait)
					if stopped {
						return
					}
					if changed {
						continue schedule
					}
				}
				if !s.enqueueBackground(account.id) && s.ctx.Err() != nil {
					return
				}
			}
		}
		remaining := cycleStart.Add(window).Sub(s.now().UTC())
		if remaining < time.Second {
			remaining = time.Second
		}
		_, stopped := s.waitForSchedule(remaining)
		if stopped {
			return
		}
	}
}

func (s *Service) cleanupScheduler() {
	defer s.wait.Done()
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		_ = s.ProcessDueCleanup(s.ctx)
		select {
		case <-ticker.C:
		case <-s.ctx.Done():
			return
		}
	}
}

func (s *Service) scheduledAccounts(ctx context.Context, window time.Duration) ([]scheduledAccount, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id FROM accounts
		WHERE status IN ('active', 'degraded')
			AND (sync_next_retry_at_utc IS NULL OR sync_next_retry_at_utc <= ?)
	`, formatTime(s.now().UTC()))
	if err != nil {
		return nil, fmt.Errorf("load scheduled accounts: %w", err)
	}
	defer rows.Close()
	result := make([]scheduledAccount, 0)
	for rows.Next() {
		var accountID int64
		if err := rows.Scan(&accountID); err != nil {
			return nil, fmt.Errorf("scan scheduled account: %w", err)
		}
		result = append(result, scheduledAccount{id: accountID, offset: syncOffset(accountID, window)})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate scheduled accounts: %w", err)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].offset < result[j].offset })
	return result, nil
}

func (s *Service) schedulerCycleWindow(ctx context.Context) time.Duration {
	if s.schedulerWindow > 0 {
		return s.schedulerWindow
	}
	settings, err := s.GetSettings(ctx)
	if err != nil {
		return defaultSyncWindow
	}
	return time.Duration(settings.SyncIntervalSeconds) * time.Second
}

func (s *Service) enqueueBackground(accountID int64) bool {
	if _, exists := s.backgroundJobs.LoadOrStore(accountID, struct{}{}); exists {
		return false
	}
	select {
	case s.background <- syncJob{accountID: accountID}:
		return true
	case <-s.ctx.Done():
		s.backgroundJobs.Delete(accountID)
		return false
	}
}

func (s *Service) waitForSchedule(duration time.Duration) (changed bool, stopped bool) {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-timer.C:
		return false, false
	case <-s.settingsChanged:
		return true, false
	case <-s.ctx.Done():
		return false, true
	}
}

func (s *Service) accountLock(accountID int64) *sync.Mutex {
	value, _ := s.accountLocks.LoadOrStore(accountID, &sync.Mutex{})
	return value.(*sync.Mutex)
}

func syncOffset(accountID int64, window time.Duration) time.Duration {
	if window <= 0 {
		return 0
	}
	value := sha256.Sum256([]byte(fmt.Sprintf("mail-sync:%d", accountID)))
	number := uint64(value[0])<<56 | uint64(value[1])<<48 | uint64(value[2])<<40 | uint64(value[3])<<32 |
		uint64(value[4])<<24 | uint64(value[5])<<16 | uint64(value[6])<<8 | uint64(value[7])
	return time.Duration(number % uint64(window))
}

func formatTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}
