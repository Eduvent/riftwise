package catalog

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"
)

func TestSelectPatch(t *testing.T) {
	tests := []struct {
		name     string
		versions []string
		live     string
		want     string
		fallback bool
		wantErr  bool
	}{
		{name: "live match", versions: []string{"16.19.1", "16.18.2", "16.18.1"}, live: "16.18.123.456", want: "16.18.2"},
		{name: "fallback latest", versions: []string{"16.19.1", "16.18.1"}, live: "16.17.99", want: "16.19.1", fallback: true},
		{name: "no live version", versions: []string{"16.19.1"}, want: "16.19.1"},
		{name: "empty versions", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			patch, fallback, err := SelectPatch(test.versions, test.live)
			if (err != nil) != test.wantErr || patch != test.want || fallback != test.fallback {
				t.Fatalf("SelectPatch() = %q, %v, %v", patch, fallback, err)
			}
		})
	}
}

func TestManagerUsesCacheWhenRemoteFails(t *testing.T) {
	cached := managerTestCatalog("16.18.1")
	repo := &fakeRepository{catalogs: []Catalog{cached}}
	manager, err := NewManager(&fakeSource{versionsErr: errors.New("offline")}, repo, "es_MX")
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	err = manager.Initialize(context.Background(), true)
	if err == nil {
		t.Fatal("Initialize() should report remote failure while using cache")
	}
	value, status, ready := manager.Current()
	if !ready || value.Patch != "16.18.1" || status.Source != "cache" || status.LastError == "" {
		t.Fatalf("cached manager state = %#v, %#v, %v", value, status, ready)
	}
}

func TestManagerPrefersLivePatchOnce(t *testing.T) {
	source := &fakeSource{
		versions: []string{"16.19.1", "16.18.1"},
		catalogs: map[string]Catalog{
			"16.19.1": managerTestCatalog("16.19.1"),
			"16.18.1": managerTestCatalog("16.18.1"),
		},
	}
	repo := &fakeRepository{}
	manager, err := NewManager(source, repo, "es_MX")
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	if err := manager.Initialize(context.Background(), true); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	if err := manager.EnsureForGame(context.Background(), "16.18.99.1"); err != nil {
		t.Fatalf("EnsureForGame() error = %v", err)
	}
	value, status, _ := manager.Current()
	if value.Patch != "16.18.1" || status.Fallback {
		t.Fatalf("live catalog = %#v, status = %#v", value, status)
	}
	before := source.fetches
	if err := manager.EnsureForGame(context.Background(), "16.18.100.2"); err != nil {
		t.Fatalf("second EnsureForGame() error = %v", err)
	}
	if source.fetches != before {
		t.Fatalf("same major.minor triggered another fetch: %d -> %d", before, source.fetches)
	}
}

func TestManagerRetriesSameLivePatchAfterTransientFailure(t *testing.T) {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	source := &fakeSource{
		versions:     []string{"16.18.1"},
		versionFails: 2, // startup and the first live-patch attempt
		catalogs:     map[string]Catalog{"16.18.1": managerTestCatalog("16.18.1")},
	}
	repo := &fakeRepository{catalogs: []Catalog{managerTestCatalog("16.17.1")}}
	manager, err := NewManager(source, repo, "es_MX")
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	manager.now = func() time.Time { return now }
	if err := manager.Initialize(context.Background(), true); err == nil {
		t.Fatal("Initialize() should expose the startup network failure")
	}
	if err := manager.EnsureForGame(context.Background(), "16.18.99.1"); err == nil {
		t.Fatal("first live-patch attempt should fail")
	}
	if manager.successfulLive != "" {
		t.Fatalf("failed live patch marked successful: %q", manager.successfulLive)
	}
	versionCalls := source.versionCalls
	if err := manager.EnsureForGame(context.Background(), "16.18.99.1"); err != nil {
		t.Fatalf("backoff call error = %v", err)
	}
	if source.versionCalls != versionCalls {
		t.Fatal("live patch retried before bounded backoff elapsed")
	}

	now = now.Add(initialRetryDelay)
	if err := manager.EnsureForGame(context.Background(), "16.18.99.1"); err != nil {
		t.Fatalf("retry EnsureForGame() error = %v", err)
	}
	value, status, ready := manager.Current()
	if !ready || value.Patch != "16.18.1" || status.Source != "remote" || manager.successfulLive != "16.18" {
		t.Fatalf("recovered state = %#v, %#v, successful=%q", value, status, manager.successfulLive)
	}
}

func TestManagerRetriesAfterCatalogPersistenceFailure(t *testing.T) {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	source := &fakeSource{
		versions: []string{"16.18.1"},
		catalogs: map[string]Catalog{"16.18.1": managerTestCatalog("16.18.1")},
	}
	repo := &fakeRepository{saveFailures: 1}
	manager, err := NewManager(source, repo, "es_MX")
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	manager.now = func() time.Time { return now }
	if err := manager.Initialize(context.Background(), false); err == nil {
		// An empty cache is expected here; enable remote for the live attempt.
	} else if !errors.Is(err, ErrNotFound) {
		t.Fatalf("cache-only Initialize() error = %v", err)
	}
	manager.mu.Lock()
	manager.remoteEnabled = true
	manager.mu.Unlock()
	if err := manager.EnsureForGame(context.Background(), "16.18.99.1"); err == nil {
		t.Fatal("persistence failure should fail the live-patch attempt")
	}
	if manager.successfulLive != "" {
		t.Fatal("unpersisted catalog marked successful")
	}
	now = now.Add(initialRetryDelay)
	if err := manager.EnsureForGame(context.Background(), "16.18.99.1"); err != nil {
		t.Fatalf("retry after persistence failure = %v", err)
	}
	if manager.successfulLive != "16.18" || repo.saveCalls != 2 {
		t.Fatalf("retry state successful=%q saves=%d", manager.successfulLive, repo.saveCalls)
	}
}

func TestManagerCacheOnlySelectsMatchingLivePatch(t *testing.T) {
	tests := []struct {
		name         string
		catalogs     []Catalog
		live         string
		wantPatch    string
		wantFallback bool
	}{
		{
			name: "exact cached patch replaces newer arbitrary patch",
			catalogs: []Catalog{
				withSyncedAt(managerTestCatalog("16.18.1"), time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)),
				withSyncedAt(managerTestCatalog("16.19.1"), time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)),
			},
			live: "16.18.1", wantPatch: "16.18.1",
		},
		{
			name: "mismatched latest remains truthful fallback",
			catalogs: []Catalog{
				withSyncedAt(managerTestCatalog("16.19.1"), time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)),
			},
			live: "16.18.9", wantPatch: "16.19.1", wantFallback: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager, err := NewManager(&fakeSource{}, &fakeRepository{catalogs: test.catalogs}, "es_MX")
			if err != nil {
				t.Fatalf("NewManager() error = %v", err)
			}
			if err := manager.Initialize(context.Background(), false); err != nil {
				t.Fatalf("Initialize() error = %v", err)
			}
			if err := manager.EnsureForGame(context.Background(), test.live); err != nil {
				t.Fatalf("EnsureForGame() error = %v", err)
			}
			value, status, ready := manager.Current()
			if !ready || value.Patch != test.wantPatch || status.Fallback != test.wantFallback {
				t.Fatalf("cache selection = %#v, %#v", value, status)
			}
		})
	}
}

func TestManagerCacheOnlyRetriesTransientLivePatchLookupFailure(t *testing.T) {
	repo := &fakeRepository{
		catalogs: []Catalog{
			withSyncedAt(managerTestCatalog("16.17.1"), time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)),
			withSyncedAt(managerTestCatalog("16.18.1"), time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)),
		},
		lookupFailures: 1,
	}
	manager, err := NewManager(&fakeSource{}, repo, "es_MX")
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	if err := manager.Initialize(context.Background(), false); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}

	err = manager.EnsureForGame(context.Background(), "16.18.99.1")
	if err == nil {
		t.Fatal("first EnsureForGame() should expose the transient SQLite lookup failure")
	}
	value, status, ready := manager.Current()
	if !ready || value.Patch != "16.18.1" || status.Fallback || status.LastError == "" {
		t.Fatalf("failed lookup state = %#v, %#v, ready=%v", value, status, ready)
	}
	if manager.successfulLive != "" {
		t.Fatalf("failed lookup advanced successfulLive to %q", manager.successfulLive)
	}

	if err := manager.EnsureForGame(context.Background(), "16.18.99.1"); err != nil {
		t.Fatalf("second EnsureForGame() error = %v", err)
	}
	value, status, ready = manager.Current()
	if !ready || value.Patch != "16.18.1" || status.Fallback || status.LastError != "" {
		t.Fatalf("recovered lookup state = %#v, %#v, ready=%v", value, status, ready)
	}
	if manager.successfulLive != "16.18" {
		t.Fatalf("successfulLive = %q, want 16.18", manager.successfulLive)
	}
	if err := manager.EnsureForGame(context.Background(), "16.18.99.1"); err != nil {
		t.Fatalf("future EnsureForGame() error = %v", err)
	}
	_, status, _ = manager.Current()
	if status.Fallback || status.LastError != "" || status.Source != "cache" {
		t.Fatalf("future cache-only status = %#v", status)
	}
}

func TestCatalogValidationRejectsBrokenRecipeClosure(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Catalog)
	}{
		{name: "map key differs from item ID", mutate: func(value *Catalog) {
			item := value.Items[1001]
			item.ID = 1002
			value.Items[1001] = item
		}},
		{name: "missing from edge", mutate: func(value *Catalog) {
			item := value.Items[1001]
			item.From = []int{9999}
			value.Items[1001] = item
		}},
		{name: "missing into edge", mutate: func(value *Catalog) {
			item := value.Items[1001]
			item.Into = []int{9999}
			value.Items[1001] = item
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := managerTestCatalog("16.18.1")
			test.mutate(&value)
			if err := value.Validate(); err == nil {
				t.Fatal("Validate() unexpectedly succeeded")
			}
		})
	}
}

type fakeSource struct {
	versions     []string
	versionsErr  error
	versionFails int
	versionCalls int
	catalogs     map[string]Catalog
	fetches      int
}

func (source *fakeSource) Versions(context.Context) ([]string, error) {
	source.versionCalls++
	if source.versionFails > 0 {
		source.versionFails--
		return nil, errors.New("transient versions failure")
	}
	return append([]string(nil), source.versions...), source.versionsErr
}

func (source *fakeSource) FetchCatalog(_ context.Context, patch, _ string) (Catalog, error) {
	source.fetches++
	value, exists := source.catalogs[patch]
	if !exists {
		return Catalog{}, errors.New("catalog unavailable")
	}
	return Clone(value), nil
}

type fakeRepository struct {
	catalogs       []Catalog
	saveFailures   int
	saveCalls      int
	lookupFailures int
}

func (repo *fakeRepository) SaveCatalog(_ context.Context, value Catalog) error {
	repo.saveCalls++
	if repo.saveFailures > 0 {
		repo.saveFailures--
		return errors.New("transient catalog save failure")
	}
	for index := range repo.catalogs {
		if repo.catalogs[index].Patch == value.Patch && repo.catalogs[index].Locale == value.Locale {
			repo.catalogs[index] = Clone(value)
			return nil
		}
	}
	repo.catalogs = append(repo.catalogs, Clone(value))
	return nil
}

func (repo *fakeRepository) LoadLatestCatalog(_ context.Context, locale string) (Catalog, error) {
	values := repo.forLocale(locale)
	if len(values) == 0 {
		return Catalog{}, ErrNotFound
	}
	return Clone(values[0]), nil
}

func (repo *fakeRepository) LoadCatalogForVersion(_ context.Context, locale, version string) (Catalog, error) {
	if repo.lookupFailures > 0 {
		repo.lookupFailures--
		return Catalog{}, errors.New("transient catalog lookup failure")
	}
	values := repo.forLocale(locale)
	for _, value := range values {
		if value.Patch == version {
			return Clone(value), nil
		}
	}
	for _, value := range values {
		if MajorMinor(value.Patch) == MajorMinor(version) {
			return Clone(value), nil
		}
	}
	return Catalog{}, ErrNotFound
}

func (repo *fakeRepository) forLocale(locale string) []Catalog {
	values := make([]Catalog, 0, len(repo.catalogs))
	for _, value := range repo.catalogs {
		if value.Locale == locale {
			values = append(values, Clone(value))
		}
	}
	sort.SliceStable(values, func(left, right int) bool {
		return values[left].SyncedAt.After(values[right].SyncedAt)
	})
	return values
}

func managerTestCatalog(patch string) Catalog {
	return Catalog{
		Patch:    patch,
		Locale:   "es_MX",
		SyncedAt: time.Now().UTC(),
		Items: map[int]Item{
			1001: {ID: 1001, Name: "Boots"},
		},
		Champions: map[string]Champion{
			"Garen": {ID: "Garen", Key: "86", Name: "Garen"},
		},
	}
}

func withSyncedAt(value Catalog, syncedAt time.Time) Catalog {
	value.SyncedAt = syncedAt
	return value
}
