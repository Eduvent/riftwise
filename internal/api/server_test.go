package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"lolengine/internal/domain"
	"lolengine/internal/history"
	"lolengine/internal/itemization"
	"lolengine/internal/itemprofiles"
	"lolengine/internal/itemrules"
	"lolengine/internal/matchsync"
	"lolengine/internal/personalization"
	"lolengine/internal/pregame"
	"lolengine/internal/recommendation"
	"lolengine/internal/riotapi"
	"lolengine/internal/state"
	"lolengine/internal/statistics"
	"lolengine/internal/storage"
)

func TestPersonalizationEndpointsAreStrictAndPrivacySeparated(t *testing.T) {
	service := &fakePersonalizationService{preferences: personalization.Preferences{Style: personalization.StyleBalanced, Version: 1}, summary: personalization.FeedbackSummary{MinimumSamples: 3, MaximumAdjustment: 9, Total: 2, Useful: 1, NotUseful: 1}}
	store := testStore()
	personalizationStatus := personalization.Status{Available: true, Enabled: true, FeedbackEnabled: true, Style: personalization.StyleSafe, Version: 4, Revision: 9, State: "ready"}
	live := staticRecommendationService{dashboard: recommendation.DashboardSnapshot{
		Epoch: "personalization-test", Revision: 2, Game: store.Current(), Personalization: personalizationStatus,
	}}
	handler := New(store, Config{Personalization: service, Recommendations: live})

	tests := []struct {
		name   string
		method string
		path   string
		body   string
		status int
		want   string
	}{
		{name: "get preferences", method: http.MethodGet, path: "/api/personalization/preferences", status: 200, want: `"style":"balanced"`},
		{name: "strict update", method: http.MethodPut, path: "/api/personalization/preferences", body: `{"style":"safe","preferred_item_ids":[3001],"avoided_item_ids":[],"feedback_enabled":true}`, status: 200, want: `"style":"safe"`},
		{name: "unknown update field", method: http.MethodPut, path: "/api/personalization/preferences", body: `{"style":"safe","secret":"x"}`, status: 422, want: `"code":"invalid_preferences"`},
		{name: "feedback summary is aggregate and explainable", method: http.MethodGet, path: "/api/personalization/feedback", status: 200, want: `"minimum_samples":3`},
		{name: "feedback submission", method: http.MethodPost, path: "/api/personalization/feedback", body: `{"client_feedback_id":"browser-1","decision_event_id":"match:decision:1","item_id":3001,"value":"useful"}`, status: 200, want: `"inserted":true`},
		{name: "feedback must name a visible choice", method: http.MethodPost, path: "/api/personalization/feedback", body: `{"client_feedback_id":"browser-2","decision_event_id":"match:decision:1","item_id":3002,"value":"useful"}`, status: 422, want: `"code":"feedback_not_eligible"`},
		{name: "export has no session identifiers", method: http.MethodGet, path: "/api/personalization/export", status: 200, want: `"schema_version":1`},
		{name: "feedback deletion needs exact confirmation", method: http.MethodDelete, path: "/api/personalization/feedback", body: `{"confirmation":"wrong"}`, status: 400, want: `"code":"invalid_confirmation"`},
		{name: "feedback deletion is separate", method: http.MethodDelete, path: "/api/personalization/feedback", body: `{"confirmation":"DELETE_PERSONALIZATION_FEEDBACK"}`, status: 200, want: `"deleted":2`},
		{name: "reset rejects null", method: http.MethodPost, path: "/api/personalization/preferences/reset", body: `null`, status: 422, want: `"code":"invalid_preferences"`},
		{name: "reset", method: http.MethodPost, path: "/api/personalization/preferences/reset", body: `{}`, status: 200, want: `"style":"balanced"`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, localRequest(test.method, test.path, strings.NewReader(test.body)))
			if response.Code != test.status || !strings.Contains(response.Body.String(), test.want) {
				t.Fatalf("response = %d %s", response.Code, response.Body.String())
			}
			if test.path == "/api/personalization/export" && (strings.Contains(response.Body.String(), "session_id") || strings.Contains(response.Body.String(), "decision_event_id")) {
				t.Fatalf("identity-linked fields leaked in export: %s", response.Body.String())
			}
		})
	}
	for _, path := range []string{"/api/dashboard", "/api/health"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, localRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"personalization"`) ||
			!strings.Contains(response.Body.String(), `"feedback_enabled":true`) {
			t.Fatalf("%s personalization status = %d %s", path, response.Code, response.Body.String())
		}
	}
}

func TestBrowserPersonalizationSerializationIsAcceptedByActualAPI(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is unavailable for the executable browser/API contract test")
	}
	command := exec.Command(node, "-e", `
		const state = require("../webui/assets/state.js");
		process.stdout.write(JSON.stringify(state.personalizationUpdatePayload({
			style: "safe", preferred_item_ids: [3002, 3001], avoided_item_ids: [3003],
			feedback_enabled: true, version: 99, updated_at: "not-an-api-field"
		})));
	`)
	payload, err := command.Output()
	if err != nil {
		t.Fatalf("execute browser serializer: %v", err)
	}
	service := &fakePersonalizationService{preferences: personalization.Preferences{Style: personalization.StyleBalanced, Version: 1}}
	handler := New(testStore(), Config{Personalization: service, Recommendations: staticRecommendationService{}})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, localRequest(http.MethodPut, "/api/personalization/preferences", bytes.NewReader(payload)))
	if response.Code != http.StatusOK {
		t.Fatalf("browser payload was rejected by actual API: %d %s payload=%s", response.Code, response.Body.String(), payload)
	}
	if service.preferences.Version != 2 || service.preferences.Style != personalization.StyleSafe ||
		!service.preferences.FeedbackEnabled || !reflect.DeepEqual(service.preferences.PreferredItemIDs, []int{3001, 3002}) ||
		!reflect.DeepEqual(service.preferences.AvoidedItemIDs, []int{3003}) {
		t.Fatalf("actual API received unexpected browser payload: %+v", service.preferences)
	}
}

func TestPregameEndpointsAreStrictAndIsolatedFromLiveState(t *testing.T) {
	store := testStore()
	live := staticRecommendationService{dashboard: recommendation.DashboardSnapshot{
		Epoch: "epoch-live", Revision: 19, Game: store.Current(),
		Recommendation: itemization.Result{Status: itemization.StatusReady},
	}}
	planner := &fakePregamePlanner{
		options: pregame.Options{Mode: pregame.ModeManualPregame, Patch: "16.18.1", Champions: []pregame.ChampionOption{{ID: "Garen", Name: "Garen"}}},
		plan: pregame.Plan{
			Mode: pregame.ModeManualPregame, Status: itemization.StatusInsufficientContext, Patch: "16.18.1",
			Provenance: pregame.Provenance{Rules: &itemrules.Status{Ready: true, Version: "garen-v2.16.18.1.1", Source: itemrules.SourceExternalExact}},
			Sources:    []string{"User-supplied scenario", "Data Dragon 16.18.1", "Profile generic-v1.16.18.1.3", "Garen v2 rules garen-v2.16.18.1.1 (external_exact)"},
		},
	}
	history := &countingMatchHistory{}
	handler := New(store, Config{Recommendations: live, MatchHistory: history, Pregame: planner})

	t.Run("catalog-backed options", func(t *testing.T) {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, localRequest(http.MethodGet, "/api/pregame/options", nil))
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"mode":"manual_pregame"`) || !strings.Contains(response.Body.String(), `"champions"`) {
			t.Fatalf("response = %d %s", response.Code, response.Body.String())
		}
	})

	validBody := `{"champion":"Garen","role":"top","stage":"first_recall","strategy":"balanced","pressures":{"physical":"medium"},"gold":1200,"inventory":[]}`
	t.Run("valid non-actionable scenario remains 200", func(t *testing.T) {
		before := live.Current()
		beforeGame := store.Current()
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, localRequest(http.MethodPost, "/api/pregame/plan", strings.NewReader(validBody)))
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"status":"insufficient_context"`) ||
			!strings.Contains(response.Body.String(), `"version":"garen-v2.16.18.1.1"`) ||
			!strings.Contains(response.Body.String(), `"source":"external_exact"`) ||
			!strings.Contains(response.Body.String(), `"Garen v2 rules garen-v2.16.18.1.1 (external_exact)"`) {
			t.Fatalf("response = %d %s", response.Code, response.Body.String())
		}
		after := live.Current()
		if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(beforeGame, store.Current()) {
			t.Fatalf("manual request changed live state: before=%#v after=%#v game=%#v", before, after, store.Current())
		}
		if history.calls != 0 {
			t.Fatalf("manual request touched history %d times", history.calls)
		}
	})

	t.Run("unknown field is rejected before planning", func(t *testing.T) {
		beforeCalls := planner.planCalls
		response := httptest.NewRecorder()
		body := strings.TrimSuffix(validBody, "}") + `,"kills":12}`
		handler.ServeHTTP(response, localRequest(http.MethodPost, "/api/pregame/plan", strings.NewReader(body)))
		if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), `"code":"invalid_scenario"`) {
			t.Fatalf("response = %d %s", response.Code, response.Body.String())
		}
		if planner.planCalls != beforeCalls {
			t.Fatal("planner received a body with unknown fields")
		}
	})

	t.Run("trailing JSON is rejected", func(t *testing.T) {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, localRequest(http.MethodPost, "/api/pregame/plan", strings.NewReader(validBody+` {}`)))
		if response.Code != http.StatusUnprocessableEntity {
			t.Fatalf("response = %d %s", response.Code, response.Body.String())
		}
	})

	t.Run("oversized body is rejected", func(t *testing.T) {
		response := httptest.NewRecorder()
		body := append([]byte(`{"champion":"`), bytes.Repeat([]byte("x"), 70<<10)...)
		body = append(body, []byte(`"}`)...)
		handler.ServeHTTP(response, localRequest(http.MethodPost, "/api/pregame/plan", bytes.NewReader(body)))
		if response.Code != http.StatusRequestEntityTooLarge || !strings.Contains(response.Body.String(), `"code":"scenario_too_large"`) {
			t.Fatalf("response = %d %s", response.Code, response.Body.String())
		}
	})

	t.Run("field-safe validation is 422", func(t *testing.T) {
		planner.planErr = &pregame.ValidationError{Fields: map[string]string{"gold": "El oro debe estar entre 0 y 100000."}}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, localRequest(http.MethodPost, "/api/pregame/plan", strings.NewReader(validBody)))
		planner.planErr = nil
		if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), `"fields":{"gold":"El oro debe estar entre 0 y 100000."}`) {
			t.Fatalf("response = %d %s", response.Code, response.Body.String())
		}
	})

	t.Run("unavailable dependency is 503", func(t *testing.T) {
		planner.planErr = pregame.ErrUnavailable
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, localRequest(http.MethodPost, "/api/pregame/plan", strings.NewReader(validBody)))
		planner.planErr = nil
		if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), `"code":"pregame_unavailable"`) {
			t.Fatalf("response = %d %s", response.Code, response.Body.String())
		}
	})
}

func TestIdentityFreeHistoryAndPrivacyEndpoints(t *testing.T) {
	now := time.Date(2026, 9, 19, 16, 0, 0, 0, time.UTC)
	service := &fakeHistoryService{
		page: domain.HistoryPage{Matches: []domain.MatchSummary{{
			SchemaVersion: 1, MatchID: "match-1", Digest: "abc123", ChampionName: "Garen",
			Completion: domain.SummaryCompleteTerminal, StartedAt: now,
			Outcome: domain.OutcomeSummary{Value: domain.MatchOutcomeUnknown, Source: "unavailable_locally"},
		}}, NextCursor: "next"},
		export: domain.HistoryExport{SchemaVersion: 1, Scope: "full", ExportedAt: now},
	}
	handler := New(testStore(), Config{History: service})

	t.Run("bounded keyset list is no-store", func(t *testing.T) {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, localRequest(http.MethodGet, "/api/history/matches?limit=999&before=cursor", nil))
		if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" || service.lastLimit != 100 || service.lastCursor != "cursor" {
			t.Fatalf("response = %d headers=%v body=%s service=%+v", response.Code, response.Header(), response.Body.String(), service)
		}
		if strings.Contains(response.Body.String(), "riot_id") || !strings.Contains(response.Body.String(), `"outcome":{"value":"unknown"`) {
			t.Fatalf("identity-free list = %s", response.Body.String())
		}
	})

	t.Run("detail has semantic digest ETag", func(t *testing.T) {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, localRequest(http.MethodGet, "/api/history/matches/match-1", nil))
		if response.Code != http.StatusOK || response.Header().Get("ETag") != `"abc123"` {
			t.Fatalf("response = %d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
		}
	})

	t.Run("safe export is attachment and no-store", func(t *testing.T) {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, localRequest(http.MethodGet, "/api/history/export?scope=full", nil))
		if response.Code != http.StatusOK || !strings.Contains(response.Header().Get("Content-Disposition"), "lol-engine-history-") || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("response = %d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
		}
	})

	t.Run("settling deletion is conflict", func(t *testing.T) {
		service.deleteErr = history.ErrHistorySettling
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, localRequest(http.MethodDelete, "/api/history/matches/match-1", strings.NewReader(`{"confirmation":"DELETE_LOCAL_MATCH_HISTORY"}`)))
		if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), `"code":"history_settling"`) {
			t.Fatalf("response = %d %s", response.Code, response.Body.String())
		}
		service.deleteErr = nil
	})

	t.Run("single deletion requires exact confirmation", func(t *testing.T) {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, localRequest(http.MethodDelete, "/api/history/matches/match-1", strings.NewReader(`{"confirmation":"wrong"}`)))
		if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"code":"invalid_confirmation"`) {
			t.Fatalf("response = %d %s", response.Code, response.Body.String())
		}
	})

	t.Run("single deletion accepts exact confirmation", func(t *testing.T) {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, localRequest(http.MethodDelete, "/api/history/matches/match-1", strings.NewReader(`{"confirmation":"DELETE_LOCAL_MATCH_HISTORY"}`)))
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"deleted":1`) {
			t.Fatalf("response = %d %s", response.Code, response.Body.String())
		}
	})

	t.Run("delete all requires exact confirmation", func(t *testing.T) {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, localRequest(http.MethodDelete, "/api/history/matches", strings.NewReader(`{"confirmation":"DELETE_LOCAL_HISTORY"}`)))
		if response.Code != http.StatusOK || service.lastConfirmation != history.DeleteAllConfirmation {
			t.Fatalf("response = %d %s confirmation=%q", response.Code, response.Body.String(), service.lastConfirmation)
		}
	})

	t.Run("delete all source change is conflict", func(t *testing.T) {
		service.deleteAllErr = storage.ErrHistoryDeleteChanged
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, localRequest(http.MethodDelete, "/api/history/matches", strings.NewReader(`{"confirmation":"DELETE_LOCAL_HISTORY"}`)))
		if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), `"code":"history_settling"`) {
			t.Fatalf("response = %d %s", response.Code, response.Body.String())
		}
		service.deleteAllErr = nil
	})

	t.Run("privacy preferences are typed", func(t *testing.T) {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, localRequest(http.MethodPut, "/api/privacy/history", strings.NewReader(`{"retention_days":30,"max_completed_matches":50}`)))
		if response.Code != http.StatusOK || service.preferences.RetentionDays == nil || *service.preferences.RetentionDays != 30 {
			t.Fatalf("response = %d %s preferences=%+v", response.Code, response.Body.String(), service.preferences)
		}
	})
}

func TestJSONEndpoints(t *testing.T) {
	store := testStore()
	handler := New(store, Config{})

	t.Run("health", func(t *testing.T) {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, localRequest(http.MethodGet, "/api/health", nil))
		if response.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", response.Code)
		}
		if contentType := response.Header().Get("Content-Type"); contentType != "application/json; charset=utf-8" {
			t.Fatalf("Content-Type = %q", contentType)
		}
		var body struct {
			Status        string               `json:"status"`
			TrackerStatus domain.TrackerStatus `json:"tracker_status"`
			Revision      uint64               `json:"revision"`
		}
		if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if body.Status != "ok" || body.TrackerStatus != domain.StatusInGame || body.Revision != 1 {
			t.Fatalf("health body = %#v", body)
		}
	})

	t.Run("game", func(t *testing.T) {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, localRequest(http.MethodGet, "/api/game", nil))
		if response.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", response.Code)
		}
		var snapshot domain.Snapshot
		if err := json.NewDecoder(response.Body).Decode(&snapshot); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if snapshot.Game == nil || snapshot.Game.ActivePlayer.ChampionName != "Garen" {
			t.Fatalf("game body = %#v", snapshot)
		}
	})

	t.Run("recommendation", func(t *testing.T) {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, localRequest(http.MethodGet, "/api/recommendation", nil))
		if response.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", response.Code)
		}
		var result itemization.Result
		if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if result.Status != itemization.StatusCatalogUnavailable {
			t.Fatalf("status = %q", result.Status)
		}
	})

	t.Run("dashboard", func(t *testing.T) {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, localRequest(http.MethodGet, "/api/dashboard", nil))
		if response.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", response.Code)
		}
		var dashboard recommendation.DashboardSnapshot
		if err := json.NewDecoder(response.Body).Decode(&dashboard); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if dashboard.Game.Status != domain.StatusInGame || dashboard.Recommendation.Status != itemization.StatusCatalogUnavailable {
			t.Fatalf("dashboard = %#v", dashboard)
		}
	})

	t.Run("recalculate", func(t *testing.T) {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, localRequest(http.MethodPost, "/api/recommendation/recalculate", nil))
		if response.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", response.Code)
		}
		var body itemization.Result
		if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if body.Status != itemization.StatusCatalogUnavailable {
			t.Fatalf("recommendation status = %q", body.Status)
		}
	})

	t.Run("not found", func(t *testing.T) {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, localRequest(http.MethodGet, "/api/unknown", nil))
		if response.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", response.Code)
		}
		if contentType := response.Header().Get("Content-Type"); contentType != "application/json; charset=utf-8" {
			t.Fatalf("Content-Type = %q, want JSON API error", contentType)
		}
		if !strings.Contains(response.Body.String(), `"code":"not_found"`) {
			t.Fatalf("body = %s, want stable not_found error", response.Body.String())
		}
	})
}

func TestRiotEnrichmentAndStatisticsEndpointsAreSafeAndExplicit(t *testing.T) {
	now := time.Date(2026, 9, 19, 22, 0, 0, 0, time.UTC)
	riot := &fakeRiotSyncService{status: matchsync.Status{
		Mode: riotapi.ModeDirect, Enabled: true, Configured: true, Region: riotapi.RegionAmericas,
		Reconciliations: []storage.RiotReconciliationState{{AccountID: "account-1", Status: storage.RiotReconciliationPending, FilterKey: "opaque-filter", RunStartedAt: now, WindowEndUnix: now.Unix(), LastErrorKind: "storage", UpdatedAt: now}},
	}}
	stats := &fakeStatisticsService{report: statistics.Report{TotalGames: 2, MinimumSample: 10, ObservationalNotice: "Observed only"}}
	handler := New(testStore(), Config{RiotSync: riot, Statistics: stats})

	t.Run("status never exposes key or PUUID", func(t *testing.T) {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, localRequest(http.MethodGet, "/api/riot/status", nil))
		if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "api_key") || strings.Contains(response.Body.String(), "puuid") || !strings.Contains(response.Body.String(), `"reconciliations":[{"account_id":"account-1","status":"pending"`) {
			t.Fatalf("response = %d %s", response.Code, response.Body.String())
		}
	})

	t.Run("dashboard status is additive and secret free", func(t *testing.T) {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, localRequest(http.MethodGet, "/api/dashboard", nil))
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"riot":{"mode":"direct"`) || strings.Contains(response.Body.String(), "api_key") || strings.Contains(response.Body.String(), "puuid") {
			t.Fatalf("response = %d %s", response.Code, response.Body.String())
		}
	})

	t.Run("link requires explicit consent and contains no credential field", func(t *testing.T) {
		riot.linked = matchsync.LinkedAccount{ID: "account-1", Region: riotapi.RegionAmericas, Platform: riotapi.PlatformNA1, LinkedAt: now}
		response := httptest.NewRecorder()
		body := `{"game_name":"José","tag_line":"LAS","platform":"NA1","consent":true}`
		handler.ServeHTTP(response, localRequest(http.MethodPost, "/api/riot/accounts", strings.NewReader(body)))
		if response.Code != http.StatusCreated || !riot.lastLink.Consent || strings.Contains(response.Body.String(), "José") || strings.Contains(response.Body.String(), "puuid") {
			t.Fatalf("response = %d %s request=%#v", response.Code, response.Body.String(), riot.lastLink)
		}
	})

	t.Run("link endpoint rejects browser supplied API key", func(t *testing.T) {
		response := httptest.NewRecorder()
		body := `{"game_name":"José","tag_line":"LAS","platform":"NA1","consent":true,"api_key":"forbidden"}`
		handler.ServeHTTP(response, localRequest(http.MethodPost, "/api/riot/accounts", strings.NewReader(body)))
		if response.Code != http.StatusBadRequest || strings.Contains(response.Body.String(), "forbidden") {
			t.Fatalf("response = %d %s", response.Code, response.Body.String())
		}
	})

	t.Run("sync exposes only typed error", func(t *testing.T) {
		riot.syncResult = matchsync.SyncResult{AccountID: "account-1", Status: storage.RiotSyncPartial, ErrorKind: string(riotapi.ErrorServer)}
		riot.syncErr = errors.New("upstream body secret-token")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, localRequest(http.MethodPost, "/api/riot/sync", strings.NewReader(`{"account_id":"account-1"}`)))
		if response.Code != http.StatusBadGateway || strings.Contains(response.Body.String(), "secret-token") || !strings.Contains(response.Body.String(), `"error_kind":"server_error"`) {
			t.Fatalf("response = %d %s", response.Code, response.Body.String())
		}
		riot.syncErr = nil
	})

	t.Run("completed ingestion with pending reconciliation is accepted", func(t *testing.T) {
		riot.syncResult = matchsync.SyncResult{
			AccountID: "account-1", Status: storage.RiotSyncComplete,
			ReconciliationStatus: storage.RiotReconciliationPending, ReconciliationErrorKind: "storage",
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, localRequest(http.MethodPost, "/api/riot/sync", strings.NewReader(`{"account_id":"account-1"}`)))
		if response.Code != http.StatusAccepted || !strings.Contains(response.Body.String(), `"status":"complete"`) || !strings.Contains(response.Body.String(), `"reconciliation_status":"pending"`) || strings.Contains(response.Body.String(), "can be resumed") {
			t.Fatalf("response = %d %s", response.Code, response.Body.String())
		}
	})

	t.Run("statistics is bounded and observational", func(t *testing.T) {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, localRequest(http.MethodGet, "/api/history/statistics?account_id=account-1&patch=16.18&champion=Garen&role=top&minimum_sample=999", nil))
		if response.Code != http.StatusOK || stats.query.MinimumSample != 100 || response.Header().Get("Cache-Control") != "no-store" || !strings.Contains(response.Body.String(), "Observed only") {
			t.Fatalf("response = %d %s query=%#v", response.Code, response.Body.String(), stats.query)
		}
	})

	t.Run("delete requires exact confirmation", func(t *testing.T) {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, localRequest(http.MethodDelete, "/api/riot/accounts/account-1", strings.NewReader(`{"confirmation":"DELETE_RIOT_ACCOUNT_DATA"}`)))
		if response.Code != http.StatusOK || riot.deletedID != "account-1" || riot.confirmation != matchsync.DeleteConfirmation {
			t.Fatalf("response = %d %s", response.Code, response.Body.String())
		}
	})
}

type fakeRiotSyncService struct {
	status       matchsync.Status
	linked       matchsync.LinkedAccount
	lastLink     matchsync.LinkRequest
	syncResult   matchsync.SyncResult
	syncErr      error
	deletedID    string
	confirmation string
}

func (service *fakeRiotSyncService) Status(context.Context) (matchsync.Status, error) {
	return service.status, nil
}
func (service *fakeRiotSyncService) Link(_ context.Context, request matchsync.LinkRequest) (matchsync.LinkedAccount, error) {
	service.lastLink = request
	return service.linked, nil
}
func (service *fakeRiotSyncService) Sync(_ context.Context, accountID string, filter matchsync.Filter) (matchsync.SyncResult, error) {
	return service.syncResult, service.syncErr
}
func (service *fakeRiotSyncService) Delete(_ context.Context, accountID, confirmation string) error {
	service.deletedID, service.confirmation = accountID, confirmation
	return nil
}

type fakeStatisticsService struct {
	report statistics.Report
	query  statistics.Query
}

func (service *fakeStatisticsService) Query(_ context.Context, query statistics.Query) (statistics.Report, error) {
	service.query = query
	return service.report, nil
}

func TestHealthAndDashboardExposeRuleProvenance(t *testing.T) {
	store := testStore()
	dashboard := recommendation.DashboardSnapshot{
		Revision: 3,
		Game:     store.Current(),
		Recommendation: itemization.Result{
			Status: itemization.StatusReady,
			Rules:  itemrules.Status{Ready: true, Version: "garen-v2.16.18.1.1", Patch: "16.18.1", Source: itemrules.SourceExternalExact, LastError: "previous reload failed"},
			Phase:  domain.PhaseMid, Strategy: domain.StrategyTeamfight,
		},
	}
	handler := New(store, Config{Recommendations: staticRecommendationService{dashboard: dashboard}})
	health := httptest.NewRecorder()
	handler.ServeHTTP(health, localRequest(http.MethodGet, "/api/health", nil))
	var healthBody struct {
		RulesReady   bool            `json:"rules_ready"`
		RulesVersion string          `json:"rules_version"`
		RulesSource  string          `json:"rules_source"`
		RulesError   string          `json:"rules_last_error"`
		Phase        domain.Phase    `json:"phase"`
		Strategy     domain.Strategy `json:"strategy"`
	}
	if err := json.NewDecoder(health.Body).Decode(&healthBody); err != nil {
		t.Fatal(err)
	}
	if !healthBody.RulesReady || healthBody.RulesVersion != "garen-v2.16.18.1.1" || healthBody.RulesSource != itemrules.SourceExternalExact || healthBody.RulesError != "previous reload failed" || healthBody.Phase != domain.PhaseMid || healthBody.Strategy != domain.StrategyTeamfight {
		t.Fatalf("health rule provenance = %#v", healthBody)
	}

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, localRequest(http.MethodGet, "/api/dashboard", nil))
	var got recommendation.DashboardSnapshot
	if err := json.NewDecoder(response.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Recommendation.Rules.Version != "garen-v2.16.18.1.1" || got.Recommendation.Strategy != domain.StrategyTeamfight {
		t.Fatalf("dashboard rule provenance = %#v", got.Recommendation)
	}
}

func TestHealthAndDashboardExposeGenericProfileProvenance(t *testing.T) {
	store := testStore()
	profileStatus := itemprofiles.Status{Ready: true, Version: "generic-v1.16.18.1.1", Patch: "16.18.1", Source: itemprofiles.SourceExternalPatchFallback, Fallback: true, LastError: "newer file invalid"}
	profile := itemprofiles.Provenance{Level: itemprofiles.KindRoleArchetype, ProfileID: "middle_archetype", RulesetVersion: profileStatus.Version, Patch: profileStatus.Patch, Fallback: true, ConfidenceCap: itemprofiles.ConfidenceRoleArchetype}
	dashboard := recommendation.DashboardSnapshot{
		Revision: 5,
		Game:     store.Current(),
		Recommendation: itemization.Result{
			Status:        itemization.StatusReady,
			ProfileStatus: &profileStatus,
			Profile:       &profile,
			Advisories:    []itemprofiles.Warning{{Code: itemprofiles.WarningRoleArchetype, Message: "fallback"}},
		},
	}
	handler := New(store, Config{Recommendations: staticRecommendationService{dashboard: dashboard}, GenericProfilesEnabled: true})
	health := httptest.NewRecorder()
	handler.ServeHTTP(health, localRequest(http.MethodGet, "/api/health", nil))
	var healthBody struct {
		ProfileReady      bool              `json:"profile_ready"`
		ProfileVersion    string            `json:"profile_version"`
		ProfileSource     string            `json:"profile_source"`
		ProfileFallback   bool              `json:"profile_fallback"`
		ProfileError      string            `json:"profile_last_error"`
		ProfileLevel      itemprofiles.Kind `json:"profile_level"`
		ProfileID         string            `json:"profile_id"`
		ProfileConfidence float64           `json:"profile_confidence_cap"`
		GenericEnabled    bool              `json:"generic_profiles_enabled"`
	}
	if err := json.NewDecoder(health.Body).Decode(&healthBody); err != nil {
		t.Fatal(err)
	}
	if !healthBody.GenericEnabled || !healthBody.ProfileReady || healthBody.ProfileVersion != profileStatus.Version || healthBody.ProfileSource != profileStatus.Source || !healthBody.ProfileFallback || healthBody.ProfileError != profileStatus.LastError || healthBody.ProfileLevel != profile.Level || healthBody.ProfileID != profile.ProfileID || healthBody.ProfileConfidence != profile.ConfidenceCap {
		t.Fatalf("health profile provenance = %#v", healthBody)
	}

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, localRequest(http.MethodGet, "/api/dashboard", nil))
	var got recommendation.DashboardSnapshot
	if err := json.NewDecoder(response.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Recommendation.Profile == nil || got.Recommendation.Profile.ProfileID != profile.ProfileID || len(got.Recommendation.Advisories) != 1 {
		t.Fatalf("dashboard profile provenance = %#v", got.Recommendation)
	}
}

func TestDashboardPreservesCompleteRankedChoiceSet(t *testing.T) {
	store := testStore()
	ranked := []itemization.RankedItem{
		{Item: itemization.ItemRef{ID: 4401, Name: "Primary", Cost: 2800}, Score: 1000},
		{Item: itemization.ItemRef{ID: 2504, Name: "Alternative one", Cost: 2900}, Score: 900},
		{Item: itemization.ItemRef{ID: 3111, Name: "Alternative two", Cost: 1250}, Score: 800},
		{Item: itemization.ItemRef{ID: 6631, Name: "Alternative three", Cost: 3300}, Score: 700},
	}
	dashboard := recommendation.DashboardSnapshot{
		Revision: 4,
		Game:     store.Current(),
		Recommendation: itemization.Result{
			Status:        itemization.StatusReady,
			TargetItem:    &ranked[0].Item,
			Alternative:   &ranked[1].Item,
			Alternatives:  append([]itemization.RankedItem(nil), ranked[1:]...),
			RankedOptions: append([]itemization.RankedItem(nil), ranked...),
		},
	}
	handler := New(store, Config{Recommendations: staticRecommendationService{dashboard: dashboard}})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, localRequest(http.MethodGet, "/api/dashboard", nil))
	var got recommendation.DashboardSnapshot
	if err := json.NewDecoder(response.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got.Recommendation.RankedOptions) != 4 || len(got.Recommendation.Alternatives) != 3 {
		t.Fatalf("ranked choice set = %#v", got.Recommendation)
	}
	for index, option := range got.Recommendation.RankedOptions {
		if option.Item.ID != ranked[index].Item.ID {
			t.Fatalf("ranked option %d ID = %d, want %d", index, option.Item.ID, ranked[index].Item.ID)
		}
	}
	if got.Recommendation.Alternative == nil || got.Recommendation.Alternative.ID != ranked[1].Item.ID {
		t.Fatalf("compatibility alternative = %#v", got.Recommendation.Alternative)
	}
}

func TestHealthAndDashboardExposeLatestDecisionEventStatus(t *testing.T) {
	latest := domain.DecisionEvent{
		ID: "match:decision:000003", SessionID: "match", Sequence: 3,
		Kind: domain.DecisionReturnToBaseOptions, Importance: domain.DecisionImportant,
		OccurredAt:     time.Date(2026, 9, 19, 10, 30, 0, 0, time.UTC),
		CurrentSummary: "Hay opciones actualizadas disponibles al volver a base.",
	}
	dashboard := recommendation.DashboardSnapshot{
		Revision: 9, Game: testStore().Current(),
		DecisionEvents: domain.DecisionEventAvailability{
			Available: true, Status: domain.DecisionEventStatusReady, Latest: &latest,
		},
	}
	handler := New(testStore(), Config{Recommendations: staticRecommendationService{dashboard: dashboard}})

	health := httptest.NewRecorder()
	handler.ServeHTTP(health, localRequest(http.MethodGet, "/api/health", nil))
	var healthBody struct {
		Available bool                  `json:"decision_events_available"`
		Status    string                `json:"decision_events_status"`
		Latest    *domain.DecisionEvent `json:"latest_decision_event"`
	}
	if err := json.NewDecoder(health.Body).Decode(&healthBody); err != nil {
		t.Fatal(err)
	}
	if !healthBody.Available || healthBody.Status != domain.DecisionEventStatusReady || healthBody.Latest == nil || healthBody.Latest.ID != latest.ID {
		t.Fatalf("health decision status = %#v", healthBody)
	}

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, localRequest(http.MethodGet, "/api/dashboard", nil))
	var got recommendation.DashboardSnapshot
	if err := json.NewDecoder(response.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.DecisionEvents.Latest == nil || got.DecisionEvents.Latest.ID != latest.ID {
		t.Fatalf("dashboard decision status = %#v", got.DecisionEvents)
	}
}

func TestMatchHistoryEndpointsAreBoundedAndOrdered(t *testing.T) {
	started := time.Date(2026, 9, 19, 1, 0, 0, 0, time.UTC)
	history := &fakeMatchHistory{
		matches: []domain.MatchSession{{ID: "match-2", State: domain.MatchEnded, StartedAt: started.Add(time.Hour)}, {ID: "match-1", State: domain.MatchEnded, StartedAt: started}},
		events: map[string][]domain.SemanticEvent{
			"match-2": {{ID: "match-2:000001", SessionID: "match-2", Sequence: 1, Kind: domain.EventMatchStarted}},
		},
	}
	handler := New(testStore(), Config{MatchHistory: history})

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, localRequest(http.MethodGet, "/api/matches?limit=999&offset=0", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("matches status = %d, body=%s", response.Code, response.Body.String())
	}
	var matches struct {
		Matches []domain.MatchSession `json:"matches"`
		Limit   int                   `json:"limit"`
		Offset  int                   `json:"offset"`
	}
	if err := json.NewDecoder(response.Body).Decode(&matches); err != nil {
		t.Fatal(err)
	}
	if matches.Limit != 100 || len(matches.Matches) != 2 || matches.Matches[0].ID != "match-2" || history.lastLimit != 100 {
		t.Fatalf("matches response = %#v, requested limit=%d", matches, history.lastLimit)
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, localRequest(http.MethodGet, "/api/matches/match-2/events?after=0&limit=10", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("events status = %d, body=%s", response.Code, response.Body.String())
	}
	var events struct {
		Events []domain.SemanticEvent `json:"events"`
		After  uint64                 `json:"after"`
	}
	if err := json.NewDecoder(response.Body).Decode(&events); err != nil {
		t.Fatal(err)
	}
	if len(events.Events) != 1 || events.Events[0].Sequence != 1 {
		t.Fatalf("events response = %#v", events)
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, localRequest(http.MethodGet, "/api/matches?limit=bad", nil))
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"code":"invalid_pagination"`) {
		t.Fatalf("invalid pagination response = %d %s", response.Code, response.Body.String())
	}
}

func TestDecisionHistoryEndpointIsAuthoritativeBoundedAndOrdered(t *testing.T) {
	history := &fakeMatchHistory{decisionEvents: map[string][]domain.DecisionEvent{
		"match-2": {
			{ID: "match-2:decision:000001", SessionID: "match-2", Sequence: 1, Kind: domain.DecisionTargetChanged},
			{ID: "match-2:decision:000002", SessionID: "match-2", Sequence: 2, Kind: domain.DecisionComponentAffordable},
			{ID: "match-2:decision:000003", SessionID: "match-2", Sequence: 3, Kind: domain.DecisionReturnToBaseOptions},
		},
	}}
	handler := New(testStore(), Config{MatchHistory: history})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, localRequest(http.MethodGet, "/api/matches/match-2/decision-events?after=1&limit=1", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
	}
	var body struct {
		MatchID       string                 `json:"match_id"`
		Events        []domain.DecisionEvent `json:"events"`
		After         uint64                 `json:"after"`
		Limit         int                    `json:"limit"`
		Authoritative bool                   `json:"authoritative"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if !body.Authoritative || body.MatchID != "match-2" || body.After != 1 || body.Limit != 1 ||
		len(body.Events) != 1 || body.Events[0].Sequence != 2 {
		t.Fatalf("decision history response = %#v", body)
	}
}

func TestEmbeddedFrontendRoutes(t *testing.T) {
	handler := New(testStore(), Config{})
	tests := []struct {
		name        string
		path        string
		contentType string
		cache       string
		contains    string
	}{
		{name: "root", path: "/", contentType: "text/html; charset=utf-8", cache: "no-store", contains: "LoL Engine"},
		{name: "css", path: "/assets/app.css", contentType: "text/css; charset=utf-8", cache: "no-cache", contains: "--accent"},
		{name: "javascript", path: "/assets/app.js", contentType: "text/javascript; charset=utf-8", cache: "no-cache", contains: "/api/dashboard"},
		{name: "favicon", path: "/favicon.svg", contentType: "image/svg+xml", cache: "no-cache", contains: "<svg"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, localRequest(http.MethodGet, test.path, nil))
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body=%s", response.Code, response.Body.String())
			}
			if got := response.Header().Get("Content-Type"); got != test.contentType {
				t.Fatalf("Content-Type = %q, want %q", got, test.contentType)
			}
			if got := response.Header().Get("Cache-Control"); got != test.cache {
				t.Fatalf("Cache-Control = %q, want %q", got, test.cache)
			}
			if !strings.Contains(response.Body.String(), test.contains) {
				t.Fatalf("body for %s does not contain %q", test.path, test.contains)
			}
		})
	}

	t.Run("API namespace is not shadowed", func(t *testing.T) {
		for _, path := range []string{"/api", "/api/not-a-real-endpoint"} {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, localRequest(http.MethodGet, path, nil))
			if response.Code != http.StatusNotFound {
				t.Fatalf("%s status = %d, want 404", path, response.Code)
			}
			if got := response.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
				t.Fatalf("%s Content-Type = %q, want JSON", path, got)
			}
		}
	})
}

func TestEmbeddedFrontendPreservesAuthoritySecurity(t *testing.T) {
	handler := New(testStore(), Config{})
	tests := []struct {
		name       string
		host       string
		origin     string
		wantStatus int
		wantCode   string
	}{
		{name: "same origin", host: "127.0.0.1:8080", origin: "http://127.0.0.1:8080", wantStatus: http.StatusOK},
		{name: "external host", host: "engine.evil.example:8080", wantStatus: http.StatusForbidden, wantCode: "forbidden_host"},
		{name: "external origin", host: "127.0.0.1:8080", origin: "https://evil.example", wantStatus: http.StatusForbidden, wantCode: "forbidden_origin"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/", nil)
			request.Host = test.host
			if test.origin != "" {
				request.Header.Set("Origin", test.origin)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", response.Code, test.wantStatus, response.Body.String())
			}
			if test.wantCode != "" && !strings.Contains(response.Body.String(), `"code":"`+test.wantCode+`"`) {
				t.Fatalf("body = %s, want error code %q", response.Body.String(), test.wantCode)
			}
		})
	}
}

func TestMethodValidation(t *testing.T) {
	handler := New(testStore(), Config{})
	tests := []struct {
		name   string
		method string
		path   string
		allow  string
	}{
		{name: "health rejects post", method: http.MethodPost, path: "/api/health", allow: http.MethodGet},
		{name: "game rejects delete", method: http.MethodDelete, path: "/api/game", allow: http.MethodGet},
		{name: "events rejects post", method: http.MethodPost, path: "/api/events", allow: http.MethodGet},
		{name: "recommendation rejects post", method: http.MethodPost, path: "/api/recommendation", allow: http.MethodGet},
		{name: "dashboard rejects post", method: http.MethodPost, path: "/api/dashboard", allow: http.MethodGet},
		{name: "recalculate rejects get", method: http.MethodGet, path: "/api/recommendation/recalculate", allow: http.MethodPost},
		{name: "pregame options rejects post", method: http.MethodPost, path: "/api/pregame/options", allow: http.MethodGet},
		{name: "pregame plan rejects get", method: http.MethodGet, path: "/api/pregame/plan", allow: http.MethodPost},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, localRequest(test.method, test.path, nil))
			if response.Code != http.StatusMethodNotAllowed {
				t.Fatalf("status = %d, want 405", response.Code)
			}
			if allow := response.Header().Get("Allow"); allow != test.allow {
				t.Fatalf("Allow = %q, want %q", allow, test.allow)
			}
		})
	}

}

func TestRejectsUntrustedHostAndOrigin(t *testing.T) {
	handler := New(testStore(), Config{})
	tests := []struct {
		name       string
		method     string
		host       string
		origin     string
		wantStatus int
		wantCode   string
	}{
		{name: "IPv4 curl", method: http.MethodGet, host: "127.0.0.1:8080", wantStatus: http.StatusOK},
		{name: "localhost browser", method: http.MethodGet, host: "localhost:8080", origin: "http://localhost:8080", wantStatus: http.StatusOK},
		{name: "IPv6 browser", method: http.MethodGet, host: "[::1]:8080", origin: "http://[::1]:8080", wantStatus: http.StatusOK},
		{name: "external host", method: http.MethodGet, host: "example.com:8080", wantStatus: http.StatusForbidden, wantCode: "forbidden_host"},
		{name: "lookalike host", method: http.MethodGet, host: "attacker.localhost:8080", wantStatus: http.StatusForbidden, wantCode: "forbidden_host"},
		{name: "external origin", method: http.MethodGet, host: "127.0.0.1:8080", origin: "https://evil.example", wantStatus: http.StatusForbidden, wantCode: "forbidden_origin"},
		{name: "different loopback origin", method: http.MethodPost, host: "127.0.0.1:8080", origin: "http://127.0.0.1:3000", wantStatus: http.StatusForbidden, wantCode: "forbidden_origin"},
		{name: "null origin", method: http.MethodPost, host: "127.0.0.1:8080", origin: "null", wantStatus: http.StatusForbidden, wantCode: "forbidden_origin"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, "/api/health", nil)
			request.Host = test.host
			if test.origin != "" {
				request.Header.Set("Origin", test.origin)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", response.Code, test.wantStatus, response.Body.String())
			}
			if test.wantCode != "" && !strings.Contains(response.Body.String(), `"code":"`+test.wantCode+`"`) {
				t.Fatalf("body = %s, want error code %q", response.Body.String(), test.wantCode)
			}
		})
	}

	t.Run("multiple origins", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodGet, "/api/health", nil)
		request.Host = "127.0.0.1:8080"
		request.Header.Add("Origin", "http://127.0.0.1:8080")
		request.Header.Add("Origin", "http://localhost:8080")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), `"code":"forbidden_origin"`) {
			t.Fatalf("response = %d %s", response.Code, response.Body.String())
		}
	})
}

func TestEventsWritesInitialSnapshot(t *testing.T) {
	handler := New(testStore(), Config{SSEKeepAlive: time.Second})
	request := localRequest(http.MethodGet, "/api/events", nil)
	ctx, cancel := context.WithCancel(request.Context())
	cancel()
	request = request.WithContext(ctx)
	response := newDeadlineRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.Code)
	}
	if contentType := response.Header().Get("Content-Type"); contentType != "text/event-stream" {
		t.Fatalf("Content-Type = %q", contentType)
	}
	if !strings.Contains(response.Body.String(), "event: dashboard_snapshot") {
		t.Fatalf("SSE body missing event name: %q", response.Body.String())
	}

	var dataLine string
	scanner := bufio.NewScanner(strings.NewReader(response.Body.String()))
	for scanner.Scan() {
		if strings.HasPrefix(scanner.Text(), "data: ") {
			dataLine = strings.TrimPrefix(scanner.Text(), "data: ")
			break
		}
	}
	if dataLine == "" {
		t.Fatalf("SSE body missing data: %q", response.Body.String())
	}
	var snapshot recommendation.DashboardSnapshot
	if err := json.Unmarshal([]byte(dataLine), &snapshot); err != nil {
		t.Fatalf("decode SSE snapshot: %v", err)
	}
	if snapshot.Game.Status != domain.StatusInGame || snapshot.Game.Game == nil {
		t.Fatalf("SSE snapshot = %#v", snapshot)
	}
}

func TestEventsAddsDecisionNotificationWithoutChangingDashboardEvent(t *testing.T) {
	applicationContext, cancel := context.WithCancel(context.Background())
	service := &decisionStreamingService{
		staticRecommendationService: staticRecommendationService{dashboard: recommendation.DashboardSnapshot{
			Epoch: "server-epoch-01", Revision: 7, Game: testStore().Current(),
		}},
		event: domain.DecisionEvent{
			ID: "match:decision:000001", SessionID: "match", Sequence: 1,
			Kind: domain.DecisionTargetChanged, Importance: domain.DecisionImportant,
			OccurredAt: time.Date(2026, 9, 19, 10, 0, 0, 0, time.UTC), CurrentSummary: "Las opciones disponibles cambiaron.",
		},
	}
	handler := New(testStore(), Config{
		SSEKeepAlive: time.Hour, ApplicationContext: applicationContext, Recommendations: service,
	})
	request := localRequest(http.MethodGet, "/api/events", nil)
	response := newDeadlineRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.ServeHTTP(response, request)
	}()
	time.Sleep(30 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("SSE handler did not stop")
	}
	body := response.Body.String()
	if !strings.Contains(body, "event: dashboard_snapshot") || !strings.Contains(body, "id: server-epoch-01:7") ||
		!strings.Contains(body, `"epoch":"server-epoch-01"`) || !strings.Contains(body, "event: decision_notification") ||
		!strings.Contains(body, `"id":"match:decision:000001"`) {
		t.Fatalf("SSE stream = %q", body)
	}
}

func TestEventsStopsWhenApplicationShutsDown(t *testing.T) {
	applicationContext, cancelApplication := context.WithCancel(context.Background())
	handler := New(testStore(), Config{
		SSEKeepAlive:       time.Hour,
		ApplicationContext: applicationContext,
	})
	request := localRequest(http.MethodGet, "/api/events", nil)
	response := newDeadlineRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.ServeHTTP(response, request)
	}()

	cancelApplication()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("SSE handler did not stop after application cancellation")
	}
	if !strings.Contains(response.Body.String(), "event: dashboard_snapshot") {
		t.Fatalf("SSE body missing initial event: %q", response.Body.String())
	}
}

func TestEventsBoundsBlockedWrites(t *testing.T) {
	const writeTimeout = 20 * time.Millisecond
	handler := New(testStore(), Config{
		SSEKeepAlive:    time.Hour,
		SSEWriteTimeout: writeTimeout,
	})
	request := localRequest(http.MethodGet, "/api/events", nil)
	response := newBlockingDeadlineWriter()
	done := make(chan struct{})
	started := time.Now()
	go func() {
		defer close(done)
		handler.ServeHTTP(response, request)
	}()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("SSE handler remained blocked after its write deadline")
	}
	if elapsed := time.Since(started); elapsed < writeTimeout {
		t.Fatalf("handler returned before simulated deadline: %v", elapsed)
	}
	nonzeroDeadlines, clearedDeadlines := response.deadlineCounts()
	if nonzeroDeadlines == 0 {
		t.Fatal("SSE handler did not set a bounded write deadline")
	}
	if clearedDeadlines < 2 {
		t.Fatalf("cleared deadlines = %d, want capability check and post-write clear", clearedDeadlines)
	}
}

type deadlineRecorder struct {
	*httptest.ResponseRecorder
}

func newDeadlineRecorder() *deadlineRecorder {
	return &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
}

func (recorder *deadlineRecorder) SetWriteDeadline(time.Time) error {
	return nil
}

type blockingDeadlineWriter struct {
	mu               sync.Mutex
	header           http.Header
	deadline         time.Time
	nonzeroDeadlines int
	clearedDeadlines int
}

func newBlockingDeadlineWriter() *blockingDeadlineWriter {
	return &blockingDeadlineWriter{header: make(http.Header)}
}

func (writer *blockingDeadlineWriter) Header() http.Header {
	return writer.header
}

func (writer *blockingDeadlineWriter) WriteHeader(int) {}

func (writer *blockingDeadlineWriter) Write([]byte) (int, error) {
	writer.mu.Lock()
	deadline := writer.deadline
	writer.mu.Unlock()
	if deadline.IsZero() {
		return 0, context.DeadlineExceeded
	}
	delay := time.Until(deadline)
	if delay > 0 {
		timer := time.NewTimer(delay)
		<-timer.C
	}
	return 0, context.DeadlineExceeded
}

func (writer *blockingDeadlineWriter) Flush() {}

func (writer *blockingDeadlineWriter) SetWriteDeadline(deadline time.Time) error {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	writer.deadline = deadline
	if deadline.IsZero() {
		writer.clearedDeadlines++
	} else {
		writer.nonzeroDeadlines++
	}
	return nil
}

func (writer *blockingDeadlineWriter) deadlineCounts() (int, int) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	return writer.nonzeroDeadlines, writer.clearedDeadlines
}

func testStore() *state.Store {
	game := domain.GameState{
		GameMode:     "CLASSIC",
		MapNumber:    11,
		ActivePlayer: domain.ActivePlayer{ChampionName: "Garen"},
	}
	return state.NewStore(domain.Snapshot{
		Status:    domain.StatusInGame,
		Game:      &game,
		UpdatedAt: time.Date(2026, time.September, 18, 12, 0, 0, 0, time.UTC),
	})
}

func localRequest(method, target string, body io.Reader) *http.Request {
	request := httptest.NewRequest(method, target, body)
	request.Host = "127.0.0.1:8080"
	return request
}

type fakePersonalizationService struct {
	preferences personalization.Preferences
	summary     personalization.FeedbackSummary
}

func (service *fakePersonalizationService) Current() personalization.Snapshot {
	return personalization.Snapshot{Preferences: service.preferences}
}
func (service *fakePersonalizationService) Options() (personalization.Options, error) {
	return personalization.Options{Patch: "16.18.1", Items: []personalization.ItemOption{{ID: 3001, Name: "Uno"}}}, nil
}
func (service *fakePersonalizationService) Update(_ context.Context, update personalization.PreferenceUpdate) (personalization.Preferences, error) {
	service.preferences = personalization.Preferences{Style: update.Style, PreferredItemIDs: update.PreferredItemIDs, AvoidedItemIDs: update.AvoidedItemIDs, FeedbackEnabled: update.FeedbackEnabled, Version: service.preferences.Version + 1}
	return service.preferences, nil
}
func (service *fakePersonalizationService) Reset(context.Context) (personalization.Preferences, error) {
	service.preferences = personalization.Preferences{Style: personalization.StyleBalanced, Version: service.preferences.Version + 1}
	return service.preferences, nil
}
func (service *fakePersonalizationService) SubmitFeedback(_ context.Context, submission personalization.FeedbackSubmission) (personalization.FeedbackResult, error) {
	if submission.ItemID != 3001 {
		return personalization.FeedbackResult{}, personalization.ErrChoiceNotVisible
	}
	return personalization.FeedbackResult{Inserted: true, Record: personalization.FeedbackRecord{ClientFeedbackID: submission.ClientFeedbackID, DecisionEventID: submission.DecisionEventID, ItemID: submission.ItemID, Value: submission.Value}}, nil
}
func (service *fakePersonalizationService) Summary() personalization.FeedbackSummary {
	return service.summary
}
func (service *fakePersonalizationService) Export() personalization.Export {
	return personalization.Export{SchemaVersion: 1, Preferences: service.preferences, Feedback: service.summary}
}
func (service *fakePersonalizationService) DeleteFeedback(_ context.Context, confirmation string) (int64, error) {
	if confirmation != personalization.DeleteFeedbackConfirmation {
		return 0, personalization.ErrDeleteConfirmation
	}
	return 2, nil
}

type staticRecommendationService struct {
	dashboard recommendation.DashboardSnapshot
}

type fakeMatchHistory struct {
	matches        []domain.MatchSession
	events         map[string][]domain.SemanticEvent
	decisionEvents map[string][]domain.DecisionEvent
	lastLimit      int
}

type fakeHistoryService struct {
	page             domain.HistoryPage
	export           domain.HistoryExport
	preferences      domain.HistoryPreferences
	lastLimit        int
	lastCursor       string
	lastConfirmation string
	deleteErr        error
	deleteAllErr     error
}

func (service *fakeHistoryService) List(_ context.Context, cursor string, limit int) (domain.HistoryPage, error) {
	service.lastCursor, service.lastLimit = cursor, limit
	return service.page, nil
}

func (service *fakeHistoryService) Get(_ context.Context, id string) (domain.MatchSummary, error) {
	for _, summary := range service.page.Matches {
		if summary.MatchID == id {
			return summary, nil
		}
	}
	return domain.MatchSummary{}, errors.New("not found")
}

func (service *fakeHistoryService) Export(_ context.Context, scope string) (domain.HistoryExport, error) {
	service.export.Scope = scope
	return service.export, nil
}

func (service *fakeHistoryService) Delete(context.Context, string, string) error {
	return service.deleteErr
}

func (service *fakeHistoryService) DeleteAll(_ context.Context, confirmation string) (int64, error) {
	service.lastConfirmation = confirmation
	return 2, service.deleteAllErr
}

func (service *fakeHistoryService) Preferences(context.Context) (domain.HistoryPreferences, error) {
	return service.preferences, nil
}

func (service *fakeHistoryService) SetPreferences(_ context.Context, preferences domain.HistoryPreferences) (domain.HistoryPreferences, error) {
	service.preferences = preferences
	return preferences, nil
}

func (history *fakeMatchHistory) ListMatches(_ context.Context, limit, offset int) ([]domain.MatchSession, error) {
	history.lastLimit = limit
	if offset >= len(history.matches) {
		return []domain.MatchSession{}, nil
	}
	end := min(offset+limit, len(history.matches))
	return append([]domain.MatchSession(nil), history.matches[offset:end]...), nil
}

func (history *fakeMatchHistory) ListMatchEvents(_ context.Context, matchID string, after uint64, limit int) ([]domain.SemanticEvent, error) {
	result := make([]domain.SemanticEvent, 0, limit)
	for _, event := range history.events[matchID] {
		if event.Sequence > after && len(result) < limit {
			result = append(result, event)
		}
	}
	return result, nil
}

func (history *fakeMatchHistory) ListDecisionEvents(_ context.Context, matchID string, after uint64, limit int) ([]domain.DecisionEvent, error) {
	result := make([]domain.DecisionEvent, 0, limit)
	for _, event := range history.decisionEvents[matchID] {
		if event.Sequence > after && len(result) < limit {
			result = append(result, event)
		}
	}
	return result, nil
}

func (service staticRecommendationService) Current() recommendation.DashboardSnapshot {
	return service.dashboard
}

func (service staticRecommendationService) Subscribe(int) (recommendation.DashboardSnapshot, <-chan recommendation.DashboardSnapshot, func()) {
	updates := make(chan recommendation.DashboardSnapshot)
	return service.dashboard, updates, func() { close(updates) }
}

func (service staticRecommendationService) Recalculate(context.Context) (recommendation.DashboardSnapshot, error) {
	return service.dashboard, nil
}

type decisionStreamingService struct {
	staticRecommendationService
	event domain.DecisionEvent
}

type fakePregamePlanner struct {
	options      pregame.Options
	optionsErr   error
	plan         pregame.Plan
	planErr      error
	optionsCalls int
	planCalls    int
}

func (planner *fakePregamePlanner) Options(context.Context) (pregame.Options, error) {
	planner.optionsCalls++
	return planner.options, planner.optionsErr
}

func (planner *fakePregamePlanner) Plan(_ context.Context, _ pregame.Scenario) (pregame.Plan, error) {
	planner.planCalls++
	return planner.plan, planner.planErr
}

type countingMatchHistory struct {
	calls int
}

func (history *countingMatchHistory) ListMatches(context.Context, int, int) ([]domain.MatchSession, error) {
	history.calls++
	return nil, errors.New("unexpected history read")
}

func (history *countingMatchHistory) ListMatchEvents(context.Context, string, uint64, int) ([]domain.SemanticEvent, error) {
	history.calls++
	return nil, errors.New("unexpected history read")
}

func (history *countingMatchHistory) ListDecisionEvents(context.Context, string, uint64, int) ([]domain.DecisionEvent, error) {
	history.calls++
	return nil, errors.New("unexpected history read")
}

func (service *decisionStreamingService) SubscribeDecisionEvents(int) (<-chan domain.DecisionEvent, func()) {
	updates := make(chan domain.DecisionEvent, 1)
	updates <- service.event
	return updates, func() { close(updates) }
}
