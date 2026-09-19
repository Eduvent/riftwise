package catalog

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ErrNotFound means no complete catalog has been persisted yet.
var ErrNotFound = errors.New("catalog not found")

// Gold describes Data Dragon's item cost fields.
type Gold struct {
	Base        int  `json:"base"`
	Total       int  `json:"total"`
	Sell        int  `json:"sell"`
	Purchasable bool `json:"purchasable"`
}

// Image identifies an image inside a Data Dragon sprite sheet.
type Image struct {
	Full   string `json:"full"`
	Sprite string `json:"sprite"`
	Group  string `json:"group"`
	X      int    `json:"x"`
	Y      int    `json:"y"`
	W      int    `json:"w"`
	H      int    `json:"h"`
}

// Item is the patch-specific item knowledge used by analysis and itemization.
type Item struct {
	ID             int                `json:"id"`
	Name           string             `json:"name"`
	Description    string             `json:"description"`
	Plaintext      string             `json:"plaintext"`
	Purchasable    bool               `json:"purchasable"`
	AvailableMap11 bool               `json:"available_map_11"`
	Gold           Gold               `json:"gold"`
	From           []int              `json:"from"`
	Into           []int              `json:"into"`
	Tags           []string           `json:"tags"`
	Stats          map[string]float64 `json:"stats"`
	Image          Image              `json:"image"`
}

// AbilityText retains the patch text needed by deterministic damage heuristics.
type AbilityText struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Tooltip     string `json:"tooltip,omitempty"`
}

// Champion is a normalized Data Dragon champion definition.
type Champion struct {
	ID      string        `json:"id"`
	Key     string        `json:"key"`
	Name    string        `json:"name"`
	Tags    []string      `json:"tags"`
	Passive AbilityText   `json:"passive"`
	Spells  []AbilityText `json:"spells"`
}

// Catalog is a complete, immutable patch snapshot.
type Catalog struct {
	Patch     string              `json:"patch"`
	Locale    string              `json:"locale"`
	SyncedAt  time.Time           `json:"synced_at"`
	Items     map[int]Item        `json:"items"`
	Champions map[string]Champion `json:"champions"`
}

// Validate rejects incomplete catalogs before they reach storage or the engine.
func (catalog Catalog) Validate() error {
	if strings.TrimSpace(catalog.Patch) == "" {
		return errors.New("catalog patch is required")
	}
	if strings.TrimSpace(catalog.Locale) == "" {
		return errors.New("catalog locale is required")
	}
	if len(catalog.Items) == 0 {
		return errors.New("catalog must contain items")
	}
	if len(catalog.Champions) == 0 {
		return errors.New("catalog must contain champions")
	}
	for key, item := range catalog.Items {
		if key <= 0 || item.ID != key {
			return fmt.Errorf("catalog item key %d does not match item ID %d", key, item.ID)
		}
		for _, recipeID := range append(append([]int(nil), item.From...), item.Into...) {
			if recipeID <= 0 {
				return fmt.Errorf("catalog item %d has invalid recipe item ID %d", item.ID, recipeID)
			}
			if _, exists := catalog.Items[recipeID]; !exists {
				return fmt.Errorf("catalog item %d references missing recipe item %d", item.ID, recipeID)
			}
		}
	}
	return nil
}

// Repository persists complete catalog snapshots.
type Repository interface {
	SaveCatalog(context.Context, Catalog) error
	LoadLatestCatalog(context.Context, string) (Catalog, error)
	LoadCatalogForVersion(context.Context, string, string) (Catalog, error)
}

// Source retrieves patch versions and normalized catalogs.
type Source interface {
	Versions(context.Context) ([]string, error)
	FetchCatalog(context.Context, string, string) (Catalog, error)
}

// Status is safe to expose through health and dashboard APIs.
type Status struct {
	Ready     bool      `json:"ready"`
	Patch     string    `json:"patch,omitempty"`
	Locale    string    `json:"locale,omitempty"`
	Source    string    `json:"source"`
	Fallback  bool      `json:"fallback"`
	LastError string    `json:"last_error,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Manager synchronizes remote catalogs while keeping the last complete cache available.
type Manager struct {
	source Source
	repo   Repository
	locale string
	now    func() time.Time

	refreshMu sync.Mutex
	mu        sync.RWMutex
	current   *Catalog
	status    Status
	versions  []string

	remoteEnabled  bool
	successfulLive string
	retryLive      string
	retryDelay     time.Duration
	nextRetry      time.Time
}

const (
	initialRetryDelay = 2 * time.Second
	maximumRetryDelay = 30 * time.Second
)

// NewManager creates a catalog manager.
func NewManager(source Source, repo Repository, locale string) (*Manager, error) {
	if source == nil {
		return nil, errors.New("catalog source must not be nil")
	}
	if repo == nil {
		return nil, errors.New("catalog repository must not be nil")
	}
	if strings.TrimSpace(locale) == "" {
		return nil, errors.New("catalog locale must not be empty")
	}
	return &Manager{
		source: source,
		repo:   repo,
		locale: locale,
		now:    func() time.Time { return time.Now().UTC() },
		status: Status{Source: "unavailable", UpdatedAt: time.Now().UTC()},
	}, nil
}

// Initialize performs the configured startup behavior without making cache failure fatal.
func (manager *Manager) Initialize(ctx context.Context, syncRemote bool) error {
	manager.mu.Lock()
	manager.remoteEnabled = syncRemote
	manager.mu.Unlock()
	if !syncRemote {
		return manager.LoadCache(ctx)
	}
	return manager.Refresh(ctx, "")
}

// EnsureForGame selects a patch matching the live major.minor version when available.
func (manager *Manager) EnsureForGame(ctx context.Context, liveVersion string) error {
	live := MajorMinor(liveVersion)
	if live == "" {
		return nil
	}

	manager.mu.RLock()
	if live == manager.successfulLive {
		manager.mu.RUnlock()
		return nil
	}
	remoteEnabled := manager.remoteEnabled
	retryDeferred := manager.retryLive == live && manager.now().Before(manager.nextRetry)
	manager.mu.RUnlock()

	// Cache selection is always performed before a network attempt. This keeps
	// cache-only mode patch-aware and avoids retaining an unrelated latest patch.
	cacheErr := manager.selectCachedForGame(ctx, liveVersion)
	if !remoteEnabled {
		if cacheErr != nil {
			return cacheErr
		}
		manager.recordCacheOnlySelectionSuccess(liveVersion)
		manager.recordLiveSuccess(live)
		return nil
	}

	manager.mu.RLock()
	currentIsPersistedMatch := manager.current != nil &&
		MajorMinor(manager.current.Patch) == live &&
		manager.status.Source == "remote" && manager.status.LastError == ""
	manager.mu.RUnlock()
	if currentIsPersistedMatch {
		manager.recordLiveSuccess(live)
		return nil
	}
	if retryDeferred {
		return nil
	}

	err := manager.Refresh(ctx, liveVersion)
	if err != nil {
		manager.recordLiveFailure(live)
		return err
	}
	manager.recordLiveSuccess(live)
	return nil
}

// Refresh synchronizes the preferred patch, falling back to the latest complete cache.
func (manager *Manager) Refresh(ctx context.Context, liveVersion string) error {
	manager.refreshMu.Lock()
	defer manager.refreshMu.Unlock()

	versions, err := manager.source.Versions(ctx)
	if err != nil {
		return manager.useCache(ctx, liveVersion, fmt.Errorf("fetch Data Dragon versions: %w", err))
	}
	patch, fallback, err := SelectPatch(versions, liveVersion)
	if err != nil {
		return manager.useCache(ctx, liveVersion, err)
	}

	remote, err := manager.source.FetchCatalog(ctx, patch, manager.locale)
	if err != nil {
		return manager.useCache(ctx, liveVersion, fmt.Errorf("fetch Data Dragon catalog %s: %w", patch, err))
	}
	if err := remote.Validate(); err != nil {
		return manager.useCache(ctx, liveVersion, fmt.Errorf("validate Data Dragon catalog %s: %w", patch, err))
	}
	if remote.SyncedAt.IsZero() {
		remote.SyncedAt = manager.now()
	}

	saveErr := manager.repo.SaveCatalog(ctx, remote)
	manager.mu.Lock()
	manager.versions = append([]string(nil), versions...)
	clone := Clone(remote)
	manager.current = &clone
	manager.status = Status{
		Ready:     true,
		Patch:     remote.Patch,
		Locale:    remote.Locale,
		Source:    "remote",
		Fallback:  fallback,
		UpdatedAt: manager.now(),
	}
	if saveErr != nil {
		manager.status.Source = "remote_unpersisted"
		manager.status.LastError = "catalog active but cache write failed: " + saveErr.Error()
	}
	manager.mu.Unlock()
	if saveErr != nil {
		return saveErr
	}
	return nil
}

// LoadCache activates the newest complete local catalog.
func (manager *Manager) LoadCache(ctx context.Context) error {
	manager.refreshMu.Lock()
	defer manager.refreshMu.Unlock()
	return manager.loadCacheLocked(ctx, "", nil)
}

func (manager *Manager) useCache(ctx context.Context, liveVersion string, remoteErr error) error {
	manager.mu.RLock()
	hasCurrent := manager.current != nil
	manager.mu.RUnlock()
	if hasCurrent {
		manager.mu.Lock()
		manager.status.Source = "memory_cache"
		manager.status.Fallback = liveVersion != "" && MajorMinor(manager.status.Patch) != MajorMinor(liveVersion)
		manager.status.LastError = remoteErr.Error()
		manager.status.UpdatedAt = manager.now()
		manager.mu.Unlock()
		return remoteErr
	}
	return manager.loadCacheLocked(ctx, liveVersion, remoteErr)
}

func (manager *Manager) loadCacheLocked(ctx context.Context, liveVersion string, cause error) error {
	cached, err := manager.repo.LoadLatestCatalog(ctx, manager.locale)
	if err != nil {
		combined := err
		if cause != nil {
			combined = errors.Join(cause, err)
		}
		manager.mu.Lock()
		manager.status = Status{Source: "unavailable", LastError: combined.Error(), UpdatedAt: manager.now()}
		manager.mu.Unlock()
		return combined
	}
	if err := cached.Validate(); err != nil {
		return fmt.Errorf("validate cached catalog: %w", err)
	}
	message := ""
	if cause != nil {
		message = cause.Error()
	}
	manager.mu.Lock()
	clone := Clone(cached)
	manager.current = &clone
	manager.status = Status{
		Ready:     true,
		Patch:     cached.Patch,
		Locale:    cached.Locale,
		Source:    "cache",
		Fallback:  liveVersion != "" && MajorMinor(cached.Patch) != MajorMinor(liveVersion),
		LastError: message,
		UpdatedAt: manager.now(),
	}
	manager.mu.Unlock()
	return cause
}

func (manager *Manager) selectCachedForGame(ctx context.Context, liveVersion string) error {
	cached, err := manager.repo.LoadCatalogForVersion(ctx, manager.locale, liveVersion)
	if err == nil {
		manager.mu.Lock()
		currentPatch := ""
		if manager.current != nil {
			currentPatch = manager.current.Patch
		}
		// Exact cached game versions take precedence. A major/minor cached match
		// replaces only an unrelated current patch, not a newer remote match.
		if cached.Patch == liveVersion || MajorMinor(currentPatch) != MajorMinor(liveVersion) {
			clone := Clone(cached)
			manager.current = &clone
			manager.status = Status{
				Ready: true, Patch: cached.Patch, Locale: cached.Locale, Source: "cache",
				Fallback: false, UpdatedAt: manager.now(),
			}
		}
		manager.mu.Unlock()
		return nil
	}
	if !errors.Is(err, ErrNotFound) {
		manager.mu.Lock()
		message := "cached live patch lookup failed: " + err.Error()
		if manager.current == nil {
			manager.status = Status{Source: "unavailable", LastError: message, UpdatedAt: manager.now()}
		} else {
			manager.status.Fallback = MajorMinor(manager.current.Patch) != MajorMinor(liveVersion)
			manager.status.LastError = message
			manager.status.UpdatedAt = manager.now()
		}
		manager.mu.Unlock()
		return err
	}

	manager.mu.RLock()
	hasCurrent := manager.current != nil
	manager.mu.RUnlock()
	if !hasCurrent {
		if loadErr := manager.LoadCache(ctx); loadErr != nil {
			return loadErr
		}
	}
	manager.mu.Lock()
	if manager.current != nil {
		manager.status.Fallback = MajorMinor(manager.current.Patch) != MajorMinor(liveVersion)
		manager.status.UpdatedAt = manager.now()
	}
	manager.mu.Unlock()
	return nil
}

func (manager *Manager) recordCacheOnlySelectionSuccess(liveVersion string) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.current == nil {
		return
	}
	manager.status.Ready = true
	manager.status.Patch = manager.current.Patch
	manager.status.Locale = manager.current.Locale
	manager.status.Source = "cache"
	manager.status.Fallback = MajorMinor(manager.current.Patch) != MajorMinor(liveVersion)
	manager.status.LastError = ""
	manager.status.UpdatedAt = manager.now()
}

func (manager *Manager) recordLiveSuccess(live string) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.successfulLive = live
	manager.retryLive = ""
	manager.retryDelay = 0
	manager.nextRetry = time.Time{}
}

func (manager *Manager) recordLiveFailure(live string) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.successfulLive = ""
	if manager.retryLive != live || manager.retryDelay == 0 {
		manager.retryDelay = initialRetryDelay
	} else {
		manager.retryDelay = min(manager.retryDelay*2, maximumRetryDelay)
	}
	manager.retryLive = live
	manager.nextRetry = manager.now().Add(manager.retryDelay)
}

// Current returns an isolated catalog and its status.
func (manager *Manager) Current() (Catalog, Status, bool) {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	if manager.current == nil {
		return Catalog{}, manager.status, false
	}
	return Clone(*manager.current), manager.status, true
}

// Status returns the current synchronization state.
func (manager *Manager) Status() Status {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	return manager.status
}

// SelectPatch prefers a live major.minor match and otherwise selects the latest entry.
func SelectPatch(versions []string, liveVersion string) (patch string, fallback bool, err error) {
	if len(versions) == 0 {
		return "", false, errors.New("Data Dragon versions list is empty")
	}
	live := MajorMinor(liveVersion)
	if live != "" {
		for _, version := range versions {
			if MajorMinor(version) == live {
				return version, false, nil
			}
		}
		return versions[0], true, nil
	}
	return versions[0], false, nil
}

// MajorMinor extracts the first two numeric version components.
func MajorMinor(version string) string {
	parts := strings.Split(strings.TrimSpace(version), ".")
	if len(parts) < 2 {
		return ""
	}
	major, majorErr := strconv.Atoi(parts[0])
	minor, minorErr := strconv.Atoi(parts[1])
	if majorErr != nil || minorErr != nil {
		return ""
	}
	return fmt.Sprintf("%d.%d", major, minor)
}

// Clone makes an isolated copy suitable for concurrent consumers.
func Clone(source Catalog) Catalog {
	clone := source
	clone.Items = make(map[int]Item, len(source.Items))
	for id, item := range source.Items {
		item.From = append([]int(nil), item.From...)
		item.Into = append([]int(nil), item.Into...)
		item.Tags = append([]string(nil), item.Tags...)
		if item.Stats != nil {
			item.Stats = make(map[string]float64, len(item.Stats))
			for key, value := range source.Items[id].Stats {
				item.Stats[key] = value
			}
		}
		clone.Items[id] = item
	}
	clone.Champions = make(map[string]Champion, len(source.Champions))
	keys := make([]string, 0, len(source.Champions))
	for key := range source.Champions {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		champion := source.Champions[key]
		champion.Tags = append([]string(nil), champion.Tags...)
		champion.Spells = append([]AbilityText(nil), champion.Spells...)
		clone.Champions[key] = champion
	}
	return clone
}
