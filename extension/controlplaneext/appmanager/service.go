// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package appmanager

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.uber.org/zap"
)

// ═══════════════════════════════════════════════════
// Sentinel errors for business rules
// ═══════════════════════════════════════════════════

var (
	// ErrAppNameExists is returned when creating/updating an app with a duplicate name.
	ErrAppNameExists = errors.New("app name already exists")

	// ErrTokenExists is returned when a custom token is already in use.
	ErrTokenExists = errors.New("token already exists")

	// ErrTokenInUse is returned with the owning app's name for user-friendly messages.
	// Checked with errors.As: var target *ErrTokenConflict
	ErrInvalidStatus = errors.New("invalid status, must be 'active' or 'disabled'")

	// ErrRetentionOutOfRange is returned when a retention value is outside [Min, Max].
	ErrRetentionOutOfRange = errors.New("retention duration is out of allowed range")
)

// ErrTokenConflict carries the name of the app already using a token.
type ErrTokenConflict struct {
	AppName string
}

func (e *ErrTokenConflict) Error() string {
	return fmt.Sprintf("token already in use by application '%s'", e.AppName)
}

// ═══════════════════════════════════════════════════
// SignalType
// ═══════════════════════════════════════════════════

// SignalType identifies the kind of observability signal for retention config.
type SignalType string

const (
	SignalTrace  SignalType = "trace"
	SignalMetric SignalType = "metric"
	SignalLog    SignalType = "log"
)

// AllSignals returns all supported signal types for iteration.
func AllSignals() []SignalType {
	return []SignalType{SignalTrace, SignalMetric, SignalLog}
}

// ═══════════════════════════════════════════════════
// RetentionLimits
// ═══════════════════════════════════════════════════

// RetentionLimits defines the allowed range for per-signal retention overrides.
type RetentionLimits struct {
	Min time.Duration // minimum allowed (e.g., 24h)
	Max time.Duration // maximum allowed (e.g., 365*24h)
}

// DefaultRetentionLimits returns sensible defaults.
func DefaultRetentionLimits() RetentionLimits {
	return RetentionLimits{
		Min: 24 * time.Hour,
		Max: 365 * 24 * time.Hour,
	}
}

// Validate checks whether the given duration is within allowed bounds.
// A zero value is considered valid (means "use platform default").
func (l RetentionLimits) Validate(d time.Duration) error {
	if d <= 0 {
		return nil
	}
	if d < l.Min {
		return fmt.Errorf("%w: %v < %v", ErrRetentionOutOfRange, d, l.Min)
	}
	if d > l.Max {
		return fmt.Errorf("%w: %v > %v", ErrRetentionOutOfRange, d, l.Max)
	}
	return nil
}

// ═══════════════════════════════════════════════════
// Consumer interfaces (ISP)
// ═══════════════════════════════════════════════════

// AppManager provides CRUD operations for application identity management.
// Consumed by admin UI handlers (adminext/handlers.go).
type AppManager interface {
	CreateApp(ctx context.Context, req *CreateAppRequest) (*AppInfo, error)
	GetApp(ctx context.Context, appID string) (*AppInfo, error)
	UpdateApp(ctx context.Context, appID string, req *UpdateAppRequest) (*AppInfo, error)
	DeleteApp(ctx context.Context, appID string) error
	ListApps(ctx context.Context) ([]*AppInfo, error)
	RegenerateToken(ctx context.Context, appID string) (*AppInfo, error)
	SetToken(ctx context.Context, appID string, req *SetTokenRequest) (*AppInfo, error)
}

// TokenValidator validates agent authentication tokens. This is the narrow
// interface consumed by auth middleware on the hot path — it has no dependency
// on CRUD operations it doesn't need.
type TokenValidator interface {
	ValidateToken(ctx context.Context, token string) (*TokenValidationResult, error)
}

// AppRetentionProvider provides per-app retention policy management.
// Consumed by retention admin handlers and background cleanup schedulers.
type AppRetentionProvider interface {
	GetRetention(ctx context.Context, appID string) (RetentionPolicy, error)
	SetRetention(ctx context.Context, appID string, signal SignalType, d time.Duration) error
	DeleteRetention(ctx context.Context, appID string, signal SignalType) error
}

// Compile-time interface satisfaction checks.
var (
	_ AppManager            = (*AppService)(nil)
	_ TokenValidator        = (*AppService)(nil)
	_ AppRetentionProvider  = (*AppService)(nil)
	_ TokenManager          = (*AppService)(nil)
)

// ═══════════════════════════════════════════════════
// AppService — single business logic implementation
// ═══════════════════════════════════════════════════

// AppService is the single source of truth for all app business rules.
// It depends only on abstractions (AppRepository, IDGenerator, TokenGenerator),
// enabling complete unit-testability without real Redis or random generation.
//
// It implements AppManager, TokenValidator, and AppRetentionProvider,
// satisfying ISP — consumers only see the narrow interface they need.
type AppService struct {
	repo     AppRepository
	idGen    IDGenerator
	tokenGen TokenGenerator
	limits   RetentionLimits
	seedApps []SeedAppConfig
	logger   *zap.Logger
}

// NewAppService creates a new AppService with the given dependencies.
// All parameters are required.
func NewAppService(repo AppRepository, idGen IDGenerator, tokenGen TokenGenerator, limits RetentionLimits, seedApps []SeedAppConfig, logger *zap.Logger) *AppService {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &AppService{
		repo:     repo,
		idGen:    idGen,
		tokenGen: tokenGen,
		limits:   limits,
		seedApps: seedApps,
		logger:   logger,
	}
}

// ════════════════════════════════════════════
// AppManager implementation
// ════════════════════════════════════════════

// CreateApp creates a new app with a generated ID and token.
// Business rules:
//   - Name must be unique across all apps
//   - Custom token (if provided) must not conflict with existing tokens
func (s *AppService) CreateApp(ctx context.Context, req *CreateAppRequest) (*AppInfo, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}

	// Check name uniqueness
	if err := s.checkNameUnique(ctx, req.Name, ""); err != nil {
		return nil, err
	}

	// Generate or validate token
	token, err := s.resolveToken(ctx, req.Token)
	if err != nil {
		return nil, err
	}

	// Generate ID
	id, err := s.idGen.Generate()
	if err != nil {
		return nil, fmt.Errorf("generate id: %w", err)
	}

	now := time.Now()
	app := &AppInfo{
		ID:          id,
		Name:        req.Name,
		Token:       token,
		Description: req.Description,
		Metadata:    req.Metadata,
		Status:      "active",
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	if err := s.repo.Insert(ctx, app); err != nil {
		return nil, fmt.Errorf("insert app: %w", err)
	}

	s.logger.Info("App created", zap.String("id", id), zap.String("name", req.Name))
	return app, nil
}

// GetApp returns app info by ID.
func (s *AppService) GetApp(ctx context.Context, appID string) (*AppInfo, error) {
	return s.repo.FindByID(ctx, appID)
}

// UpdateApp updates an existing app.
// Business rules:
//   - New name (if provided) must be unique (excluding self)
//   - Status must be "active" or "disabled"
func (s *AppService) UpdateApp(ctx context.Context, appID string, req *UpdateAppRequest) (*AppInfo, error) {
	if req == nil {
		return nil, errors.New("update request is required")
	}

	app, err := s.repo.FindByID(ctx, appID)
	if err != nil {
		return nil, err
	}

	// Name change → uniqueness check
	if req.Name != "" && req.Name != app.Name {
		if err := s.checkNameUnique(ctx, req.Name, appID); err != nil {
			return nil, err
		}
		app.Name = req.Name
	}

	if req.Description != "" {
		app.Description = req.Description
	}

	if req.Metadata != nil {
		if app.Metadata == nil {
			app.Metadata = make(map[string]string)
		}
		for k, v := range req.Metadata {
			app.Metadata[k] = v
		}
	}

	if req.Status != "" {
		if req.Status != "active" && req.Status != "disabled" {
			return nil, ErrInvalidStatus
		}
		app.Status = req.Status
	}

	app.UpdatedAt = time.Now()

	if err := s.repo.Save(ctx, app); err != nil {
		return nil, fmt.Errorf("save app: %w", err)
	}

	s.logger.Info("App updated", zap.String("id", appID), zap.String("name", app.Name))
	return app, nil
}

// DeleteApp removes an app and invalidates its token.
func (s *AppService) DeleteApp(ctx context.Context, appID string) error {
	app, err := s.repo.FindByID(ctx, appID)
	if err != nil {
		return err
	}
	if err := s.repo.Delete(ctx, appID); err != nil {
		return err
	}
	s.logger.Info("App deleted", zap.String("id", appID), zap.String("name", app.Name))
	return nil
}

// ListApps returns all registered apps.
func (s *AppService) ListApps(ctx context.Context) ([]*AppInfo, error) {
	return s.repo.List(ctx)
}

// RegenerateToken generates a new token for an app (invalidates the old one).
func (s *AppService) RegenerateToken(ctx context.Context, appID string) (*AppInfo, error) {
	app, err := s.repo.FindByID(ctx, appID)
	if err != nil {
		return nil, err
	}

	token, err := s.tokenGen.Generate()
	if err != nil {
		return nil, fmt.Errorf("generate token: %w", err)
	}

	app.Token = token
	app.UpdatedAt = time.Now()

	if err := s.repo.Save(ctx, app); err != nil {
		return nil, fmt.Errorf("save app: %w", err)
	}

	s.logger.Info("Token regenerated", zap.String("id", appID), zap.String("name", app.Name))
	return app, nil
}

// SetToken sets a custom token for an app. If token is empty, generates a new one.
// Business rules:
//   - Custom token must be unique (excluding self)
//   - Conflict returns ErrTokenConflict with the owning app's name
func (s *AppService) SetToken(ctx context.Context, appID string, req *SetTokenRequest) (*AppInfo, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}

	app, err := s.repo.FindByID(ctx, appID)
	if err != nil {
		return nil, err
	}

	var token string
	if req.Token != "" {
		// Custom token: check conflict with other apps (not self)
		existing, err := s.repo.FindByToken(ctx, req.Token)
		if err == nil {
			if existing.ID != appID {
				return nil, &ErrTokenConflict{AppName: existing.Name}
			}
			// Same app, same token → no-op
			return app, nil
		}
		if !errors.Is(err, ErrNotFound) {
			return nil, fmt.Errorf("check token: %w", err)
		}
		token = req.Token
	} else {
		token, err = s.tokenGen.Generate()
		if err != nil {
			return nil, fmt.Errorf("generate token: %w", err)
		}
	}

	// No change → early return
	if token == app.Token {
		return app, nil
	}

	app.Token = token
	app.UpdatedAt = time.Now()

	if err := s.repo.Save(ctx, app); err != nil {
		return nil, fmt.Errorf("save app: %w", err)
	}

	s.logger.Info("Token set", zap.String("id", appID), zap.String("name", app.Name))
	return app, nil
}

// ════════════════════════════════════════════
// TokenValidator implementation
// ════════════════════════════════════════════

// ValidateToken validates a token and returns the associated app info.
// Returns a non-nil result even when validation fails (Valid=false).
func (s *AppService) ValidateToken(ctx context.Context, token string) (*TokenValidationResult, error) {
	if token == "" {
		return &TokenValidationResult{Valid: false, Reason: "token is empty"}, nil
	}

	app, err := s.repo.FindByToken(ctx, token)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return &TokenValidationResult{Valid: false, Reason: "token not found"}, nil
		}
		return nil, fmt.Errorf("validate token: %w", err)
	}

	if app.Status != "active" {
		return &TokenValidationResult{
			Valid:  false,
			AppID:  app.ID,
			Reason: "app is disabled",
		}, nil
	}

	return &TokenValidationResult{
		Valid:   true,
		AppID:   app.ID,
		AppName: app.Name,
	}, nil
}

// ════════════════════════════════════════════
// AppRetentionProvider implementation
// ════════════════════════════════════════════

// GetRetention returns the retention policy for the given app.
// A zero RetentionPolicy means all signals use platform defaults.
func (s *AppService) GetRetention(ctx context.Context, appID string) (RetentionPolicy, error) {
	app, err := s.repo.FindByID(ctx, appID)
	if err != nil {
		return RetentionPolicy{}, err
	}
	return app.Retention, nil
}

// SetRetention sets per-app retention for a specific signal.
// Business rules:
//   - Duration must be within [Min, Max] (zero = revert to default)
func (s *AppService) SetRetention(ctx context.Context, appID string, signal SignalType, d time.Duration) error {
	if err := s.limits.Validate(d); err != nil {
		return err
	}

	app, err := s.repo.FindByID(ctx, appID)
	if err != nil {
		return err
	}

	setRetention(&app.Retention, signal, d)
	app.UpdatedAt = time.Now()

	if err := s.repo.Save(ctx, app); err != nil {
		return fmt.Errorf("save app: %w", err)
	}

	s.logger.Info("Retention set",
		zap.String("appID", appID),
		zap.String("signal", string(signal)),
		zap.Duration("duration", d),
	)
	return nil
}

// DeleteRetention removes the per-app retention override for a signal
// (sets it to zero, meaning "use platform default").
func (s *AppService) DeleteRetention(ctx context.Context, appID string, signal SignalType) error {
	return s.SetRetention(ctx, appID, signal, 0)
}

// ════════════════════════════════════════════
// Lifecycle (TokenManager compatibility)
// ════════════════════════════════════════════

// Start is a no-op provided for TokenManager interface compatibility.
func (s *AppService) Start(ctx context.Context) error {
	// Auto-register seed apps (built-in apps like collector's own telemetry).
	// Idempotent: if an app with the same name already exists, it is skipped.
	for _, seed := range s.seedApps {
		if err := s.ensureSeedApp(ctx, seed); err != nil {
			s.logger.Error("Failed to register seed app",
				zap.String("name", seed.Name),
				zap.Error(err),
			)
			// Continue with other seed apps instead of failing the whole startup.
		}
	}
	s.logger.Info("AppService started", zap.Int("seed_apps_registered", len(s.seedApps)))
	return nil
}

// ensureSeedApp creates a seed app if one with the same name does not already exist.
func (s *AppService) ensureSeedApp(ctx context.Context, seed SeedAppConfig) error {
	// Check if app with this name already exists (walk the list - seed apps are few).
	apps, err := s.repo.List(ctx)
	if err != nil {
		return err
	}
	for _, a := range apps {
		if a.Name == seed.Name {
			s.logger.Info("Seed app already exists, skipping",
				zap.String("name", seed.Name),
				zap.String("id", a.ID),
			)
			return nil
		}
	}

	req := &CreateAppRequest{
		Name:        seed.Name,
		Token:       seed.Token,
		Description: seed.Description,
	}
	app, err := s.CreateApp(ctx, req)
	if err != nil {
		return err
	}

	s.logger.Info("Seed app registered",
		zap.String("name", app.Name),
		zap.String("id", app.ID),
		zap.String("token", app.Token),
	)
	return nil
}

// Close is a no-op provided for TokenManager interface compatibility.
func (s *AppService) Close() error {
	s.logger.Info("AppService stopped")
	return nil
}

// ════════════════════════════════════════════
// Private helpers
// ════════════════════════════════════════════

// checkNameUnique returns ErrAppNameExists if another app (excluding excludeID) already has the given name.
func (s *AppService) checkNameUnique(ctx context.Context, name, excludeID string) error {
	apps, err := s.repo.List(ctx)
	if err != nil {
		return fmt.Errorf("list apps for name check: %w", err)
	}
	for _, a := range apps {
		if a.Name == name && a.ID != excludeID {
			return ErrAppNameExists
		}
	}
	return nil
}

// resolveToken returns the token to use: custom if provided and non-conflicting, otherwise generates one.
func (s *AppService) resolveToken(ctx context.Context, customToken string) (string, error) {
	if customToken == "" {
		return s.tokenGen.Generate()
	}

	// Check conflict
	_, err := s.repo.FindByToken(ctx, customToken)
	if err == nil {
		return "", ErrTokenExists
	}
	if !errors.Is(err, ErrNotFound) {
		return "", fmt.Errorf("check token: %w", err)
	}

	return customToken, nil
}

// setRetention sets the duration for the given signal on the policy.
func setRetention(p *RetentionPolicy, signal SignalType, d time.Duration) {
	switch signal {
	case SignalTrace:
		p.Trace = d
	case SignalMetric:
		p.Metric = d
	case SignalLog:
		p.Log = d
	}
}
