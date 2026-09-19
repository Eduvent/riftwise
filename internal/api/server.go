package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"lolengine/internal/catalog"
	"lolengine/internal/domain"
	"lolengine/internal/history"
	"lolengine/internal/itemization"
	"lolengine/internal/matchsync"
	"lolengine/internal/personalization"
	"lolengine/internal/pregame"
	"lolengine/internal/recommendation"
	"lolengine/internal/riotapi"
	"lolengine/internal/state"
	"lolengine/internal/statistics"
	"lolengine/internal/storage"
	"lolengine/internal/webui"
)

const defaultSSEKeepAlive = 15 * time.Second
const defaultSSEWriteTimeout = time.Second
const maximumPregameScenarioBytes int64 = 64 << 10
const maximumHistoryRequestBytes int64 = 16 << 10
const maximumRiotRequestBytes int64 = 16 << 10
const maximumPersonalizationRequestBytes int64 = 16 << 10

// Config controls transport behavior that is useful to tune or shorten in tests.
type Config struct {
	SSEKeepAlive           time.Duration
	SSEWriteTimeout        time.Duration
	ApplicationContext     context.Context
	Recommendations        recommendation.Service
	MatchHistory           MatchHistory
	History                HistoryService
	Pregame                pregame.Planner
	GenericProfilesEnabled bool
	RiotSync               RiotSyncService
	Statistics             StatisticsService
	Personalization        PersonalizationService
}

// MatchHistory is the authoritative durable event/history read boundary.
type MatchHistory interface {
	ListMatches(context.Context, int, int) ([]domain.MatchSession, error)
	ListMatchEvents(context.Context, string, uint64, int) ([]domain.SemanticEvent, error)
	ListDecisionEvents(context.Context, string, uint64, int) ([]domain.DecisionEvent, error)
}

// HistoryService is the identity-free authoritative match-summary boundary.
type HistoryService interface {
	List(context.Context, string, int) (domain.HistoryPage, error)
	Get(context.Context, string) (domain.MatchSummary, error)
	Export(context.Context, string) (domain.HistoryExport, error)
	Delete(context.Context, string, string) error
	DeleteAll(context.Context, string) (int64, error)
	Preferences(context.Context) (domain.HistoryPreferences, error)
	SetPreferences(context.Context, domain.HistoryPreferences) (domain.HistoryPreferences, error)
}

type RiotSyncService interface {
	Status(context.Context) (matchsync.Status, error)
	Link(context.Context, matchsync.LinkRequest) (matchsync.LinkedAccount, error)
	Sync(context.Context, string, matchsync.Filter) (matchsync.SyncResult, error)
	Delete(context.Context, string, string) error
}

type StatisticsService interface {
	Query(context.Context, statistics.Query) (statistics.Report, error)
}

type PersonalizationService interface {
	Current() personalization.Snapshot
	Options() (personalization.Options, error)
	Update(context.Context, personalization.PreferenceUpdate) (personalization.Preferences, error)
	Reset(context.Context) (personalization.Preferences, error)
	SubmitFeedback(context.Context, personalization.FeedbackSubmission) (personalization.FeedbackResult, error)
	Summary() personalization.FeedbackSummary
	Export() personalization.Export
	DeleteFeedback(context.Context, string) (int64, error)
}

type decisionNotificationSource interface {
	SubscribeDecisionEvents(int) (<-chan domain.DecisionEvent, func())
}

// Handler exposes the backend state over a small framework-free HTTP API.
type Handler struct {
	store                  *state.Store
	mux                    *http.ServeMux
	keepAlive              time.Duration
	writeTimeout           time.Duration
	applicationContext     context.Context
	recommendations        recommendation.Service
	matchHistory           MatchHistory
	history                HistoryService
	pregame                pregame.Planner
	genericProfilesEnabled bool
	riotSync               RiotSyncService
	statistics             StatisticsService
	personalization        PersonalizationService
}

// New creates the API handler.
func New(store *state.Store, cfg Config) http.Handler {
	keepAlive := cfg.SSEKeepAlive
	if keepAlive <= 0 {
		keepAlive = defaultSSEKeepAlive
	}
	writeTimeout := cfg.SSEWriteTimeout
	if writeTimeout <= 0 {
		writeTimeout = defaultSSEWriteTimeout
	}
	applicationContext := cfg.ApplicationContext
	if applicationContext == nil {
		applicationContext = context.Background()
	}
	recommendations := cfg.Recommendations
	if recommendations == nil {
		recommendations = newFallbackService(store)
	}

	handler := &Handler{
		store:                  store,
		mux:                    http.NewServeMux(),
		keepAlive:              keepAlive,
		writeTimeout:           writeTimeout,
		applicationContext:     applicationContext,
		recommendations:        recommendations,
		matchHistory:           cfg.MatchHistory,
		history:                cfg.History,
		pregame:                cfg.Pregame,
		genericProfilesEnabled: cfg.GenericProfilesEnabled,
		riotSync:               cfg.RiotSync,
		statistics:             cfg.Statistics,
		personalization:        cfg.Personalization,
	}
	handler.mux.HandleFunc("/api/health", handler.handleHealth)
	handler.mux.HandleFunc("/api/game", handler.handleGame)
	handler.mux.HandleFunc("/api/recommendation", handler.handleRecommendation)
	handler.mux.HandleFunc("/api/dashboard", handler.handleDashboard)
	handler.mux.HandleFunc("/api/events", handler.handleEvents)
	handler.mux.HandleFunc("/api/matches", handler.handleMatches)
	handler.mux.HandleFunc("/api/matches/", handler.handleMatchEvents)
	handler.mux.HandleFunc("/api/history/matches", handler.handleHistoryMatches)
	handler.mux.HandleFunc("/api/history/matches/", handler.handleHistoryMatch)
	handler.mux.HandleFunc("/api/history/export", handler.handleHistoryExport)
	handler.mux.HandleFunc("/api/history/statistics", handler.handleStatistics)
	handler.mux.HandleFunc("/api/privacy/history", handler.handleHistoryPreferences)
	handler.mux.HandleFunc("/api/riot/status", handler.handleRiotStatus)
	handler.mux.HandleFunc("/api/riot/account", handler.handleRiotAccounts)
	handler.mux.HandleFunc("/api/riot/accounts", handler.handleRiotAccounts)
	handler.mux.HandleFunc("/api/riot/accounts/", handler.handleRiotAccount)
	handler.mux.HandleFunc("/api/riot/sync", handler.handleRiotSync)
	handler.mux.HandleFunc("/api/recommendation/recalculate", handler.handleRecalculate)
	handler.mux.HandleFunc("/api/pregame/options", handler.handlePregameOptions)
	handler.mux.HandleFunc("/api/pregame/plan", handler.handlePregamePlan)
	handler.mux.HandleFunc("/api/personalization/preferences", handler.handlePersonalizationPreferences)
	handler.mux.HandleFunc("/api/personalization/options", handler.handlePersonalizationOptions)
	handler.mux.HandleFunc("/api/personalization/preferences/reset", handler.handlePersonalizationReset)
	handler.mux.HandleFunc("/api/personalization/feedback", handler.handlePersonalizationFeedback)
	handler.mux.HandleFunc("/api/personalization/export", handler.handlePersonalizationExport)
	// Keep unknown API routes on the stable JSON error contract before the UI's
	// root pattern handles browser routes and embedded assets.
	handler.mux.HandleFunc("/api", handler.handleNotFound)
	handler.mux.HandleFunc("/api/", handler.handleNotFound)
	handler.mux.Handle("/", webui.New())
	return handler
}

func (h *Handler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	if err := validateRequestHost(request); err != nil {
		writeAPIError(response, http.StatusForbidden, "forbidden_host", "Request host must be the local LoL Engine")
		return
	}
	if err := validateRequestOrigin(request); err != nil {
		writeAPIError(response, http.StatusForbidden, "forbidden_origin", "Request origin must match the local LoL Engine")
		return
	}
	h.mux.ServeHTTP(response, request)
}

func validateRequestHost(request *http.Request) error {
	_, err := canonicalLoopbackAuthority(request.Host, defaultPort(request))
	return err
}

func validateRequestOrigin(request *http.Request) error {
	origins := request.Header.Values("Origin")
	if len(origins) == 0 {
		return nil // curl and other non-browser local clients do not send Origin.
	}
	if len(origins) != 1 {
		return fmt.Errorf("multiple origins are not allowed")
	}
	rawOrigin := strings.TrimSpace(origins[0])
	if rawOrigin == "" {
		return fmt.Errorf("empty origin")
	}
	origin, err := url.Parse(rawOrigin)
	if err != nil || (origin.Scheme != "http" && origin.Scheme != "https") || origin.Host == "" || origin.User != nil {
		return fmt.Errorf("invalid origin")
	}
	if origin.RawQuery != "" || origin.Fragment != "" || (origin.Path != "" && origin.Path != "/") {
		return fmt.Errorf("origin must not contain path, query, or fragment")
	}
	expectedScheme := "http"
	if request.TLS != nil {
		expectedScheme = "https"
	}
	if origin.Scheme != expectedScheme {
		return fmt.Errorf("origin scheme does not match request")
	}
	requestAuthority, err := canonicalLoopbackAuthority(request.Host, defaultPort(request))
	if err != nil {
		return err
	}
	originDefaultPort := "80"
	if origin.Scheme == "https" {
		originDefaultPort = "443"
	}
	originAuthority, err := canonicalLoopbackAuthority(origin.Host, originDefaultPort)
	if err != nil {
		return err
	}
	if requestAuthority != originAuthority {
		return fmt.Errorf("origin authority does not match request host")
	}
	return nil
}

func canonicalLoopbackAuthority(rawAuthority, fallbackPort string) (string, error) {
	rawAuthority = strings.TrimSpace(rawAuthority)
	if rawAuthority == "" {
		return "", fmt.Errorf("empty authority")
	}
	parsed, err := url.Parse("http://" + rawAuthority)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("invalid authority")
	}
	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	if host == "" {
		return "", fmt.Errorf("empty host")
	}
	canonicalHost := host
	if !strings.EqualFold(host, "localhost") {
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return "", fmt.Errorf("host is not loopback")
		}
		canonicalHost = ip.String()
	}
	port := parsed.Port()
	if port == "" {
		port = fallbackPort
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return "", fmt.Errorf("invalid port")
	}
	return net.JoinHostPort(canonicalHost, port), nil
}

func defaultPort(request *http.Request) string {
	if request.TLS != nil {
		return "443"
	}
	return "80"
}

func (h *Handler) handleHealth(response http.ResponseWriter, request *http.Request) {
	if !requireMethod(response, request, http.MethodGet) {
		return
	}
	dashboard := h.recommendations.Current()
	snapshot := dashboard.Game
	recommendationReady := dashboard.Recommendation.Status == itemization.StatusReady ||
		dashboard.Recommendation.Status == itemization.StatusSaveForComponent ||
		dashboard.Recommendation.Status == itemization.StatusComplete
	var profileReady, profileFallback bool
	var profileVersion, profilePatch, profileSource, profileLastError string
	if profileStatus := dashboard.Recommendation.ProfileStatus; profileStatus != nil {
		profileReady = profileStatus.Ready
		profileVersion = profileStatus.Version
		profilePatch = profileStatus.Patch
		profileSource = profileStatus.Source
		profileFallback = profileStatus.Fallback
		profileLastError = profileStatus.LastError
	}
	var profileLevel, profileID string
	var profileConfidenceCap float64
	if profile := dashboard.Recommendation.Profile; profile != nil {
		profileLevel = string(profile.Level)
		profileID = profile.ProfileID
		profileConfidenceCap = profile.ConfidenceCap
	}
	riotStatus := h.safeRiotStatus(request.Context())
	writeJSON(response, http.StatusOK, map[string]any{
		"status":                    "ok",
		"epoch":                     dashboard.Epoch,
		"tracker_status":            snapshot.Status,
		"catalog_ready":             dashboard.Catalog.Ready,
		"catalog_patch":             dashboard.Catalog.Patch,
		"recommendation_ready":      recommendationReady,
		"recommendation_status":     dashboard.Recommendation.Status,
		"rules_ready":               dashboard.Recommendation.Rules.Ready,
		"rules_version":             dashboard.Recommendation.Rules.Version,
		"rules_patch":               dashboard.Recommendation.Rules.Patch,
		"rules_source":              dashboard.Recommendation.Rules.Source,
		"rules_fallback":            dashboard.Recommendation.Rules.Fallback,
		"rules_legacy":              dashboard.Recommendation.Rules.Legacy,
		"rules_last_error":          dashboard.Recommendation.Rules.LastError,
		"profile_ready":             profileReady,
		"profile_version":           profileVersion,
		"profile_patch":             profilePatch,
		"profile_source":            profileSource,
		"profile_fallback":          profileFallback,
		"profile_last_error":        profileLastError,
		"profile_level":             profileLevel,
		"profile_id":                profileID,
		"profile_confidence_cap":    profileConfidenceCap,
		"profile_advisories":        dashboard.Recommendation.Advisories,
		"generic_profiles_enabled":  h.genericProfilesEnabled,
		"phase":                     dashboard.Recommendation.Phase,
		"strategy":                  dashboard.Recommendation.Strategy,
		"decision_events_available": dashboard.DecisionEvents.Available,
		"decision_events_status":    dashboard.DecisionEvents.Status,
		"latest_decision_event":     dashboard.DecisionEvents.Latest,
		"personalization":           dashboard.Personalization,
		"revision":                  dashboard.Revision,
		"riot":                      riotStatus,
		"time":                      time.Now().UTC(),
	})
}

func (h *Handler) handleRiotStatus(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	if !requireMethod(response, request, http.MethodGet) {
		return
	}
	if h.riotSync == nil {
		writeJSON(response, http.StatusOK, matchsync.Status{Mode: riotapi.ModeDisabled, Configured: true})
		return
	}
	status, err := h.riotSync.Status(request.Context())
	if err != nil {
		writeAPIError(response, http.StatusInternalServerError, "riot_status_failed", "Riot enrichment status could not be read")
		return
	}
	writeJSON(response, http.StatusOK, status)
}

func (h *Handler) handleRiotAccounts(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	if h.riotSync == nil {
		writeAPIError(response, http.StatusServiceUnavailable, "riot_disabled", "Riot enrichment is disabled")
		return
	}
	switch request.Method {
	case http.MethodGet:
		status, err := h.riotSync.Status(request.Context())
		if err != nil {
			writeAPIError(response, http.StatusInternalServerError, "riot_status_failed", "Riot account status could not be read")
			return
		}
		writeJSON(response, http.StatusOK, status)
	case http.MethodPost:
		var body matchsync.LinkRequest
		if err := decodeSingleJSON(response, request, maximumRiotRequestBytes, &body); err != nil {
			writeAPIError(response, http.StatusBadRequest, "invalid_riot_account", "Riot account link request is invalid")
			return
		}
		linked, err := h.riotSync.Link(request.Context(), body)
		if err != nil {
			handleRiotError(response, err, "link")
			return
		}
		writeJSON(response, http.StatusCreated, linked)
	default:
		response.Header().Set("Allow", http.MethodGet+", "+http.MethodPost)
		writeAPIError(response, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed")
	}
}

func (h *Handler) handleRiotAccount(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	if h.riotSync == nil {
		writeAPIError(response, http.StatusServiceUnavailable, "riot_disabled", "Riot enrichment is disabled")
		return
	}
	if !requireMethod(response, request, http.MethodDelete) {
		return
	}
	accountID := strings.Trim(strings.TrimPrefix(request.URL.Path, "/api/riot/accounts/"), "/")
	if accountID == "" || strings.Contains(accountID, "/") || len(accountID) > 128 {
		h.handleNotFound(response, request)
		return
	}
	var body struct {
		Confirmation string `json:"confirmation"`
	}
	if err := decodeSingleJSON(response, request, maximumRiotRequestBytes, &body); err != nil || body.Confirmation != matchsync.DeleteConfirmation {
		writeAPIError(response, http.StatusBadRequest, "invalid_confirmation", "Exact Riot account deletion confirmation is required")
		return
	}
	if err := h.riotSync.Delete(request.Context(), accountID, body.Confirmation); err != nil {
		if errors.Is(err, storage.ErrRiotAccountNotFound) {
			writeAPIError(response, http.StatusNotFound, "riot_account_not_found", "Linked Riot account was not found")
			return
		}
		if errors.Is(err, matchsync.ErrAccountSettling) {
			writeAPIError(response, http.StatusConflict, "riot_account_settling", "Riot account synchronization is still settling")
			return
		}
		writeAPIError(response, http.StatusInternalServerError, "riot_delete_failed", "Linked Riot account data could not be deleted")
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"deleted": true, "account_id": accountID, "local_history_preserved": true})
}

func (h *Handler) handleRiotSync(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	if !requireMethod(response, request, http.MethodPost) {
		return
	}
	if h.riotSync == nil {
		writeAPIError(response, http.StatusServiceUnavailable, "riot_disabled", "Riot enrichment is disabled")
		return
	}
	var body struct {
		AccountID string           `json:"account_id"`
		Filter    matchsync.Filter `json:"filter"`
	}
	if err := decodeSingleJSON(response, request, maximumRiotRequestBytes, &body); err != nil || strings.TrimSpace(body.AccountID) == "" {
		writeAPIError(response, http.StatusBadRequest, "invalid_riot_sync", "Riot synchronization request is invalid")
		return
	}
	result, err := h.riotSync.Sync(request.Context(), body.AccountID, body.Filter)
	if err != nil {
		if errors.Is(err, matchsync.ErrDisabled) {
			writeAPIError(response, http.StatusServiceUnavailable, "riot_disabled", "Riot enrichment is disabled")
			return
		}
		if errors.Is(err, matchsync.ErrInvalidRequest) {
			writeAPIError(response, http.StatusUnprocessableEntity, "invalid_riot_sync", "Riot synchronization request is invalid")
			return
		}
		if errors.Is(err, matchsync.ErrAccountSettling) {
			writeAPIError(response, http.StatusConflict, "riot_account_settling", "Riot account data is being deleted")
			return
		}
		// The typed kind and committed progress are safe; the upstream error text
		// is intentionally omitted because it may originate outside this process.
		writeJSON(response, http.StatusBadGateway, map[string]any{
			"result": result,
			"error":  map[string]string{"code": "riot_sync_partial", "error_kind": result.ErrorKind, "message": "Riot synchronization stopped and can be resumed"},
		})
		return
	}
	if result.Status == storage.RiotSyncComplete && result.ReconciliationStatus == storage.RiotReconciliationPending {
		writeJSON(response, http.StatusAccepted, result)
		return
	}
	writeJSON(response, http.StatusOK, result)
}

func (h *Handler) handleStatistics(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	if !requireMethod(response, request, http.MethodGet) {
		return
	}
	if h.statistics == nil {
		writeAPIError(response, http.StatusServiceUnavailable, "statistics_unavailable", "Historical statistics are unavailable")
		return
	}
	accountID := strings.TrimSpace(request.URL.Query().Get("account_id"))
	champion := strings.TrimSpace(request.URL.Query().Get("champion"))
	patch := strings.TrimSpace(request.URL.Query().Get("patch"))
	if accountID == "" || len(accountID) > 128 || len(champion) > 100 || (patch != "" && !validMajorMinorPatch(patch)) {
		writeAPIError(response, http.StatusBadRequest, "invalid_statistics_query", "Historical statistics query is invalid")
		return
	}
	roleValue := strings.TrimSpace(request.URL.Query().Get("role"))
	var role domain.Role
	if roleValue != "" {
		var ok bool
		role, ok = domain.ParseRole(roleValue)
		if !ok {
			writeAPIError(response, http.StatusBadRequest, "invalid_statistics_query", "Historical statistics role is invalid")
			return
		}
	}
	minimum, err := boundedQueryInt(request, "minimum_sample", 10, 100)
	if err != nil || minimum < 1 {
		writeAPIError(response, http.StatusBadRequest, "invalid_statistics_query", "Historical statistics sample size is invalid")
		return
	}
	queue, errQueue := boundedQueryInt(request, "queue", 0, 10000)
	mapID, errMap := boundedQueryInt(request, "map", 0, 10000)
	if errQueue != nil || errMap != nil {
		writeAPIError(response, http.StatusBadRequest, "invalid_statistics_query", "Historical statistics filters are invalid")
		return
	}
	report, err := h.statistics.Query(request.Context(), statistics.Query{
		AccountID: accountID, Patch: patch, ChampionName: champion, Role: role,
		QueueID: queue, MapID: mapID, MinimumSample: minimum,
	})
	if err != nil {
		if errors.Is(err, statistics.ErrInvalidQuery) {
			writeAPIError(response, http.StatusBadRequest, "invalid_statistics_query", "Historical statistics query is invalid")
			return
		}
		writeAPIError(response, http.StatusInternalServerError, "statistics_failed", "Historical statistics could not be built")
		return
	}
	writeJSON(response, http.StatusOK, report)
}

func validMajorMinorPatch(value string) bool {
	parts := strings.Split(value, ".")
	if len(parts) != 2 {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
		number, err := strconv.Atoi(part)
		if err != nil || number < 0 || strconv.Itoa(number) != part {
			return false
		}
	}
	major, _ := strconv.Atoi(parts[0])
	return major > 0
}

func handleRiotError(response http.ResponseWriter, err error, operation string) {
	switch {
	case errors.Is(err, matchsync.ErrDisabled):
		writeAPIError(response, http.StatusServiceUnavailable, "riot_disabled", "Riot enrichment is disabled")
	case errors.Is(err, matchsync.ErrConsentRequired):
		writeAPIError(response, http.StatusUnprocessableEntity, "riot_consent_required", "Explicit account-link consent is required")
	case errors.Is(err, matchsync.ErrInvalidRequest), riotapi.KindOf(err) == riotapi.ErrorBadRequest:
		writeAPIError(response, http.StatusUnprocessableEntity, "invalid_riot_account", "Riot account link request is invalid")
	case riotapi.KindOf(err) == riotapi.ErrorNotFound:
		writeAPIError(response, http.StatusNotFound, "riot_account_not_found", "Riot account was not found")
	case riotapi.KindOf(err) == riotapi.ErrorRateLimited:
		writeAPIError(response, http.StatusTooManyRequests, "riot_rate_limited", "Riot service is rate limited; retry later")
	case riotapi.KindOf(err) == riotapi.ErrorUnauthorized, riotapi.KindOf(err) == riotapi.ErrorForbidden:
		writeAPIError(response, http.StatusServiceUnavailable, "riot_credentials_unavailable", "Riot credentials are not authorized")
	default:
		writeAPIError(response, http.StatusBadGateway, "riot_"+operation+"_failed", "Riot service request failed")
	}
}

func (h *Handler) handleGame(response http.ResponseWriter, request *http.Request) {
	if !requireMethod(response, request, http.MethodGet) {
		return
	}
	writeJSON(response, http.StatusOK, h.store.Current())
}

func (h *Handler) handleRecommendation(response http.ResponseWriter, request *http.Request) {
	if !requireMethod(response, request, http.MethodGet) {
		return
	}
	writeJSON(response, http.StatusOK, h.recommendations.Current().Recommendation)
}

func (h *Handler) handleDashboard(response http.ResponseWriter, request *http.Request) {
	if !requireMethod(response, request, http.MethodGet) {
		return
	}
	writeJSON(response, http.StatusOK, struct {
		recommendation.DashboardSnapshot
		Riot matchsync.Status `json:"riot"`
	}{DashboardSnapshot: h.recommendations.Current(), Riot: h.safeRiotStatus(request.Context())})
}

func (h *Handler) safeRiotStatus(ctx context.Context) matchsync.Status {
	if h.riotSync == nil {
		return matchsync.Status{Mode: riotapi.ModeDisabled, Configured: true}
	}
	status, err := h.riotSync.Status(ctx)
	if err != nil {
		return matchsync.Status{Configured: false}
	}
	return status
}

func (h *Handler) handleEvents(response http.ResponseWriter, request *http.Request) {
	if !requireMethod(response, request, http.MethodGet) {
		return
	}
	_, ok := response.(http.Flusher)
	if !ok {
		writeAPIError(response, http.StatusInternalServerError, "streaming_unsupported", "HTTP streaming is not supported")
		return
	}
	controller := http.NewResponseController(response)
	if err := controller.SetWriteDeadline(time.Time{}); err != nil {
		writeAPIError(response, http.StatusInternalServerError, "streaming_unsupported", "HTTP write deadlines are not supported")
		return
	}

	response.Header().Set("Content-Type", "text/event-stream")
	response.Header().Set("Cache-Control", "no-cache")
	response.Header().Set("Connection", "keep-alive")
	response.Header().Set("X-Accel-Buffering", "no")

	initial, updates, unsubscribe := h.recommendations.Subscribe(1)
	defer unsubscribe()
	var decisionUpdates <-chan domain.DecisionEvent
	cancelDecisions := func() {}
	if source, ok := h.recommendations.(decisionNotificationSource); ok {
		decisionUpdates, cancelDecisions = source.SubscribeDecisionEvents(16)
	}
	defer cancelDecisions()
	if err := h.writeSSE(response, func() error { return writeDashboardEvent(response, initial) }); err != nil {
		return
	}

	keepAlive := time.NewTicker(h.keepAlive)
	defer keepAlive.Stop()
	for {
		select {
		case <-h.applicationContext.Done():
			return
		case <-request.Context().Done():
			return
		case dashboard, open := <-updates:
			if !open {
				return
			}
			if err := h.writeSSE(response, func() error { return writeDashboardEvent(response, dashboard) }); err != nil {
				return
			}
		case decision, open := <-decisionUpdates:
			if !open {
				decisionUpdates = nil
				continue
			}
			if err := h.writeSSE(response, func() error { return writeDecisionEvent(response, decision) }); err != nil {
				return
			}
		case <-keepAlive.C:
			if err := h.writeSSE(response, func() error {
				_, err := fmt.Fprint(response, ": keepalive\n\n")
				return err
			}); err != nil {
				return
			}
		}
	}
}

func (h *Handler) handleMatches(response http.ResponseWriter, request *http.Request) {
	if !requireMethod(response, request, http.MethodGet) {
		return
	}
	if h.matchHistory == nil {
		writeAPIError(response, http.StatusServiceUnavailable, "history_unavailable", "Match history is unavailable")
		return
	}
	limit, err := boundedQueryInt(request, "limit", 20, 100)
	if err != nil {
		writeAPIError(response, http.StatusBadRequest, "invalid_pagination", "Pagination values must be non-negative integers")
		return
	}
	offset, err := boundedQueryInt(request, "offset", 0, 1_000_000)
	if err != nil {
		writeAPIError(response, http.StatusBadRequest, "invalid_pagination", "Pagination values must be non-negative integers")
		return
	}
	matches, err := h.matchHistory.ListMatches(request.Context(), limit, offset)
	if err != nil {
		writeAPIError(response, http.StatusInternalServerError, "history_failed", "Match history could not be read")
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"matches": matches, "limit": limit, "offset": offset})
}

func (h *Handler) handleMatchEvents(response http.ResponseWriter, request *http.Request) {
	if !requireMethod(response, request, http.MethodGet) {
		return
	}
	if h.matchHistory == nil {
		writeAPIError(response, http.StatusServiceUnavailable, "history_unavailable", "Match history is unavailable")
		return
	}
	remainder := strings.Trim(strings.TrimPrefix(request.URL.Path, "/api/matches/"), "/")
	parts := strings.Split(remainder, "/")
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || (parts[1] != "events" && parts[1] != "decision-events") {
		h.handleNotFound(response, request)
		return
	}
	limit, err := boundedQueryInt(request, "limit", 50, 100)
	if err != nil {
		writeAPIError(response, http.StatusBadRequest, "invalid_pagination", "Pagination values must be non-negative integers")
		return
	}
	after, err := boundedQueryUint(request, "after")
	if err != nil {
		writeAPIError(response, http.StatusBadRequest, "invalid_pagination", "Pagination values must be non-negative integers")
		return
	}
	if parts[1] == "decision-events" {
		events, err := h.matchHistory.ListDecisionEvents(request.Context(), parts[0], after, limit)
		if err != nil {
			writeAPIError(response, http.StatusInternalServerError, "history_failed", "Decision events could not be read")
			return
		}
		writeJSON(response, http.StatusOK, map[string]any{
			"match_id": parts[0], "events": events, "after": after, "limit": limit,
			"authoritative": true,
		})
		return
	}
	events, err := h.matchHistory.ListMatchEvents(request.Context(), parts[0], after, limit)
	if err != nil {
		writeAPIError(response, http.StatusInternalServerError, "history_failed", "Match events could not be read")
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{
		"match_id": parts[0], "events": events, "after": after, "limit": limit,
		"authoritative": true,
	})
}

func (h *Handler) handleHistoryMatches(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	if h.history == nil {
		writeAPIError(response, http.StatusServiceUnavailable, "history_unavailable", "Local match summaries are unavailable")
		return
	}
	switch request.Method {
	case http.MethodGet:
		limit, err := boundedQueryInt(request, "limit", 20, 100)
		if err != nil {
			writeAPIError(response, http.StatusBadRequest, "invalid_pagination", "History pagination is invalid")
			return
		}
		page, err := h.history.List(request.Context(), request.URL.Query().Get("before"), limit)
		if err != nil {
			if errors.Is(err, history.ErrInvalidCursor) {
				writeAPIError(response, http.StatusBadRequest, "invalid_cursor", "History cursor is invalid")
				return
			}
			writeAPIError(response, http.StatusInternalServerError, "history_failed", "Local match summaries could not be read")
			return
		}
		writeJSON(response, http.StatusOK, page)
	case http.MethodDelete:
		var body struct {
			Confirmation string `json:"confirmation"`
		}
		if err := decodeSingleJSON(response, request, maximumHistoryRequestBytes, &body); err != nil {
			writeAPIError(response, http.StatusBadRequest, "invalid_confirmation", "A valid deletion confirmation is required")
			return
		}
		deleted, err := h.history.DeleteAll(request.Context(), body.Confirmation)
		if err != nil {
			handleHistoryMutationError(response, err)
			return
		}
		writeJSON(response, http.StatusOK, map[string]any{"deleted": deleted})
	default:
		response.Header().Set("Allow", http.MethodGet+", "+http.MethodDelete)
		writeAPIError(response, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed")
	}
}

func (h *Handler) handleHistoryMatch(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	if h.history == nil {
		writeAPIError(response, http.StatusServiceUnavailable, "history_unavailable", "Local match summaries are unavailable")
		return
	}
	matchID := strings.Trim(strings.TrimPrefix(request.URL.Path, "/api/history/matches/"), "/")
	if matchID == "" || strings.Contains(matchID, "/") {
		h.handleNotFound(response, request)
		return
	}
	switch request.Method {
	case http.MethodGet:
		summary, err := h.history.Get(request.Context(), matchID)
		if err != nil {
			if errors.Is(err, catalog.ErrNotFound) {
				writeAPIError(response, http.StatusNotFound, "history_not_found", "Match summary was not found")
				return
			}
			writeAPIError(response, http.StatusInternalServerError, "history_failed", "Match summary could not be read")
			return
		}
		if summary.Digest != "" {
			response.Header().Set("ETag", `"`+summary.Digest+`"`)
		}
		writeJSON(response, http.StatusOK, summary)
	case http.MethodDelete:
		var body struct {
			Confirmation string `json:"confirmation"`
		}
		if err := decodeSingleJSON(response, request, maximumHistoryRequestBytes, &body); err != nil || body.Confirmation != history.DeleteOneConfirmation {
			writeAPIError(response, http.StatusBadRequest, "invalid_confirmation", "Exact match deletion confirmation is required")
			return
		}
		if err := h.history.Delete(request.Context(), matchID, body.Confirmation); err != nil {
			if errors.Is(err, catalog.ErrNotFound) {
				writeAPIError(response, http.StatusNotFound, "history_not_found", "Match summary was not found")
				return
			}
			handleHistoryMutationError(response, err)
			return
		}
		writeJSON(response, http.StatusOK, map[string]any{"deleted": 1, "match_id": matchID})
	default:
		response.Header().Set("Allow", http.MethodGet+", "+http.MethodDelete)
		writeAPIError(response, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed")
	}
}

func (h *Handler) handleHistoryExport(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	if !requireMethod(response, request, http.MethodGet) {
		return
	}
	if h.history == nil {
		writeAPIError(response, http.StatusServiceUnavailable, "history_unavailable", "Local match summaries are unavailable")
		return
	}
	exported, err := h.history.Export(request.Context(), strings.TrimSpace(request.URL.Query().Get("scope")))
	if err != nil {
		if errors.Is(err, history.ErrInvalidExportScope) {
			writeAPIError(response, http.StatusBadRequest, "invalid_export", "History export scope is invalid")
			return
		}
		writeAPIError(response, http.StatusInternalServerError, "history_failed", "Local match history could not be exported")
		return
	}
	filename := "lol-engine-history-" + exported.ExportedAt.UTC().Format("20060102T150405Z") + ".json"
	response.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	writeJSON(response, http.StatusOK, exported)
}

func (h *Handler) handleHistoryPreferences(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	if h.history == nil {
		writeAPIError(response, http.StatusServiceUnavailable, "history_unavailable", "Local history privacy settings are unavailable")
		return
	}
	switch request.Method {
	case http.MethodGet:
		preferences, err := h.history.Preferences(request.Context())
		if err != nil {
			writeAPIError(response, http.StatusInternalServerError, "history_failed", "History privacy settings could not be read")
			return
		}
		writeJSON(response, http.StatusOK, preferences)
	case http.MethodPut:
		var preferences domain.HistoryPreferences
		if err := decodeSingleJSON(response, request, maximumHistoryRequestBytes, &preferences); err != nil {
			writeAPIError(response, http.StatusBadRequest, "invalid_preferences", "History privacy settings are invalid")
			return
		}
		stored, err := h.history.SetPreferences(request.Context(), preferences)
		if err != nil {
			if errors.Is(err, history.ErrInvalidPreferences) {
				writeAPIError(response, http.StatusUnprocessableEntity, "invalid_preferences", "Retention values are outside the supported bounds")
				return
			}
			writeAPIError(response, http.StatusInternalServerError, "history_failed", "History privacy settings could not be saved")
			return
		}
		writeJSON(response, http.StatusOK, stored)
	default:
		response.Header().Set("Allow", http.MethodGet+", "+http.MethodPut)
		writeAPIError(response, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed")
	}
}

func handleHistoryMutationError(response http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, history.ErrConfirmationRequired):
		writeAPIError(response, http.StatusBadRequest, "confirmation_required", "Explicit local-history deletion confirmation is required")
	case errors.Is(err, history.ErrHistorySettling), errors.Is(err, storage.ErrHistoryDeleteChanged):
		writeAPIError(response, http.StatusConflict, "history_settling", "Match history is still committing and cannot be deleted yet")
	case errors.Is(err, storage.ErrActiveMatchHistory):
		writeAPIError(response, http.StatusConflict, "active_match_protected", "The active match cannot be deleted")
	default:
		writeAPIError(response, http.StatusInternalServerError, "history_delete_failed", "Local match history could not be deleted")
	}
}

func decodeSingleJSON(response http.ResponseWriter, request *http.Request, maximum int64, target any) error {
	request.Body = http.MaxBytesReader(response, request.Body, maximum)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("request body must contain one JSON value")
	}
	return nil
}

func boundedQueryInt(request *http.Request, key string, fallback, maximum int) (int, error) {
	raw := strings.TrimSpace(request.URL.Query().Get(key))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		return 0, errors.New("invalid pagination")
	}
	if value == 0 && key == "limit" {
		return fallback, nil
	}
	if value > maximum {
		value = maximum
	}
	return value, nil
}

func boundedQueryUint(request *http.Request, key string) (uint64, error) {
	raw := strings.TrimSpace(request.URL.Query().Get(key))
	if raw == "" {
		return 0, nil
	}
	return strconv.ParseUint(raw, 10, 64)
}

func (h *Handler) writeSSE(response http.ResponseWriter, write func() error) (err error) {
	controller := http.NewResponseController(response)
	if err = controller.SetWriteDeadline(time.Now().Add(h.writeTimeout)); err != nil {
		return fmt.Errorf("set SSE write deadline: %w", err)
	}
	defer func() {
		clearErr := controller.SetWriteDeadline(time.Time{})
		if err == nil && clearErr != nil {
			err = fmt.Errorf("clear SSE write deadline: %w", clearErr)
		}
	}()

	if err = write(); err != nil {
		return err
	}
	if err = controller.Flush(); err != nil {
		return fmt.Errorf("flush SSE response: %w", err)
	}
	return nil
}

func (h *Handler) handleRecalculate(response http.ResponseWriter, request *http.Request) {
	if !requireMethod(response, request, http.MethodPost) {
		return
	}
	dashboard, err := h.recommendations.Recalculate(request.Context())
	if err != nil {
		writeAPIError(response, http.StatusServiceUnavailable, "recalculation_failed", "Recommendation recalculation failed")
		return
	}
	writeJSON(response, http.StatusOK, dashboard.Recommendation)
}

func (h *Handler) handlePregameOptions(response http.ResponseWriter, request *http.Request) {
	if !requireMethod(response, request, http.MethodGet) {
		return
	}
	if h.pregame == nil {
		writeAPIError(response, http.StatusServiceUnavailable, "pregame_unavailable", "Manual pregame planning is unavailable")
		return
	}
	options, err := h.pregame.Options(request.Context())
	if err != nil {
		writeAPIError(response, http.StatusServiceUnavailable, "pregame_unavailable", "Manual pregame planning is unavailable")
		return
	}
	writeJSON(response, http.StatusOK, options)
}

func (h *Handler) handlePregamePlan(response http.ResponseWriter, request *http.Request) {
	if !requireMethod(response, request, http.MethodPost) {
		return
	}
	if h.pregame == nil {
		writeAPIError(response, http.StatusServiceUnavailable, "pregame_unavailable", "Manual pregame planning is unavailable")
		return
	}
	request.Body = http.MaxBytesReader(response, request.Body, maximumPregameScenarioBytes)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	var scenario pregame.Scenario
	if err := decoder.Decode(&scenario); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeAPIError(response, http.StatusRequestEntityTooLarge, "scenario_too_large", "Manual scenario exceeds the maximum request size")
			return
		}
		writeAPIValidationError(response, map[string]string{"body": "El cuerpo debe ser un único objeto JSON con campos conocidos."})
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeAPIError(response, http.StatusRequestEntityTooLarge, "scenario_too_large", "Manual scenario exceeds the maximum request size")
			return
		}
		writeAPIValidationError(response, map[string]string{"body": "El cuerpo debe contener un único objeto JSON."})
		return
	}
	plan, err := h.pregame.Plan(request.Context(), scenario)
	if err != nil {
		var validation *pregame.ValidationError
		switch {
		case errors.As(err, &validation):
			writeAPIValidationError(response, validation.Fields)
		case errors.Is(err, pregame.ErrUnavailable):
			writeAPIError(response, http.StatusServiceUnavailable, "pregame_unavailable", "Manual pregame planning is unavailable for the active catalog")
		default:
			writeAPIError(response, http.StatusInternalServerError, "pregame_failed", "Manual pregame planning failed")
		}
		return
	}
	writeJSON(response, http.StatusOK, plan)
}

func (h *Handler) handlePersonalizationPreferences(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	if h.personalization == nil {
		writeAPIError(response, http.StatusServiceUnavailable, "personalization_unavailable", "Personalization is unavailable")
		return
	}
	switch request.Method {
	case http.MethodGet:
		writeJSON(response, http.StatusOK, h.personalization.Current().Preferences)
	case http.MethodPut:
		var update personalization.PreferenceUpdate
		if code := decodeStrictBoundedJSON(response, request, maximumPersonalizationRequestBytes, &update); code != "" {
			writeAPIError(response, statusForDecodeCode(code), code, "Personalization preferences must be one bounded JSON object with known fields")
			return
		}
		preferences, err := h.personalization.Update(request.Context(), update)
		if err != nil {
			handlePersonalizationError(response, err)
			return
		}
		_, _ = h.recommendations.Recalculate(request.Context())
		writeJSON(response, http.StatusOK, preferences)
	default:
		response.Header().Set("Allow", "GET, PUT")
		writeAPIError(response, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed")
	}
}

func (h *Handler) handlePersonalizationOptions(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	if !requireMethod(response, request, http.MethodGet) {
		return
	}
	if h.personalization == nil {
		writeAPIError(response, http.StatusServiceUnavailable, "personalization_unavailable", "Personalization is unavailable")
		return
	}
	options, err := h.personalization.Options()
	if err != nil {
		handlePersonalizationError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, options)
}

func (h *Handler) handlePersonalizationReset(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	if !requireMethod(response, request, http.MethodPost) {
		return
	}
	if h.personalization == nil {
		writeAPIError(response, http.StatusServiceUnavailable, "personalization_unavailable", "Personalization is unavailable")
		return
	}
	var body *struct{}
	if code := decodeStrictBoundedJSON(response, request, maximumPersonalizationRequestBytes, &body); code != "" {
		writeAPIError(response, statusForDecodeCode(code), code, "Reset request must be an empty JSON object")
		return
	}
	if body == nil {
		writeAPIError(response, http.StatusUnprocessableEntity, "invalid_preferences", "Reset request must be an empty JSON object")
		return
	}
	preferences, err := h.personalization.Reset(request.Context())
	if err != nil {
		handlePersonalizationError(response, err)
		return
	}
	_, _ = h.recommendations.Recalculate(request.Context())
	writeJSON(response, http.StatusOK, preferences)
}

func (h *Handler) handlePersonalizationFeedback(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	if h.personalization == nil {
		writeAPIError(response, http.StatusServiceUnavailable, "personalization_unavailable", "Personalization is unavailable")
		return
	}
	switch request.Method {
	case http.MethodGet:
		writeJSON(response, http.StatusOK, h.personalization.Summary())
	case http.MethodPost:
		var submission personalization.FeedbackSubmission
		if code := decodeStrictBoundedJSON(response, request, maximumPersonalizationRequestBytes, &submission); code != "" {
			writeAPIError(response, statusForDecodeCode(code), code, "Feedback must be one bounded JSON object with known fields")
			return
		}
		result, err := h.personalization.SubmitFeedback(request.Context(), submission)
		if err != nil {
			handlePersonalizationError(response, err)
			return
		}
		_, _ = h.recommendations.Recalculate(request.Context())
		writeJSON(response, http.StatusOK, result)
	case http.MethodDelete:
		var body struct {
			Confirmation string `json:"confirmation"`
		}
		if code := decodeStrictBoundedJSON(response, request, maximumPersonalizationRequestBytes, &body); code != "" {
			writeAPIError(response, statusForDecodeCode(code), code, "Feedback deletion requires one bounded JSON object")
			return
		}
		deleted, err := h.personalization.DeleteFeedback(request.Context(), body.Confirmation)
		if err != nil {
			handlePersonalizationError(response, err)
			return
		}
		_, _ = h.recommendations.Recalculate(request.Context())
		writeJSON(response, http.StatusOK, map[string]any{"deleted": deleted})
	default:
		response.Header().Set("Allow", "GET, POST, DELETE")
		writeAPIError(response, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed")
	}
}

func (h *Handler) handlePersonalizationExport(response http.ResponseWriter, request *http.Request) {
	response.Header().Set("Cache-Control", "no-store")
	if !requireMethod(response, request, http.MethodGet) {
		return
	}
	if h.personalization == nil {
		writeAPIError(response, http.StatusServiceUnavailable, "personalization_unavailable", "Personalization is unavailable")
		return
	}
	writeJSON(response, http.StatusOK, h.personalization.Export())
}

func decodeStrictBoundedJSON(response http.ResponseWriter, request *http.Request, limit int64, target any) string {
	request.Body = http.MaxBytesReader(response, request.Body, limit)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return "personalization_too_large"
		}
		return "invalid_preferences"
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return "invalid_preferences"
	}
	return ""
}

func statusForDecodeCode(code string) int {
	if code == "personalization_too_large" {
		return http.StatusRequestEntityTooLarge
	}
	return http.StatusUnprocessableEntity
}

func handlePersonalizationError(response http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, personalization.ErrUnavailable):
		writeAPIError(response, http.StatusServiceUnavailable, "personalization_unavailable", "Personalization is unavailable for the active catalog")
	case errors.Is(err, personalization.ErrFeedbackDisabled):
		writeAPIError(response, http.StatusConflict, "feedback_disabled", "Recommendation feedback is disabled")
	case errors.Is(err, personalization.ErrDecisionNotFound), errors.Is(err, personalization.ErrChoiceNotVisible):
		writeAPIError(response, http.StatusUnprocessableEntity, "feedback_not_eligible", "Feedback must reference an existing visible decision choice")
	case errors.Is(err, personalization.ErrFeedbackConflict):
		writeAPIError(response, http.StatusConflict, "feedback_conflict", "The client feedback id is already associated with different feedback")
	case errors.Is(err, personalization.ErrDeleteConfirmation):
		writeAPIError(response, http.StatusBadRequest, "invalid_confirmation", "Exact personalization feedback deletion confirmation is required")
	case errors.Is(err, personalization.ErrInvalidPreference):
		writeAPIError(response, http.StatusUnprocessableEntity, "invalid_preferences", "Personalization preferences are invalid")
	default:
		writeAPIError(response, http.StatusInternalServerError, "personalization_failed", "Personalization could not be updated")
	}
}

func (h *Handler) handleNotFound(response http.ResponseWriter, request *http.Request) {
	writeAPIError(response, http.StatusNotFound, "not_found", "The requested endpoint does not exist")
}

func requireMethod(response http.ResponseWriter, request *http.Request, allowed string) bool {
	if request.Method == allowed {
		return true
	}
	response.Header().Set("Allow", allowed)
	writeAPIError(response, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed")
	return false
}

func writeDashboardEvent(response http.ResponseWriter, dashboard recommendation.DashboardSnapshot) error {
	payload, err := json.Marshal(dashboard)
	if err != nil {
		return fmt.Errorf("encode snapshot event: %w", err)
	}
	eventID := strconv.FormatUint(dashboard.Revision, 10)
	if recommendation.ValidEpoch(dashboard.Epoch) {
		eventID = dashboard.Epoch + ":" + eventID
	}
	_, err = fmt.Fprintf(response, "id: %s\nevent: dashboard_snapshot\ndata: %s\n\n", eventID, payload)
	return err
}

func writeDecisionEvent(response http.ResponseWriter, event domain.DecisionEvent) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("encode decision notification: %w", err)
	}
	_, err = fmt.Fprintf(response, "id: %s\nevent: decision_notification\ndata: %s\n\n", event.ID, payload)
	return err
}

type fallbackService struct {
	game  *state.Store
	store *recommendation.Store
}

func newFallbackService(game *state.Store) *fallbackService {
	snapshot := game.Current()
	result := (itemization.Engine{}).Recommend(time.Now().UTC(), snapshot, catalog.Catalog{})
	return &fallbackService{
		game: game,
		store: recommendation.NewStore(recommendation.DashboardSnapshot{
			Game:           snapshot,
			Catalog:        catalog.Status{Source: "unavailable", UpdatedAt: time.Now().UTC()},
			Recommendation: result,
			DecisionEvents: domain.DecisionEventAvailability{Status: domain.DecisionEventStatusUnavailable},
			UpdatedAt:      time.Now().UTC(),
		}),
	}
}

func (service *fallbackService) Current() recommendation.DashboardSnapshot {
	return service.store.Current()
}

func (service *fallbackService) Subscribe(buffer int) (recommendation.DashboardSnapshot, <-chan recommendation.DashboardSnapshot, func()) {
	return service.store.Subscribe(buffer)
}

func (service *fallbackService) Recalculate(context.Context) (recommendation.DashboardSnapshot, error) {
	game := service.game.Current()
	result := (itemization.Engine{}).Recommend(time.Now().UTC(), game, catalog.Catalog{})
	return service.store.Update(game, service.store.Current().Catalog, result, time.Now().UTC()), nil
}

type errorEnvelope struct {
	Error apiError `json:"error"`
}

type apiError struct {
	Code    string            `json:"code"`
	Message string            `json:"message"`
	Fields  map[string]string `json:"fields,omitempty"`
}

func writeAPIError(response http.ResponseWriter, status int, code, message string) {
	writeJSON(response, status, errorEnvelope{Error: apiError{Code: code, Message: message}})
}

func writeAPIValidationError(response http.ResponseWriter, fields map[string]string) {
	clone := make(map[string]string, len(fields))
	for key, value := range fields {
		clone[key] = value
	}
	writeJSON(response, http.StatusUnprocessableEntity, errorEnvelope{Error: apiError{
		Code: "invalid_scenario", Message: "Manual pregame scenario is invalid", Fields: clone,
	}})
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}
