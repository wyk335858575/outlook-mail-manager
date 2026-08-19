package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/hotp"
	"github.com/pquerna/otp/totp"
	"golang.org/x/crypto/argon2"

	"outlook-mail-manager/internal/datakey"
	"outlook-mail-manager/internal/secretbox"
)

const (
	adminID               = 1
	setupChallengeTTL     = 10 * time.Minute
	sessionTTL            = 12 * time.Hour
	sessionTokenBytes     = 32
	challengeIDBytes      = 24
	dataKeyBytes          = 32
	keySaltBytes          = 16
	keyWrapMemoryKiB      = 19 * 1024
	keyWrapIterations     = 2
	keyWrapParallelism    = 1
	totpAssociatedData    = "admin:1:totp-secret"
	dataKeyAssociatedData = "admin:1:data-key"
)

var (
	ErrAlreadyInitialized = errors.New("administrator is already initialized")
	ErrNotInitialized     = errors.New("administrator is not initialized")
	ErrInvalidChallenge   = errors.New("invalid or expired setup challenge")
	ErrInvalidFactor      = errors.New("invalid authentication factor")
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrInvalidSession     = errors.New("invalid session")
	ErrInvalidCSRF        = errors.New("invalid CSRF token")
)

type Options struct {
	Keyring       *datakey.Store
	SecureCookies bool
	Now           func() time.Time
	Random        io.Reader
}

type Service struct {
	db            *sql.DB
	keyring       *datakey.Store
	secureCookies bool
	now           func() time.Time
	random        io.Reader
	passwordGate  chan struct{}
	challengeMu   sync.Mutex
	challenge     *setupChallenge
}

type setupChallenge struct {
	id                  string
	username            string
	passwordHash        string
	keySalt             []byte
	wrappedDataKey      string
	encryptedTOTPSecret string
	totpSecret          string
	dataKey             []byte
	expiresAt           time.Time
}

type SetupStart struct {
	ChallengeID     string
	Secret          string
	ProvisioningURI string
	ExpiresAt       time.Time
}

type SessionGrant struct {
	Token     string
	CSRFToken string
	ExpiresAt time.Time
}

type Status struct {
	Initialized      bool
	Authenticated    bool
	Username         string
	CSRFToken        string
	SessionExpiresAt time.Time
}

func New(db *sql.DB, options Options) (*Service, error) {
	if db == nil {
		return nil, errors.New("auth database is required")
	}
	if options.Keyring == nil {
		return nil, errors.New("data key store is required")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Random == nil {
		options.Random = rand.Reader
	}
	return &Service{
		db: db, keyring: options.Keyring, secureCookies: options.SecureCookies,
		now: options.Now, random: options.Random, passwordGate: make(chan struct{}, 2),
	}, nil
}

func (s *Service) ValidateStartup(ctx context.Context) error {
	initialized, err := s.Initialized(ctx)
	if err != nil || !initialized {
		return err
	}
	var username, encryptedSecret, wrappedDataKey string
	var keySalt []byte
	if err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(username, ''), totp_secret_ciphertext,
			COALESCE(key_salt, X''), COALESCE(wrapped_data_key, '')
		FROM admins WHERE id = ?
	`, adminID).Scan(&username, &encryptedSecret, &keySalt, &wrappedDataKey); err != nil {
		return fmt.Errorf("load administrator credentials: %w", err)
	}
	if username == "" || encryptedSecret == "" || len(keySalt) != keySaltBytes || wrappedDataKey == "" {
		return errors.New("administrator encryption metadata is incomplete")
	}
	if _, err := s.db.ExecContext(ctx, "DELETE FROM sessions"); err != nil {
		return fmt.Errorf("expire sessions after restart: %w", err)
	}
	return nil
}

func (s *Service) Initialized(ctx context.Context) (bool, error) {
	var initialized bool
	if err := s.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM admins WHERE id = ?)", adminID).Scan(&initialized); err != nil {
		return false, fmt.Errorf("check administrator initialization: %w", err)
	}
	return initialized, nil
}

func (s *Service) StartSetup(ctx context.Context, username, password string) (SetupStart, error) {
	initialized, err := s.Initialized(ctx)
	if err != nil {
		return SetupStart{}, err
	}
	if initialized {
		return SetupStart{}, ErrAlreadyInitialized
	}
	username = strings.TrimSpace(username)
	if err := ValidateUsername(username); err != nil {
		return SetupStart{}, err
	}
	if err := ValidatePassword(password); err != nil {
		return SetupStart{}, err
	}
	if err := s.acquirePasswordGate(ctx); err != nil {
		return SetupStart{}, err
	}
	passwordHash, err := HashPassword(password, s.random)
	s.releasePasswordGate()
	if err != nil {
		return SetupStart{}, err
	}
	keySalt, err := randomBytes(s.random, keySaltBytes)
	if err != nil {
		return SetupStart{}, err
	}
	dataKey, err := randomBytes(s.random, dataKeyBytes)
	if err != nil {
		return SetupStart{}, err
	}
	wrappedDataKey, err := wrapDataKey(password, keySalt, dataKey, s.random)
	if err != nil {
		return SetupStart{}, err
	}
	key, err := totp.Generate(totp.GenerateOpts{
		Issuer: "Outlook 邮箱管理台", AccountName: username, Period: 30,
		SecretSize: 20, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1, Rand: s.random,
	})
	if err != nil {
		return SetupStart{}, fmt.Errorf("generate TOTP secret: %w", err)
	}
	dataBox, err := secretbox.New(dataKey, s.random)
	if err != nil {
		return SetupStart{}, err
	}
	encryptedSecret, err := dataBox.SealString(key.Secret(), totpAssociatedData)
	if err != nil {
		return SetupStart{}, err
	}
	challengeID, err := randomURLToken(s.random, challengeIDBytes)
	if err != nil {
		return SetupStart{}, err
	}
	expiresAt := s.now().UTC().Add(setupChallengeTTL)
	s.challengeMu.Lock()
	if s.challenge != nil {
		wipe(s.challenge.dataKey)
	}
	s.challenge = &setupChallenge{
		id: challengeID, username: username, passwordHash: passwordHash,
		keySalt: keySalt, wrappedDataKey: wrappedDataKey, encryptedTOTPSecret: encryptedSecret,
		totpSecret: key.Secret(), dataKey: dataKey, expiresAt: expiresAt,
	}
	s.challengeMu.Unlock()
	return SetupStart{
		ChallengeID: challengeID, Secret: key.Secret(),
		ProvisioningURI: key.URL(), ExpiresAt: expiresAt,
	}, nil
}

func (s *Service) CompleteSetup(ctx context.Context, challengeID, passcode string) (SessionGrant, error) {
	initialized, err := s.Initialized(ctx)
	if err != nil {
		return SessionGrant{}, err
	}
	if initialized {
		return SessionGrant{}, ErrAlreadyInitialized
	}
	s.challengeMu.Lock()
	challenge := s.challenge
	if challenge == nil || challenge.id != challengeID || !s.now().UTC().Before(challenge.expiresAt) {
		s.challengeMu.Unlock()
		return SessionGrant{}, ErrInvalidChallenge
	}
	step, valid, err := verifyTOTP(challenge.totpSecret, passcode, s.now().UTC(), 0)
	if err != nil || !valid {
		s.challengeMu.Unlock()
		if err != nil {
			return SessionGrant{}, err
		}
		return SessionGrant{}, ErrInvalidFactor
	}
	s.challenge = nil
	s.challengeMu.Unlock()
	defer wipe(challenge.dataKey)
	grant, tokenHash, err := s.newSessionGrant()
	if err != nil {
		return SessionGrant{}, err
	}
	now := s.now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SessionGrant{}, fmt.Errorf("begin administrator setup: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO admins (
			id, username, password_hash, password_algorithm, totp_secret_ciphertext,
			key_salt, wrapped_data_key, last_totp_step, created_at_utc, password_updated_at_utc
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, adminID, challenge.username, challenge.passwordHash, passwordAlgorithm,
		challenge.encryptedTOTPSecret, challenge.keySalt, challenge.wrappedDataKey,
		step, formatTime(now), formatTime(now)); err != nil {
		if initialized, checkErr := s.Initialized(ctx); checkErr == nil && initialized {
			return SessionGrant{}, ErrAlreadyInitialized
		}
		return SessionGrant{}, fmt.Errorf("create administrator: %w", err)
	}
	if err := insertSession(ctx, tx, tokenHash, grant.ExpiresAt, now); err != nil {
		return SessionGrant{}, err
	}
	if err := insertAudit(ctx, tx, "admin_initialized", "admin", map[string]string{"factor": "totp"}, now); err != nil {
		return SessionGrant{}, err
	}
	if _, err := tx.ExecContext(ctx,
		"UPDATE app_metadata SET value = ?, updated_at_utc = ? WHERE key = 'installation_state'",
		"security_initialized", formatTime(now)); err != nil {
		return SessionGrant{}, fmt.Errorf("update installation state: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return SessionGrant{}, fmt.Errorf("commit administrator setup: %w", err)
	}
	if err := s.keyring.Unlock(challenge.dataKey); err != nil {
		return SessionGrant{}, fmt.Errorf("unlock data encryption key: %w", err)
	}
	return grant, nil
}

func (s *Service) Login(ctx context.Context, username, password, passcode string) (SessionGrant, error) {
	var storedUsername, passwordHash, encryptedSecret, wrappedDataKey string
	var keySalt []byte
	var lastTOTPStep int64
	err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(username, ''), password_hash, totp_secret_ciphertext,
			COALESCE(key_salt, X''), COALESCE(wrapped_data_key, ''), last_totp_step
		FROM admins WHERE id = ?
	`, adminID).Scan(&storedUsername, &passwordHash, &encryptedSecret, &keySalt, &wrappedDataKey, &lastTOTPStep)
	if errors.Is(err, sql.ErrNoRows) {
		return SessionGrant{}, ErrNotInitialized
	}
	if err != nil {
		return SessionGrant{}, fmt.Errorf("load administrator credentials: %w", err)
	}
	if err := s.acquirePasswordGate(ctx); err != nil {
		return SessionGrant{}, err
	}
	passwordValid, err := VerifyPassword(passwordHash, password)
	s.releasePasswordGate()
	if err != nil {
		return SessionGrant{}, err
	}
	if !passwordValid || !secureUsernameEqual(storedUsername, username) {
		return SessionGrant{}, s.loginFailure(ctx)
	}
	dataKey, err := unwrapDataKey(password, keySalt, wrappedDataKey)
	if err != nil {
		return SessionGrant{}, fmt.Errorf("unlock administrator data key: %w", err)
	}
	defer wipe(dataKey)
	dataBox, err := secretbox.New(dataKey, s.random)
	if err != nil {
		return SessionGrant{}, err
	}
	secret, err := dataBox.OpenString(encryptedSecret, totpAssociatedData)
	if err != nil {
		return SessionGrant{}, fmt.Errorf("decrypt TOTP secret: %w", err)
	}
	now := s.now().UTC()
	step, valid, err := verifyTOTP(secret, passcode, now, lastTOTPStep)
	if err != nil {
		return SessionGrant{}, err
	}
	if !valid {
		return SessionGrant{}, s.loginFailure(ctx)
	}
	grant, tokenHash, err := s.newSessionGrant()
	if err != nil {
		return SessionGrant{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SessionGrant{}, fmt.Errorf("begin login: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx,
		"UPDATE admins SET last_totp_step = ? WHERE id = ? AND last_totp_step < ?",
		step, adminID, step)
	if err != nil {
		return SessionGrant{}, fmt.Errorf("record TOTP step: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return SessionGrant{}, fmt.Errorf("read TOTP update result: %w", err)
	}
	if rows != 1 {
		if err := tx.Rollback(); err != nil {
			return SessionGrant{}, fmt.Errorf("rollback replayed TOTP login: %w", err)
		}
		return SessionGrant{}, s.loginFailure(ctx)
	}
	if err := insertSession(ctx, tx, tokenHash, grant.ExpiresAt, now); err != nil {
		return SessionGrant{}, err
	}
	if err := insertAudit(ctx, tx, "login_succeeded", "admin", map[string]string{"factor": "totp"}, now); err != nil {
		return SessionGrant{}, err
	}
	if err := tx.Commit(); err != nil {
		return SessionGrant{}, fmt.Errorf("commit login: %w", err)
	}
	if err := s.keyring.Unlock(dataKey); err != nil {
		return SessionGrant{}, fmt.Errorf("unlock data encryption key: %w", err)
	}
	return grant, nil
}

func (s *Service) Status(ctx context.Context, rawToken string) (Status, error) {
	initialized, err := s.Initialized(ctx)
	if err != nil {
		return Status{}, err
	}
	status := Status{Initialized: initialized}
	if !initialized || rawToken == "" || s.keyring.Locked() {
		return status, nil
	}
	now := s.now().UTC()
	tokenHash := sha256.Sum256([]byte(rawToken))
	var expiresAtValue, username string
	err = s.db.QueryRowContext(ctx, `
		SELECT s.expires_at_utc, a.username FROM sessions s
		JOIN admins a ON a.id = s.admin_id
		WHERE s.token_hash = ? AND s.admin_id = ?
			AND s.revoked_at_utc IS NULL AND s.expires_at_utc > ?
	`, tokenHash[:], adminID, formatTime(now)).Scan(&expiresAtValue, &username)
	if errors.Is(err, sql.ErrNoRows) {
		return status, nil
	}
	if err != nil {
		return Status{}, fmt.Errorf("load session: %w", err)
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, expiresAtValue)
	if err != nil {
		return Status{}, fmt.Errorf("parse session expiry: %w", err)
	}
	if _, err := s.db.ExecContext(ctx,
		"UPDATE sessions SET last_seen_at_utc = ? WHERE token_hash = ?",
		formatTime(now), tokenHash[:]); err != nil {
		return Status{}, fmt.Errorf("update session activity: %w", err)
	}
	status.Authenticated = true
	status.Username = username
	status.CSRFToken = csrfToken(rawToken)
	status.SessionExpiresAt = expiresAt
	return status, nil
}

func (s *Service) Logout(ctx context.Context, rawToken, csrf string) error {
	status, err := s.Status(ctx, rawToken)
	if err != nil {
		return err
	}
	if !status.Authenticated {
		return ErrInvalidSession
	}
	if subtle.ConstantTimeCompare([]byte(status.CSRFToken), []byte(csrf)) != 1 {
		return ErrInvalidCSRF
	}
	now := s.now().UTC()
	tokenHash := sha256.Sum256([]byte(rawToken))
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin logout: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx,
		"UPDATE sessions SET revoked_at_utc = ? WHERE token_hash = ? AND revoked_at_utc IS NULL",
		formatTime(now), tokenHash[:])
	if err != nil {
		return fmt.Errorf("revoke session: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read logout result: %w", err)
	}
	if rows != 1 {
		return ErrInvalidSession
	}
	if err := insertAudit(ctx, tx, "logout", "admin", nil, now); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit logout: %w", err)
	}
	return nil
}

func (s *Service) SecureCookies() bool { return s.secureCookies }

func (s *Service) CookieName() string {
	if s.secureCookies {
		return "__Host-omm_session"
	}
	return "omm_session"
}

func (s *Service) acquirePasswordGate(ctx context.Context) error {
	select {
	case s.passwordGate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Service) releasePasswordGate() { <-s.passwordGate }

func (s *Service) newSessionGrant() (SessionGrant, []byte, error) {
	token, err := randomURLToken(s.random, sessionTokenBytes)
	if err != nil {
		return SessionGrant{}, nil, err
	}
	tokenHash := sha256.Sum256([]byte(token))
	expiresAt := s.now().UTC().Add(sessionTTL)
	return SessionGrant{Token: token, CSRFToken: csrfToken(token), ExpiresAt: expiresAt}, tokenHash[:], nil
}

func csrfToken(sessionToken string) string {
	mac := hmac.New(sha256.New, []byte(sessionToken))
	mac.Write([]byte("csrf-v1"))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (s *Service) loginFailure(ctx context.Context) error {
	if err := insertAudit(ctx, s.db, "login_failed", "anonymous", map[string]string{"factor": "totp"}, s.now().UTC()); err != nil {
		return err
	}
	return ErrInvalidCredentials
}

func wrapDataKey(password string, salt, dataKey []byte, random io.Reader) (string, error) {
	box, err := secretbox.New(deriveWrapKey(password, salt), random)
	if err != nil {
		return "", err
	}
	return box.SealString(string(dataKey), dataKeyAssociatedData)
}

func unwrapDataKey(password string, salt []byte, wrapped string) ([]byte, error) {
	if len(salt) != keySaltBytes || wrapped == "" {
		return nil, errors.New("data key metadata is invalid")
	}
	box, err := secretbox.New(deriveWrapKey(password, salt), nil)
	if err != nil {
		return nil, err
	}
	plaintext, err := box.OpenString(wrapped, dataKeyAssociatedData)
	if err != nil {
		return nil, err
	}
	dataKey := []byte(plaintext)
	if len(dataKey) != dataKeyBytes {
		return nil, errors.New("unwrapped data key has an invalid length")
	}
	return dataKey, nil
}

func deriveWrapKey(password string, salt []byte) []byte {
	return argon2.IDKey([]byte(password), salt, keyWrapIterations, keyWrapMemoryKiB, keyWrapParallelism, dataKeyBytes)
}

func secureUsernameEqual(stored, supplied string) bool {
	storedHash := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(stored))))
	suppliedHash := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(supplied))))
	return subtle.ConstantTimeCompare(storedHash[:], suppliedHash[:]) == 1
}

func verifyTOTP(secret, passcode string, now time.Time, lastStep int64) (int64, bool, error) {
	current := now.Unix() / 30
	for _, offset := range []int64{0, -1, 1} {
		step := current + offset
		if step <= lastStep || step < 0 {
			continue
		}
		valid, err := hotp.ValidateCustom(strings.TrimSpace(passcode), uint64(step), secret, hotp.ValidateOpts{
			Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1,
		})
		if err != nil {
			if errors.Is(err, otp.ErrValidateInputInvalidLength) {
				return 0, false, nil
			}
			return 0, false, fmt.Errorf("validate TOTP: %w", err)
		}
		if valid {
			return step, true, nil
		}
	}
	return 0, false, nil
}

func randomBytes(random io.Reader, size int) ([]byte, error) {
	value := make([]byte, size)
	if _, err := io.ReadFull(random, value); err != nil {
		return nil, fmt.Errorf("generate random value: %w", err)
	}
	return value, nil
}

func randomURLToken(random io.Reader, size int) (string, error) {
	value, err := randomBytes(random, size)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func wipe(value []byte) {
	for index := range value {
		value[index] = 0
	}
}

type databaseExecutor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func insertSession(ctx context.Context, executor databaseExecutor, tokenHash []byte, expiresAt, now time.Time) error {
	_, err := executor.ExecContext(ctx, `
		INSERT INTO sessions (token_hash, admin_id, created_at_utc, expires_at_utc, last_seen_at_utc)
		VALUES (?, ?, ?, ?, ?)
	`, tokenHash, adminID, formatTime(now), formatTime(expiresAt), formatTime(now))
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	return nil
}

func insertAudit(ctx context.Context, executor databaseExecutor, eventType, actorType string, details map[string]string, now time.Time) error {
	if details == nil {
		details = map[string]string{}
	}
	encoded, err := json.Marshal(details)
	if err != nil {
		return fmt.Errorf("encode audit details: %w", err)
	}
	if _, err := executor.ExecContext(ctx, `
		INSERT INTO audit_events (event_type, actor_type, details_json, created_at_utc)
		VALUES (?, ?, ?, ?)
	`, eventType, actorType, string(encoded), formatTime(now)); err != nil {
		return fmt.Errorf("write audit event: %w", err)
	}
	return nil
}

func formatTime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }
