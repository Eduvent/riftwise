package webui

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func TestEmbeddedAssets(t *testing.T) {
	handler := New()
	tests := []struct {
		name        string
		path        string
		contentType string
		cache       string
		contains    string
	}{
		{name: "index", path: "/", contentType: "text/html; charset=utf-8", cache: "no-store", contains: `<main id="main-content"`},
		{name: "index alias", path: "/index.html", contentType: "text/html; charset=utf-8", cache: "no-store", contains: "LoL Engine"},
		{name: "stylesheet", path: "/assets/app.css", contentType: "text/css; charset=utf-8", cache: "no-cache", contains: "prefers-reduced-motion"},
		{name: "state helpers", path: "/assets/state.js", contentType: "text/javascript; charset=utf-8", cache: "no-cache", contains: "evaluateDashboardVersion"},
		{name: "application", path: "/assets/app.js", contentType: "text/javascript; charset=utf-8", cache: "no-cache", contains: `new EventSource("/api/events")`},
		{name: "history state", path: "/assets/history-state.js", contentType: "text/javascript; charset=utf-8", cache: "no-cache", contains: "normalizePage"},
		{name: "history application", path: "/assets/history.js", contentType: "text/javascript; charset=utf-8", cache: "no-cache", contains: `fetch("/api/privacy/history"`},
		{name: "favicon", path: "/favicon.svg", contentType: "image/svg+xml", cache: "no-cache", contains: "<svg"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", response.Code)
			}
			if got := response.Header().Get("Content-Type"); got != test.contentType {
				t.Fatalf("Content-Type = %q, want %q", got, test.contentType)
			}
			if got := response.Header().Get("Cache-Control"); got != test.cache {
				t.Fatalf("Cache-Control = %q, want %q", got, test.cache)
			}
			if !strings.Contains(response.Body.String(), test.contains) {
				t.Fatalf("response for %s does not contain %q", test.path, test.contains)
			}
			if response.Header().Get("Content-Security-Policy") == "" {
				t.Fatal("Content-Security-Policy is empty")
			}
		})
	}
}

func TestHandlerRejectsUnknownRoutesAndMethods(t *testing.T) {
	handler := New()

	t.Run("unknown route", func(t *testing.T) {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/not-a-route", nil))
		if response.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", response.Code)
		}
	})

	t.Run("post", func(t *testing.T) {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/", nil))
		if response.Code != http.StatusMethodNotAllowed {
			t.Fatalf("status = %d, want 405", response.Code)
		}
		if allow := response.Header().Get("Allow"); allow != "GET, HEAD" {
			t.Fatalf("Allow = %q, want GET, HEAD", allow)
		}
	})
}

func TestBrowserSourceContract(t *testing.T) {
	javascript, err := fs.ReadFile(embeddedAssets, "assets/app.js")
	if err != nil {
		t.Fatalf("read app.js: %v", err)
	}
	source := string(javascript)
	for _, forbidden := range []string{"inner" + "HTML", "document.write", "eval(", "warnings[0]", "Caché alterno"} {
		if strings.Contains(source, forbidden) {
			t.Fatalf("app.js contains unsafe DOM primitive %q", forbidden)
		}
	}
	for _, required := range []string{
		"textContent",
		"replaceChildren",
		`fetch("/api/dashboard"`,
		`new EventSource("/api/events")`,
		`fetch("/api/recommendation/recalculate"`,
		`get("demo") === "1"`,
		"Number.isSafeInteger",
		"PATCH_PATTERN",
		"connectionGeneration",
		"new AbortController()",
		"signal: controller.signal",
		"isCurrentOpenConnection(expectedGeneration)",
		"inventory_slots_used",
		"wholeNumber(item.slot, -1) !== 6",
		"systemAlertDetails.replaceChildren",
		"map((detail) => {",
		"const alertDetails = Array.from(new Set",
		"catalog.source",
		"catalog.fallback",
		`remote: { label: "Data Dragon", tone: "good" }`,
		`cache: { label: "Caché en disco", tone: "good" }`,
		`memory_cache: { label: "Caché en memoria", tone: "pending" }`,
		`remote_unpersisted: { label: "Remoto sin guardar", tone: "pending" }`,
		`catalog.fallback ? "pending" : source.tone`,
		"El catálogo remoto actualizado está activo, pero no pudo guardarse localmente",
		"se mantiene el catálogo validado en memoria",
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("app.js is missing source contract %q", required)
		}
	}

	html, err := fs.ReadFile(embeddedAssets, "assets/index.html")
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}
	markup := string(html)
	if strings.Contains(markup, "http://") || strings.Contains(markup, "https://") {
		t.Fatal("index.html must not load remote scripts, styles, fonts, or images")
	}
	if !strings.Contains(markup, "MODO DEMO") {
		t.Fatal("index.html is missing the explicit demo banner")
	}
	if !strings.Contains(markup, `id="system-alert-details"`) {
		t.Fatal("index.html is missing the accessible warning-details list")
	}
	for _, required := range []string{`id="history-view-button"`, `id="history-view"`, "Resultado no disponible", "historial local"} {
		if !strings.Contains(markup, required) {
			t.Fatalf("index.html is missing history contract %q", required)
		}
	}
	stateScript := strings.Index(markup, `<script src="/assets/state.js" defer></script>`)
	applicationScript := strings.Index(markup, `<script src="/assets/app.js" defer></script>`)
	if stateScript < 0 || applicationScript < 0 || stateScript > applicationScript {
		t.Fatal("index.html must load pure state helpers before the dashboard application")
	}
	historyStateScript := strings.Index(markup, `<script src="/assets/history-state.js" defer></script>`)
	historyApplicationScript := strings.Index(markup, `<script src="/assets/history.js" defer></script>`)
	if historyStateScript < 0 || historyApplicationScript < 0 || historyStateScript > historyApplicationScript {
		t.Fatal("index.html must load pure history state helpers before history application")
	}

	stylesheet, err := fs.ReadFile(embeddedAssets, "assets/app.css")
	if err != nil {
		t.Fatalf("read app.css: %v", err)
	}
	css := string(stylesheet)
	for _, pattern := range []string{
		`(?s)\.metric-label\s*\{[^}]*font-size:\s*0\.875rem`,
		`(?s)dt\s*\{[^}]*font-size:\s*0\.875rem`,
	} {
		matched, matchErr := regexp.MatchString(pattern, css)
		if matchErr != nil {
			t.Fatalf("compile CSS contract %q: %v", pattern, matchErr)
		}
		if !matched {
			t.Fatalf("app.css does not satisfy legibility contract %q", pattern)
		}
	}
	if !strings.Contains(css, "--dim: #929eac;") {
		t.Fatal("app.css is missing the higher-contrast secondary text token")
	}
}

func TestChoicePresentationContract(t *testing.T) {
	javascript, err := fs.ReadFile(embeddedAssets, "assets/app.js")
	if err != nil {
		t.Fatalf("read app.js: %v", err)
	}
	html, err := fs.ReadFile(embeddedAssets, "assets/index.html")
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}
	source := string(javascript)
	markup := string(html)
	combined := source + "\n" + markup
	for _, forbidden := range []string{"COMPRA AHORA", "Compra este componente"} {
		if strings.Contains(combined, forbidden) {
			t.Fatalf("choice UI contains imperative copy %q", forbidden)
		}
	}
	for _, required := range []string{
		`id="ranked-choice-list"`,
		`aria-labelledby="ranked-choice-heading"`,
		"OPCIONES PARA COMPARAR",
	} {
		if !strings.Contains(markup, required) {
			t.Fatalf("choice markup is missing %q", required)
		}
	}
	for _, required := range []string{
		"function recommendationChoices(recommendation)",
		"recommendation.ranked_options",
		"recommendation.alternatives",
		"recommendation.alternative",
		"elements.rankedChoiceList.replaceChildren(...cards)",
		`document.createElement("li")`,
		"OPCIÓN DISPONIBLE",
		"Componente sugerido",
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("choice renderer is missing contract %q", required)
		}
	}
}

func TestAccessibleDecisionAlertContract(t *testing.T) {
	javascript, err := fs.ReadFile(embeddedAssets, "assets/app.js")
	if err != nil {
		t.Fatalf("read app.js: %v", err)
	}
	html, err := fs.ReadFile(embeddedAssets, "assets/index.html")
	if err != nil {
		t.Fatalf("read index.html: %v", err)
	}
	source := string(javascript)
	markup := string(html)
	for _, required := range []string{
		`id="decision-alert-region"`, `role="status"`, `aria-live="polite"`,
		`id="decision-history-list"`, `id="alert-mode"`, `id="alert-volume"`,
		`id="alert-muted"`, `id="alert-cooldown"`,
	} {
		if !strings.Contains(markup, required) {
			t.Fatalf("alert markup is missing %q", required)
		}
	}
	for _, required := range []string{
		`mode: "off"`, `muted: true`,
		`new Set(["off", "tone", "voice"])`,
		"localStorage.getItem(ALERT_SETTINGS_KEY)",
		"sessionStorage.getItem(ALERT_SEEN_KEY)",
		"clamp(finiteNumber(value.volume, DEFAULT_ALERT_SETTINGS.volume), 0, 1)",
		"clamp(wholeNumber(value.cooldownSeconds, DEFAULT_ALERT_SETTINGS.cooldownSeconds), 5, 300)",
		"function activateAlertsFromGesture()",
		`eventSource.addEventListener("decision_notification"`,
		"dashboardState.evaluateDashboardVersion",
		"dashboardState.acceptsDecisionSession(currentSessionID, event.session_id)",
		"dashboardState.shouldReconcileHistory",
		"elements.decisionAlertRegion.replaceChildren()",
		"currentLatestDecisionSequence - 50",
		"/decision-events?after=${after}&limit=50",
		"event.importance !== \"important\" && event.importance !== \"critical\"",
		`currentTrackerStatus === "stale"`,
		"Date.now() - lastAudibleAlertAt",
		"window.AudioContext || window.webkitAudioContext",
		"window.speechSynthesis",
		"Tus opciones de compra cambiaron.",
		"MAX_VISIBLE_TOASTS",
		"MAX_DECISION_HISTORY",
		"recommendation.ranked_options",
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("alert source is missing contract %q", required)
		}
	}
	if strings.Contains(source, "Notification.requestPermission") {
		t.Fatal("browser must not request notification permission automatically")
	}
	for _, forbidden := range []string{"Compra ahora", "Compra este", "Debes comprar"} {
		if strings.Contains(source+markup, forbidden) {
			t.Fatalf("alert UI contains prescriptive language %q", forbidden)
		}
	}
}

func TestAccessiblePersonalizationAndFeedbackContract(t *testing.T) {
	javascript, err := fs.ReadFile(embeddedAssets, "assets/app.js")
	if err != nil {
		t.Fatal(err)
	}
	html, err := fs.ReadFile(embeddedAssets, "assets/index.html")
	if err != nil {
		t.Fatal(err)
	}
	source, markup := string(javascript), string(html)
	for _, required := range []string{
		`id="personalization-form"`, `id="personalization-style"`, `id="personalization-preferred"`,
		`id="personalization-avoided"`, `id="personalization-feedback-enabled"`,
		`id="personalization-delete-feedback"`,
		`id="personalization-status"`, `role="status"`, `aria-live="polite"`,
		"No predice resultados, victorias ni rendimiento", "Los contadores críticos permanecen disponibles",
	} {
		if !strings.Contains(markup, required) {
			t.Fatalf("personalization markup is missing %q", required)
		}
	}
	for _, required := range []string{
		`fetch("/api/personalization/preferences"`, `fetch("/api/personalization/options"`,
		`fetch("/api/personalization/feedback"`, "beginPersonalizationMutation", "settlePersonalizationMutation",
		"personalizationUpdatePayload", "feedbackChoiceKey", "beginFeedbackSubmission", "settleFeedbackSubmission",
		"feedbackEvidenceMatchesRecommendation", "dashboardState.acceptsDecisionSession",
		"createRankedChoice(choice, index, patch, true)",
		`confirmation: "DELETE_PERSONALIZATION_FEEDBACK"`, "El historial de partidas no cambió",
		"la personalización queda pausada",
		"reutilizará exactamente la misma valoración", "No se usa para inferir resultados",
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("personalization source is missing %q", required)
		}
	}
	for _, forbidden := range []string{"optimiza tus victorias", "mejor build", "debes comprar"} {
		if strings.Contains(strings.ToLower(source+markup), forbidden) {
			t.Fatalf("personalization UI contains forbidden outcome/prescriptive copy %q", forbidden)
		}
	}
}

func TestAccessibleManualPregameContract(t *testing.T) {
	javascript, err := fs.ReadFile(embeddedAssets, "assets/app.js")
	if err != nil {
		t.Fatal(err)
	}
	html, err := fs.ReadFile(embeddedAssets, "assets/index.html")
	if err != nil {
		t.Fatal(err)
	}
	source := string(javascript)
	markup := string(html)
	for _, required := range []string{
		`id="pregame-view-button"`, `aria-controls="pregame-view"`, `id="pregame-form"`,
		`Escenario manual. No representa datos de una partida activa.`,
		`id="pregame-champion"`, `id="pregame-lane-opponent"`, `id="pregame-role"`,
		`id="pregame-stage"`, `id="pregame-strategy"`, `id="pregame-gold"`,
		`data-pregame-pressure="magic_resist"`, `id="pregame-inventory-rows"`,
		`id="pregame-error-summary"`, `role="alert"`, `tabindex="-1"`,
		`id="pregame-result"`, `role="status"`, `aria-live="polite"`,
		`id="pregame-ranked-options"`, `id="pregame-runes"`, `id="pregame-summoners"`, `id="pregame-initial-plan"`,
	} {
		if !strings.Contains(markup, required) {
			t.Fatalf("pregame markup is missing %q", required)
		}
	}
	pregameStart := strings.Index(markup, `id="pregame-view"`)
	pregameEnd := strings.LastIndex(markup, `</main>`)
	if pregameStart < 0 || pregameEnd <= pregameStart {
		t.Fatal("cannot isolate pregame view")
	}
	pregameMarkup := markup[pregameStart:pregameEnd]
	for _, forbidden := range []string{"live-stamp", "decision-alert-panel", "COMPRA AHORA", "Compra este"} {
		if strings.Contains(pregameMarkup, forbidden) {
			t.Fatalf("pregame view contains live/prescriptive contract %q", forbidden)
		}
	}
	for _, required := range []string{
		`fetch("/api/pregame/options"`, `fetch("/api/pregame/plan"`,
		"dashboardState.buildPregameScenario", "dashboardState.validatePregameScenario",
		"dashboardState.beginPregameOptionsRequest", "dashboardState.evaluatePregameOptionsResponse",
		"dashboardState.canUsePregameOptions", "dashboardState.canApplyPregamePlanRequest",
		"dashboardState.applyPregamePlanEffect", "isCurrentPregamePlanRequest",
		"applyCurrentPregamePlanEffect", "synchronizePregameCatalogPatch",
		"elements.pregameErrorSummary.focus()", "control.setAttribute(\"aria-invalid\", \"true\")",
		"elements.pregameRankedOptions.replaceChildren(...cards)", "container.replaceChildren(summary, list)",
		"provenance.rules", "Garen v2",
		`activeView !== "live"`, `elements.liveStatusCluster.hidden = planning`,
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("pregame source is missing %q", required)
		}
	}
	if strings.Contains(source, "inner"+"HTML") {
		t.Fatal("pregame UI must use safe DOM APIs")
	}
	renderStart := strings.Index(source, "function renderPregamePlan")
	if renderStart < 0 {
		t.Fatal("cannot isolate manual pregame renderer")
	}
	renderEnd := strings.Index(source[renderStart:], "function renderPregameCopy")
	if renderEnd < 0 {
		t.Fatal("cannot isolate manual pregame renderer")
	}
	renderer := source[renderStart : renderStart+renderEnd]
	if strings.Contains(renderer, "appendFeedbackControls") || strings.Contains(renderer, "validPatch(plan.patch), true") {
		t.Fatal("manual pregame choices must not enter the live durable-feedback path")
	}
}

func TestOptionalRiotHistoryPanelNeverAcceptsBrowserCredential(t *testing.T) {
	javascript, err := fs.ReadFile(embeddedAssets, "assets/history.js")
	if err != nil {
		t.Fatal(err)
	}
	html, err := fs.ReadFile(embeddedAssets, "assets/index.html")
	if err != nil {
		t.Fatal(err)
	}
	source, markup := string(javascript), string(html)
	for _, required := range []string{
		`id="riot-history-title"`, `id="riot-link-form"`, `id="riot-consent"`,
		`id="riot-statistics-form"`, `id="riot-account-list"`, `id="riot-statistics-results"`,
		"No se guardan identificadores de oponentes", "Desactivado por defecto",
	} {
		if !strings.Contains(markup, required) {
			t.Fatalf("Riot history markup is missing %q", required)
		}
	}
	for _, required := range []string{
		`fetch("/api/riot/status"`, `fetch("/api/riot/accounts"`, `fetch("/api/riot/sync"`,
		`confirmation: "DELETE_RIOT_ACCOUNT_DATA"`, "fetch(`/api/history/statistics?${query}`",
		"normalizeRiotStatus", "normalizeStatistics", "replaceChildren",
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("Riot history source is missing %q", required)
		}
	}
	for _, forbidden := range []string{`id="riot-api-key"`, `name="api_key"`, "localStorage.setItem(\"riot", "sessionStorage.setItem(\"riot"} {
		if strings.Contains(markup+source, forbidden) {
			t.Fatalf("browser source contains credential/storage surface %q", forbidden)
		}
	}
	for _, platform := range []string{"ME1", "OC1"} {
		if !strings.Contains(markup, "<option>"+platform+"</option>") {
			t.Fatalf("documented platform %s missing from Riot account selector", platform)
		}
	}
}

func TestManualPregamePlanGuardsEveryAsyncOutcome(t *testing.T) {
	javascript, err := fs.ReadFile(embeddedAssets, "assets/app.js")
	if err != nil {
		t.Fatal(err)
	}
	source := string(javascript)
	start := strings.Index(source, "async function submitPregamePlan")
	if start < 0 {
		t.Fatal("cannot find manual pregame submit function")
	}
	end := strings.Index(source[start:], "function renderPregamePlan")
	if end < 0 {
		t.Fatal("cannot isolate manual pregame submit function")
	}
	body := source[start : start+end]
	sequence := []string{
		`const response = await fetch("/api/pregame/plan"`,
		"if (!isCurrentPregamePlanRequest(requestGeneration, optionsGeneration, optionsPatch))",
		"const payload = await response.json()",
		"if (!isCurrentPregamePlanRequest(requestGeneration, optionsGeneration, optionsPatch))",
		"if (!response.ok)",
	}
	offset := 0
	for _, fragment := range sequence {
		index := strings.Index(body[offset:], fragment)
		if index < 0 {
			t.Fatalf("manual plan outcome sequence is missing %q", fragment)
		}
		offset += index + len(fragment)
	}
	for _, guardedBranch := range []string{
		`} catch {
      if (!applyCurrentPregamePlanEffect(requestGeneration, optionsGeneration, optionsPatch, () => {`,
		`} finally {
      applyCurrentPregamePlanEffect(requestGeneration, optionsGeneration, optionsPatch, () => {`,
	} {
		if !strings.Contains(body, guardedBranch) {
			t.Fatalf("manual plan async branch is not generation-guarded: %q", guardedBranch)
		}
	}
}

func TestCatalogSourcePresentationContract(t *testing.T) {
	javascript, err := fs.ReadFile(embeddedAssets, "assets/app.js")
	if err != nil {
		t.Fatalf("read app.js: %v", err)
	}
	source := string(javascript)
	tests := []struct {
		name            string
		source          string
		label           string
		tone            string
		warningFragment string
	}{
		{name: "fresh remote", source: "remote", label: "Data Dragon", tone: "good"},
		{name: "disk cache", source: "cache", label: "Caché en disco", tone: "good"},
		{name: "memory cache", source: "memory_cache", label: "Caché en memoria", tone: "pending", warningFragment: "se mantiene el catálogo validado en memoria"},
		{name: "unpersisted remote", source: "remote_unpersisted", label: "Remoto sin guardar", tone: "pending", warningFragment: "El catálogo remoto actualizado está activo, pero no pudo guardarse localmente"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mapping := test.source + `: { label: "` + test.label + `", tone: "` + test.tone + `" }`
			if !strings.Contains(source, mapping) {
				t.Fatalf("app.js is missing catalog source mapping %q", mapping)
			}
			if test.warningFragment != "" && !strings.Contains(source, test.warningFragment) {
				t.Fatalf("app.js is missing warning %q", test.warningFragment)
			}
		})
	}
	if !strings.Contains(source, `catalog.fallback ? "pending" : source.tone`) {
		t.Fatal("catalog fallback tone must remain independent from source tone")
	}
}

func TestResponsiveSystemStatusRemainsVisible(t *testing.T) {
	stylesheet, err := fs.ReadFile(embeddedAssets, "assets/app.css")
	if err != nil {
		t.Fatalf("read app.css: %v", err)
	}
	css := string(stylesheet)
	for _, pattern := range []string{
		`(?s)(?:#tracker-pill|#catalog-pill|\.patch-chip|\.status-label)[^{]*\{[^}]*display:\s*none`,
	} {
		if regexp.MustCompile(pattern).MatchString(css) {
			t.Fatalf("responsive CSS hides required system provenance: %q", pattern)
		}
	}
	required := []string{
		`(?s)@media \(max-width: 1160px\).*?\.status-cluster\s*\{[^}]*flex-wrap:\s*wrap`,
		`(?s)@media \(max-width: 780px\).*?\.status-cluster\s*\{[^}]*display:\s*grid[^}]*grid-template-columns:\s*repeat\(2, minmax\(0, 1fr\)\)`,
		`(?s)@media \(max-width: 460px\).*?\.status-cluster\s*\{[^}]*grid-template-columns:\s*1fr`,
	}
	for _, pattern := range required {
		if !regexp.MustCompile(pattern).MatchString(css) {
			t.Fatalf("responsive CSS is missing status-layout contract %q", pattern)
		}
	}
}

func TestContextCardsUseInlineSizeQueries(t *testing.T) {
	stylesheet, err := fs.ReadFile(embeddedAssets, "assets/app.css")
	if err != nil {
		t.Fatalf("read app.css: %v", err)
	}
	css := string(stylesheet)
	required := []string{
		`(?s)\.context-column\s*\{[^}]*min-width:\s*0`,
		`(?s)\.context-column > \.panel\s*\{[^}]*min-width:\s*0[^}]*container:\s*context-card / inline-size`,
		`(?s)\.player-metrics\s*\{[^}]*grid-template-columns:\s*minmax\(0, 1fr\)`,
		`(?s)\.threat-metrics\s*\{[^}]*grid-template-columns:\s*minmax\(0, 1fr\)`,
		`(?s)@container context-card \(min-width: 260px\).*?\.player-metrics,\s*\.threat-metrics\s*\{[^}]*grid-template-columns:\s*repeat\(2, minmax\(0, 1fr\)\)`,
	}
	for _, pattern := range required {
		if !regexp.MustCompile(pattern).MatchString(css) {
			t.Fatalf("responsive context cards are missing contract %q", pattern)
		}
	}

	fourColumn := regexp.MustCompile(`(?s)@container context-card \(min-width: ([0-9]+)px\) \{\s*\.player-metrics,\s*\.threat-metrics\s*\{[^}]*grid-template-columns:\s*repeat\(4, minmax\(0, 1fr\)\)`).FindStringSubmatch(css)
	if len(fourColumn) != 2 {
		t.Fatal("responsive context cards are missing the four-column container query")
	}
	threshold, parseErr := strconv.Atoi(fourColumn[1])
	if parseErr != nil {
		t.Fatalf("parse four-column container threshold %q: %v", fourColumn[1], parseErr)
	}
	if threshold < 500 {
		t.Fatalf("four-column container threshold = %dpx, want at least 500px", threshold)
	}
}

func TestContextGridPreservesReadableMinimumCardWidths(t *testing.T) {
	stylesheet, err := fs.ReadFile(embeddedAssets, "assets/app.css")
	if err != nil {
		t.Fatalf("read app.css: %v", err)
	}
	css := string(stylesheet)
	pattern := `(?s)@media \(max-width: 1160px\).*?\.context-column\s*\{[^}]*grid-template-columns:\s*repeat\(auto-fit, minmax\(min\(20rem, 100%\), 1fr\)\)`
	if !regexp.MustCompile(pattern).MatchString(css) {
		t.Fatalf("responsive context grid is missing its auto-fit minimum-width contract %q", pattern)
	}
}

func TestMetricSeparatorsFollowContainerColumnCount(t *testing.T) {
	stylesheet, err := fs.ReadFile(embeddedAssets, "assets/app.css")
	if err != nil {
		t.Fatalf("read app.css: %v", err)
	}
	css := string(stylesheet)
	required := []string{
		`(?s)\.player-metrics > div\s*\{[^}]*min-width:\s*0[^}]*border-bottom:\s*1px solid var\(--line\)`,
		`(?s)\.threat-metrics > div \+ div\s*\{[^}]*border-top:\s*1px solid var\(--line\)`,
		`(?s)@container context-card \(min-width: 260px\).*?\.player-metrics > div:nth-child\(odd\)\s*\{[^}]*border-right:\s*1px solid var\(--line\)`,
		`(?s)@container context-card \(min-width: 260px\).*?\.player-metrics > div:nth-child\(-n \+ 2\)\s*\{[^}]*border-bottom:\s*1px solid var\(--line\)`,
		`(?s)@container context-card \(min-width: 260px\).*?\.threat-metrics > div \+ div\s*\{[^}]*border-top:\s*0`,
		`(?s)@container context-card \(min-width: 260px\).*?\.threat-metrics > div:nth-child\(even\)\s*\{[^}]*border-left:\s*1px solid var\(--line\)`,
		`(?s)@container context-card \(min-width: 260px\).*?\.threat-metrics > div:nth-child\(n \+ 3\)\s*\{[^}]*border-top:\s*1px solid var\(--line\)`,
		`(?s)@container context-card \(min-width: 500px\).*?\.player-metrics > div:last-child\s*\{[^}]*border-right:\s*0`,
	}
	for _, pattern := range required {
		if !regexp.MustCompile(pattern).MatchString(css) {
			t.Fatalf("responsive metric separators are missing contract %q", pattern)
		}
	}

	fourColumnStart := strings.Index(css, "@container context-card (min-width: 500px)")
	fourColumnEnd := -1
	if fourColumnStart >= 0 {
		if relativeEnd := strings.Index(css[fourColumnStart:], "\n.inventory-panel,"); relativeEnd >= 0 {
			fourColumnEnd = fourColumnStart + relativeEnd
		}
	}
	if fourColumnStart < 0 || fourColumnEnd <= fourColumnStart {
		t.Fatal("cannot isolate the four-column context-card query")
	}
	fourColumnCSS := css[fourColumnStart:fourColumnEnd]
	resetSelector := ".threat-metrics > div:nth-child(even),\n  .threat-metrics > div:nth-child(n + 3)"
	restoreSelector := ".threat-metrics > div:nth-child(n + 2)"
	resetIndex := strings.Index(fourColumnCSS, resetSelector)
	restoreIndex := strings.Index(fourColumnCSS, restoreSelector)
	if resetIndex < 0 || restoreIndex < 0 {
		t.Fatalf("four-column threat separators must use equal-specificity nth-child reset and restore selectors")
	}
	if restoreIndex <= resetIndex {
		t.Fatal("four-column threat divider restore must follow the nth-child reset in cascade order")
	}
	restoreRule := regexp.MustCompile(`(?s)\.threat-metrics > div:nth-child\(n \+ 2\)\s*\{[^}]*border-left:\s*1px solid var\(--line\)[^}]*padding-left:\s*11px`)
	if !restoreRule.MatchString(fourColumnCSS) {
		t.Fatal("four-column threat divider restore must reapply its border and spacing")
	}
}

func TestMetricTextUsesSafeWrappingFallbacks(t *testing.T) {
	stylesheet, err := fs.ReadFile(embeddedAssets, "assets/app.css")
	if err != nil {
		t.Fatalf("read app.css: %v", err)
	}
	css := string(stylesheet)
	for _, pattern := range []string{
		`(?s)\.player-metrics dt,\s*\.player-metrics dd\s*\{[^}]*overflow-wrap:\s*break-word[^}]*word-break:\s*normal`,
		`(?s)\.threat-metrics dt,\s*\.threat-metrics dd\s*\{[^}]*overflow-wrap:\s*break-word[^}]*word-break:\s*normal`,
	} {
		if !regexp.MustCompile(pattern).MatchString(css) {
			t.Fatalf("metric text is missing its safe wrapping contract %q", pattern)
		}
	}
	if strings.Contains(css, "overflow-wrap: anywhere") {
		t.Fatal("metric text must not opt into unconditional mid-word fragmentation")
	}
}
