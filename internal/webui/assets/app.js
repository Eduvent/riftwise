(() => {
  "use strict";

  const DDRAGON_CDN = "https://ddragon.leagueoflegends.com";
  const PATCH_PATTERN = /^\d+\.\d+\.\d+$/;
  const DEMO_MODE = new URLSearchParams(window.location.search).get("demo") === "1";
  const numberFormat = new Intl.NumberFormat("es-PE", { maximumFractionDigits: 0 });
  const ALERT_SETTINGS_KEY = "lol-engine:decision-alert-settings:v1";
  const ALERT_SEEN_KEY = "lol-engine:seen-decision-events:v1";
  const FEEDBACK_SUBMITTED_KEY = "lol-engine:submitted-feedback:v1";
  const ALERT_MODES = new Set(["off", "tone", "voice"]);
  const DEFAULT_ALERT_SETTINGS = Object.freeze({ mode: "off", volume: 0.5, muted: true, cooldownSeconds: 30 });
  const MAX_VISIBLE_TOASTS = 3;
  const MAX_DECISION_HISTORY = 20;
  const MAX_SEEN_DECISIONS = 200;
  const dashboardState = window.LoLEngineState;

  const elements = Object.freeze({
    demoBanner: byID("demo-banner"),
    liveStatusCluster: byID("live-status-cluster"),
    liveViewButton: byID("live-view-button"),
    pregameViewButton: byID("pregame-view-button"),
    liveView: byID("live-view"),
    pregameView: byID("pregame-view"),
    connectionPill: byID("connection-pill"),
    connectionStatus: byID("connection-status"),
    trackerPill: byID("tracker-pill"),
    trackerStatus: byID("tracker-status"),
    catalogPill: byID("catalog-pill"),
    catalogStatus: byID("catalog-status"),
    patchLabel: byID("patch-label"),
    systemAlert: byID("system-alert"),
    systemAlertText: byID("system-alert-text"),
    systemAlertDetails: byID("system-alert-details"),
    decisionAlertRegion: byID("decision-alert-region"),
    decisionHistoryList: byID("decision-history-list"),
    decisionHistoryStatus: byID("decision-history-status"),
    alertMode: byID("alert-mode"),
    alertVolume: byID("alert-volume"),
    alertVolumeValue: byID("alert-volume-value"),
    alertMuted: byID("alert-muted"),
    alertCooldown: byID("alert-cooldown"),
    personalizationForm: byID("personalization-form"),
    personalizationStyle: byID("personalization-style"),
    personalizationPreferred: byID("personalization-preferred"),
    personalizationAvoided: byID("personalization-avoided"),
    personalizationPreferredChips: byID("personalization-preferred-chips"),
    personalizationAvoidedChips: byID("personalization-avoided-chips"),
    personalizationFeedbackEnabled: byID("personalization-feedback-enabled"),
    personalizationSave: byID("personalization-save"),
    personalizationReset: byID("personalization-reset"),
    personalizationDeleteFeedback: byID("personalization-delete-feedback"),
    personalizationStatus: byID("personalization-status"),
    personalizationVersion: byID("personalization-version"),
    statePanel: byID("state-panel"),
    stateKicker: byID("state-kicker"),
    stateTitle: byID("state-title"),
    stateDetail: byID("state-detail"),
    stateSecondary: byID("state-secondary"),
    dashboard: byID("dashboard-content"),
    decisionStatus: byID("decision-status"),
    adviceFreshness: byID("advice-freshness"),
    purchaseCommand: byID("purchase-command"),
    nextIcon: byID("next-icon"),
    nextFallback: byID("next-fallback"),
    nextName: byID("next-name"),
    nextPrice: byID("next-price"),
    goldMessage: byID("gold-message"),
    currentGold: byID("current-gold"),
    goldRemaining: byID("gold-remaining"),
    recommendationReason: byID("recommendation-reason"),
    rankedChoiceList: byID("ranked-choice-list"),
    rankedChoiceCount: byID("ranked-choice-count"),
    recalculateButton: byID("recalculate-button"),
    recalculateLabel: byID("recalculate-label"),
    actionFeedback: byID("action-feedback"),
    playerState: byID("player-state"),
    playerChampion: byID("player-champion"),
    playerKDA: byID("player-kda"),
    playerLevel: byID("player-level"),
    playerCS: byID("player-cs"),
    confidenceLabel: byID("confidence-label"),
    confidenceMeter: byID("confidence-meter"),
    confidenceBar: byID("confidence-bar"),
    physicalLabel: byID("physical-label"),
    physicalMeter: byID("physical-meter"),
    physicalBar: byID("physical-bar"),
    magicLabel: byID("magic-label"),
    magicMeter: byID("magic-meter"),
    magicBar: byID("magic-bar"),
    trueLabel: byID("true-label"),
    trueMeter: byID("true-meter"),
    trueBar: byID("true-bar"),
    threatTitle: byID("threat-title"),
    threatIdentity: byID("threat-identity"),
    threatScore: byID("threat-score"),
    threatLevel: byID("threat-level"),
    threatKDA: byID("threat-kda"),
    threatCS: byID("threat-cs"),
    threatValue: byID("threat-value"),
    inventoryList: byID("inventory-list"),
    inventoryCount: byID("inventory-count"),
    mapName: byID("map-name"),
    gameMode: byID("game-mode"),
    gameTime: byID("game-time"),
    dashboardRevision: byID("dashboard-revision"),
    lastUpdate: byID("last-update"),
    pregameOptionsStatus: byID("pregame-options-status"),
    pregameForm: byID("pregame-form"),
    pregameErrorSummary: byID("pregame-error-summary"),
    pregameErrorList: byID("pregame-error-list"),
    pregameChampion: byID("pregame-champion"),
    pregameRole: byID("pregame-role"),
    pregameStage: byID("pregame-stage"),
    pregameStrategy: byID("pregame-strategy"),
    pregameLaneOpponent: byID("pregame-lane-opponent"),
    pregameGold: byID("pregame-gold"),
    pregamePressureSelects: Array.from(document.querySelectorAll("[data-pregame-pressure]")),
    pregameInventoryRows: byID("pregame-inventory-rows"),
    pregameSubmit: byID("pregame-submit"),
    pregameResult: byID("pregame-result"),
    pregameResultStatus: byID("pregame-result-status"),
    pregameResultReason: byID("pregame-result-reason"),
    pregameProfile: byID("pregame-profile"),
    pregamePatch: byID("pregame-patch"),
    pregameConfidence: byID("pregame-confidence"),
    pregameSource: byID("pregame-source"),
    pregameRankedOptions: byID("pregame-ranked-options"),
    pregameWarnings: byID("pregame-warnings"),
    pregameRunes: byID("pregame-runes"),
    pregameSummoners: byID("pregame-summoners"),
    pregameInitialPlan: byID("pregame-initial-plan"),
  });

  let dashboardVersion = { epoch: "", revision: -1, retiredEpochs: [] };
  let lastUpdatedAt = 0;
  let eventSource = null;
  let openedEventStream = false;
  let renderedDashboard = false;
  let connectionGeneration = 0;
  let streamOpen = false;
  let dashboardResyncRequired = false;
  let activeDashboardRequest = null;
  let currentTrackerStatus = "waiting_for_game";
  let currentSessionID = "";
  let currentLatestDecisionSequence = 0;
  let highestRESTDecisionSequence = 0;
  let decisionHistoryInFlight = false;
  let decisionHistoryRequestGeneration = 0;
  let loadedDecisionHistorySession = "";
  let alertSettings = loadAlertSettings();
  let seenDecisionIDs = loadSeenDecisionIDs();
  let renderedDecisionIDs = new Set();
  let audioContext = null;
  let alertGestureGranted = false;
  let lastAudibleAlertAt = 0;
  let activeView = "live";
  let pregameOptions = null;
  let pregameOptionsPromise = null;
  let pregameOptionsState = { generation: 0, expectedPatch: "", loadedPatch: "", ready: false };
  let activeCatalogPatch = "";
  let pregamePlanRequestGeneration = 0;
  let personalizationPreferences = dashboardState.normalizePersonalizationPreferences({});
  let personalizationMutation = { generation: 0, visible: personalizationPreferences, pending: false };
  let feedbackState = loadSubmittedFeedback();
  let latestDecisionEvent = {};
  let currentFeedbackRecommendation = {};
  let currentDecisionAvailability = {};
  let feedbackContextGeneration = 1;
  let feedbackContextKey = "";
  let personalizationLoadGeneration = 0;

  const stateCopy = Object.freeze({
    loading: {
      kicker: "INICIANDO",
      title: "Preparando el panel",
      detail: "Leyendo el estado local y el catálogo del parche.",
      secondary: "Esta pantalla se actualizará automáticamente.",
    },
    catalog_loading: {
      kicker: "CATÁLOGO",
      title: "Sincronizando el parche",
      detail: "Validando objetos, recetas y campeones antes de recomendar una compra.",
      secondary: "La primera sincronización necesita acceso a Data Dragon.",
    },
    waiting_for_game: {
      kicker: "LISTO PARA LEER",
      title: "Esperando una partida",
      detail: "Entra a una partida o a la Herramienta de práctica para activar el análisis.",
      secondary: "El launcher por sí solo no expone Live Client Data.",
    },
    catalog_unavailable: {
      kicker: "CATÁLOGO NO DISPONIBLE",
      title: "Aún no puedo validar objetos",
      detail: "No hay un catálogo completo del parche para producir una recomendación segura.",
      secondary: "Revisa la conexión y vuelve a iniciar el motor para sincronizar Data Dragon.",
    },
    unsupported: {
      kicker: "PARTIDA NO COMPATIBLE",
      title: "Esta sesión queda fuera del MVP",
      detail: "Por ahora el motor analiza a Garen en la Grieta del Invocador.",
      secondary: "No se mostrará una compra que no haya sido validada para este escenario.",
    },
    insufficient_context: {
      kicker: "CONTEXTO INSUFICIENTE",
      title: "Aún no hay suficientes opciones para comparar",
      detail: "El contexto actual no permite una comparación de objetos válida.",
      secondary: "No se sugiere una compra hasta contar con una opción principal y dos alternativas válidas.",
    },
    inventory_full: {
      kicker: "INVENTARIO LLENO",
      title: "No hay un espacio libre para otro componente",
      detail: "El siguiente componente ocuparía un séptimo espacio y no se combinaría de inmediato.",
      secondary: "No se presenta una compra imposible como si estuviera disponible.",
    },
    complete: {
      kicker: "RUTA COMPLETADA",
      title: "Tus objetivos prioritarios están listos",
      detail: "El motor no encontró otro objetivo de la ruta actual que debas completar.",
      secondary: "Sigue observando la partida; el contexto puede cambiar.",
    },
    stale: {
      kicker: "DATOS DESACTUALIZADOS",
      title: "Recomendación pausada",
      detail: "La conexión con la partida dejó de entregar datos recientes.",
      secondary: "La recomendación anterior se ocultó para no presentarla como vigente.",
    },
    error: {
      kicker: "NO SE PUDO CALCULAR",
      title: "Recomendación no disponible",
      detail: "El motor no pudo construir una recomendación válida en este momento.",
      secondary: "El panel seguirá intentando recuperarse con las próximas actualizaciones.",
    },
  });

  const demoSnapshot = createDemoSnapshot();

  function byID(id) {
    return document.getElementById(id);
  }

  function createDemoSnapshot() {
    const now = new Date().toISOString();
    return deepFreeze({
      revision: 42,
      epoch: "demo-epoch",
      updated_at: now,
      game: {
        status: "in_game",
        revision: 87,
        updated_at: now,
        last_success_at: now,
        game: {
          game_time_seconds: 1048,
          game_mode: "CLASSIC",
          game_version: "16.18.99.1",
          map_number: 11,
          map_name: "Summoner's Rift",
          active_player: {
            identity: { riot_id: "Demacia#LAS" },
            champion_name: "Garen",
            team: "ORDER",
            position: "TOP",
            level: 12,
            current_gold: 1120,
            score: { kills: 4, deaths: 3, assists: 2, creep_score: 139 },
            items: [
              { id: 1055, display_name: "Escudo de Doran", count: 1, price: 450, slot: 0 },
              { id: 3047, display_name: "Tabi de acero", count: 1, price: 1200, slot: 1 },
              { id: 3067, display_name: "Kindlegem", count: 1, price: 800, slot: 2 },
              { id: 1036, display_name: "Espada larga", count: 1, price: 350, slot: 3 },
            ],
          },
          players: [],
        },
      },
      catalog: {
        ready: true,
        patch: "16.18.1",
        locale: "es_MX",
        source: "remote",
        fallback: false,
        updated_at: now,
      },
      recommendation: {
        status: "ready",
        generated_at: now,
        source_game_revision: 87,
        patch: "16.18.1",
        player_state: "even",
        damage_mix: { physical: 0.24, magic: 0.68, true: 0.08 },
        main_threat: {
          riot_id: "Zorrita#MID",
          champion_name: "Ahri",
          score: 8.42,
          level: 13,
          kills: 7,
          deaths: 1,
          assists: 5,
          creep_score: 151,
          inventory_value: 7925,
        },
        next_component: { id: 1057, name: "Capa negatrón", cost: 850 },
        target_item: { id: 4401, name: "Fuerza de la naturaleza", cost: 2800 },
        alternative: { id: 3065, name: "Rostro espiritual", cost: 2900 },
        alternatives: [
          { item: { id: 3065, name: "Rostro espiritual", cost: 2900 }, score: 890, category: "defense" },
          { item: { id: 6665, name: "Jak'Sho, el Proteico", cost: 3200 }, score: 820, category: "defense" },
          { item: { id: 3053, name: "Calibrador de Sterak", cost: 3200 }, score: 760, category: "survival" },
        ],
        ranked_options: [
          { item: { id: 4401, name: "Fuerza de la naturaleza", cost: 2800 }, score: 960, category: "defense" },
          { item: { id: 3065, name: "Rostro espiritual", cost: 2900 }, score: 890, category: "defense" },
          { item: { id: 6665, name: "Jak'Sho, el Proteico", cost: 3200 }, score: 820, category: "defense" },
          { item: { id: 3053, name: "Calibrador de Sterak", cost: 3200 }, score: 760, category: "survival" },
        ],
        reason: "El daño enemigo es mayormente mágico; prioriza resistencia mágica contra Ahri.",
        confidence: 0.82,
        gold_needed: 0,
        inventory_slots_used: 4,
        warnings: [],
        sources: ["Live Client Data", "Data Dragon 16.18.1", "Heurística determinista local"],
      },
    });
  }

  function deepFreeze(value) {
    if (!value || typeof value !== "object" || Object.isFrozen(value)) {
      return value;
    }
    Object.getOwnPropertyNames(value).forEach((key) => deepFreeze(value[key]));
    return Object.freeze(value);
  }

  function setText(node, value, fallback = "—") {
    const text = value === null || value === undefined || value === "" ? fallback : String(value);
    node.textContent = text;
  }

  function finiteNumber(value, fallback = 0) {
    const number = Number(value);
    return Number.isFinite(number) ? number : fallback;
  }

  function wholeNumber(value, fallback = 0) {
    return Math.floor(finiteNumber(value, fallback));
  }

  function clamp(value, minimum = 0, maximum = 1) {
    return Math.min(maximum, Math.max(minimum, finiteNumber(value)));
  }

  function validatedAlertSettings(value) {
    value = objectValue(value);
    const source = value;
    const mode = ALERT_MODES.has(source.mode) ? source.mode : DEFAULT_ALERT_SETTINGS.mode;
    return {
      mode,
      volume: clamp(finiteNumber(value.volume, DEFAULT_ALERT_SETTINGS.volume), 0, 1),
      muted: mode === "off" || (typeof source.muted === "boolean" ? source.muted : DEFAULT_ALERT_SETTINGS.muted),
      cooldownSeconds: clamp(wholeNumber(value.cooldownSeconds, DEFAULT_ALERT_SETTINGS.cooldownSeconds), 5, 300),
    };
  }

  function loadAlertSettings() {
    try {
      const stored = localStorage.getItem(ALERT_SETTINGS_KEY);
      if (!stored) {
        return { ...DEFAULT_ALERT_SETTINGS };
      }
      return validatedAlertSettings(JSON.parse(stored));
    } catch {
      return { ...DEFAULT_ALERT_SETTINGS };
    }
  }

  function saveAlertSettings() {
    try {
      localStorage.setItem(ALERT_SETTINGS_KEY, JSON.stringify(alertSettings));
    } catch {
      // Private browsing or a full storage quota must not disable text alerts.
    }
  }

  function loadSeenDecisionIDs() {
    try {
      const stored = JSON.parse(sessionStorage.getItem(ALERT_SEEN_KEY) || "[]");
      if (!Array.isArray(stored)) {
        return new Set();
      }
      return new Set(stored.filter((id) => typeof id === "string" && id.trim()).slice(-MAX_SEEN_DECISIONS));
    } catch {
      return new Set();
    }
  }

  function rememberDecisionID(id) {
    seenDecisionIDs.add(id);
    while (seenDecisionIDs.size > MAX_SEEN_DECISIONS) {
      seenDecisionIDs.delete(seenDecisionIDs.values().next().value);
    }
    try {
      sessionStorage.setItem(ALERT_SEEN_KEY, JSON.stringify(Array.from(seenDecisionIDs)));
    } catch {
      // In-memory dedupe remains available when session storage is unavailable.
    }
  }

  function renderAlertSettings() {
    elements.alertMode.value = alertSettings.mode;
    elements.alertVolume.value = String(alertSettings.volume);
    setText(elements.alertVolumeValue, `${Math.round(alertSettings.volume * 100)}%`);
    elements.alertMuted.checked = alertSettings.muted;
    elements.alertCooldown.value = String(alertSettings.cooldownSeconds);
  }

  function configureAlertControls() {
    renderAlertSettings();
    elements.alertMode.addEventListener("change", () => {
      const mode = ALERT_MODES.has(elements.alertMode.value) ? elements.alertMode.value : "off";
      alertSettings = { ...alertSettings, mode, muted: mode === "off" };
      elements.alertMuted.checked = alertSettings.muted;
      saveAlertSettings();
      activateAlertsFromGesture();
    });
    elements.alertVolume.addEventListener("input", () => {
      alertSettings = { ...alertSettings, volume: clamp(finiteNumber(elements.alertVolume.value), 0, 1) };
      setText(elements.alertVolumeValue, `${Math.round(alertSettings.volume * 100)}%`);
      saveAlertSettings();
      activateAlertsFromGesture();
    });
    elements.alertMuted.addEventListener("change", () => {
      alertSettings = { ...alertSettings, muted: elements.alertMuted.checked };
      saveAlertSettings();
      if (!alertSettings.muted) {
        activateAlertsFromGesture();
      }
    });
    elements.alertCooldown.addEventListener("change", () => {
      alertSettings = {
        ...alertSettings,
        cooldownSeconds: clamp(wholeNumber(elements.alertCooldown.value), 5, 300),
      };
      elements.alertCooldown.value = String(alertSettings.cooldownSeconds);
      saveAlertSettings();
      activateAlertsFromGesture();
    });
  }

  async function activateAlertsFromGesture() {
    alertGestureGranted = true;
    if (alertSettings.mode !== "tone") {
      return;
    }
    const AudioContextConstructor = window.AudioContext || window.webkitAudioContext;
    if (!AudioContextConstructor) {
      return;
    }
    try {
      if (!audioContext) {
        audioContext = new AudioContextConstructor();
      }
      if (audioContext.state === "suspended") {
        await audioContext.resume();
      }
    } catch {
      audioContext = null;
    }
  }

  function playDecisionTone(event) {
    if (!audioContext || audioContext.state !== "running") {
      return;
    }
    try {
      const oscillator = audioContext.createOscillator();
      const gain = audioContext.createGain();
      oscillator.type = "sine";
      oscillator.frequency.value = event.importance === "critical" ? 720 : 560;
      gain.gain.setValueAtTime(Math.max(0.0001, alertSettings.volume * 0.13), audioContext.currentTime);
      gain.gain.exponentialRampToValueAtTime(0.0001, audioContext.currentTime + 0.24);
      oscillator.connect(gain);
      gain.connect(audioContext.destination);
      oscillator.start();
      oscillator.stop(audioContext.currentTime + 0.25);
    } catch {
      // Text remains the authoritative accessible fallback.
    }
  }

  function speakDecisionChange() {
    if (!("speechSynthesis" in window) || typeof window.SpeechSynthesisUtterance !== "function") {
      return;
    }
    try {
      const utterance = new window.SpeechSynthesisUtterance("Tus opciones de compra cambiaron.");
      utterance.lang = "es-MX";
      utterance.volume = alertSettings.volume;
      window.speechSynthesis.cancel();
      window.speechSynthesis.speak(utterance);
    } catch {
      // Text remains visible if synthesis is missing or fails.
    }
  }

  function maybePlayDecisionAlert(event) {
    if (activeView !== "live" || !alertGestureGranted || alertSettings.mode === "off" || alertSettings.muted ||
        currentTrackerStatus === "stale" || event.kind === "data_stale" ||
        (event.importance !== "important" && event.importance !== "critical")) {
      return;
    }
    const cooldownMilliseconds = alertSettings.cooldownSeconds * 1000;
    if (Date.now() - lastAudibleAlertAt < cooldownMilliseconds) {
      return;
    }
    lastAudibleAlertAt = Date.now();
    if (alertSettings.mode === "tone") {
      playDecisionTone(event);
    } else if (alertSettings.mode === "voice") {
      speakDecisionChange();
    }
  }

  function decisionKindLabel(kind) {
    const labels = {
      target_changed: "Opciones actualizadas",
      component_changed: "Componente actualizado",
      component_affordable: "Opción disponible",
      critical_counter_activated: "Necesidad crítica",
      inventory_full: "Inventario completo",
      data_stale: "Datos pausados",
      rules_unavailable: "Reglas no disponibles",
      connection_restored: "Conexión restablecida",
      return_to_base_options: "Opciones al volver a base",
    };
    return labels[kind] || "Cambio de opciones";
  }

  function handleDecisionEvent(value, options = {}) {
    const event = objectValue(value);
    if (!dashboardState.acceptsDecisionSession(currentSessionID, event.session_id)) {
      return;
    }
    const id = typeof event.id === "string" ? event.id.trim() : "";
    const summary = typeof event.current_summary === "string" ? event.current_summary.trim() : "";
    if (!id || !summary) {
      return;
    }
    if (!renderedDecisionIDs.has(id)) {
      addDecisionHistory(event);
      renderedDecisionIDs.add(id);
    }
    const historic = options.historic === true;
    const duplicate = seenDecisionIDs.has(id);
    if (historic) {
      rememberDecisionID(id);
      return;
    }
    if (duplicate) {
      return;
    }
    rememberDecisionID(id);
    if (activeView === "live" && (event.importance === "important" || event.importance === "critical")) {
      showDecisionToast(event);
    }
    maybePlayDecisionAlert(event);
  }

  function addDecisionHistory(event) {
    const item = document.createElement("li");
    item.className = "decision-history-item";
    const heading = document.createElement("strong");
    heading.textContent = decisionKindLabel(event.kind);
    const summary = document.createElement("p");
    summary.textContent = event.current_summary;
    const timestamp = document.createElement("time");
    const parsed = Date.parse(event.occurred_at);
    timestamp.textContent = Number.isFinite(parsed) ? new Date(parsed).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" }) : "Ahora";
    if (Number.isFinite(parsed)) {
      timestamp.dateTime = new Date(parsed).toISOString();
    }
    item.append(heading, summary, timestamp);
    elements.decisionHistoryList.prepend(item);
    while (elements.decisionHistoryList.children.length > MAX_DECISION_HISTORY) {
      elements.decisionHistoryList.lastElementChild.remove();
    }
    const count = elements.decisionHistoryList.children.length;
    setText(elements.decisionHistoryStatus, count === 1 ? "1 aviso en esta sesión" : `${count} avisos en esta sesión`);
  }

  function showDecisionToast(event) {
    const toast = document.createElement("article");
    toast.className = "decision-toast";
    toast.dataset.importance = event.importance;
    const copy = document.createElement("div");
    const title = document.createElement("strong");
    title.textContent = decisionKindLabel(event.kind);
    const summary = document.createElement("p");
    summary.textContent = event.current_summary;
    copy.append(title, summary);
    const dismiss = document.createElement("button");
    dismiss.type = "button";
    dismiss.className = "toast-dismiss";
    dismiss.textContent = "Cerrar";
    dismiss.setAttribute("aria-label", `Cerrar aviso: ${title.textContent}`);
    dismiss.addEventListener("click", () => toast.remove());
    toast.append(copy, dismiss);
    elements.decisionAlertRegion.prepend(toast);
    while (elements.decisionAlertRegion.children.length > MAX_VISIBLE_TOASTS) {
      elements.decisionAlertRegion.lastElementChild.remove();
    }
    window.setTimeout(() => toast.remove(), 12000);
  }

  function synchronizeDecisionHistory(sessionID, latestSequence = 0) {
    sessionID = typeof sessionID === "string" ? sessionID.trim() : "";
    const sessionChanged = sessionID !== currentSessionID;
    const safeLatestSequence = Math.max(0, wholeNumber(latestSequence));
    currentLatestDecisionSequence = sessionChanged ? safeLatestSequence :
      Math.max(currentLatestDecisionSequence, safeLatestSequence);
    if (!sessionChanged) {
      if (sessionID && loadedDecisionHistorySession !== sessionID && !decisionHistoryInFlight) {
        loadDecisionHistory(sessionID);
      } else {
        reconcileDecisionHistory(sessionID, currentLatestDecisionSequence);
      }
      return;
    }
    currentSessionID = sessionID;
    feedbackState = dashboardState.feedbackStateForSession(feedbackState, sessionID);
    saveSubmittedFeedback();
    latestDecisionEvent = {};
    loadedDecisionHistorySession = "";
    highestRESTDecisionSequence = 0;
    decisionHistoryInFlight = false;
    decisionHistoryRequestGeneration += 1;
    renderedDecisionIDs = new Set();
    elements.decisionAlertRegion.replaceChildren();
    elements.decisionHistoryList.replaceChildren();
    lastAudibleAlertAt = 0;
    setText(elements.decisionHistoryStatus, sessionID ? "Cargando historial" : "Sin avisos en esta sesión");
    if (sessionID) {
      loadDecisionHistory(sessionID);
    }
  }

  function reconcileDecisionHistory(sessionID, latestSequence) {
    if (dashboardState.shouldReconcileHistory({
      currentSessionID,
      loadedSessionID: loadedDecisionHistorySession,
      dashboardSessionID: sessionID,
      highestRESTSequence: highestRESTDecisionSequence,
      dashboardLatestSequence: latestSequence,
      inFlight: decisionHistoryInFlight,
    })) {
      loadDecisionHistory(sessionID, true);
    }
  }

  async function loadDecisionHistory(sessionID, force = false) {
    if (!sessionID || decisionHistoryInFlight || (!force && loadedDecisionHistorySession === sessionID)) {
      return;
    }
    const requestGeneration = ++decisionHistoryRequestGeneration;
    const requestedThrough = currentLatestDecisionSequence;
    decisionHistoryInFlight = true;
    try {
      const after = Math.max(0, currentLatestDecisionSequence - 50);
      const response = await fetch(`/api/matches/${encodeURIComponent(sessionID)}/decision-events?after=${after}&limit=50`, {
        method: "GET", cache: "no-store", headers: { Accept: "application/json" },
      });
      const payload = await responseJSON(response);
      if (sessionID !== currentSessionID || requestGeneration !== decisionHistoryRequestGeneration) {
        return;
      }
      const events = Array.isArray(payload.events) ? payload.events : [];
      events.forEach((event) => handleDecisionEvent(event, { historic: true }));
      highestRESTDecisionSequence = events.reduce((highest, event) => {
        if (!dashboardState.acceptsDecisionSession(sessionID, event.session_id)) {
          return highest;
        }
        return Math.max(highest, Math.max(0, wholeNumber(event.sequence)));
      }, highestRESTDecisionSequence);
      loadedDecisionHistorySession = sessionID;
      if (events.length === 0) {
        setText(elements.decisionHistoryStatus, "Sin avisos en esta sesión");
      }
    } catch {
      if (sessionID === currentSessionID) {
        setText(elements.decisionHistoryStatus, "Historial no disponible");
      }
    } finally {
      if (requestGeneration === decisionHistoryRequestGeneration) {
        decisionHistoryInFlight = false;
        if (sessionID === currentSessionID && currentLatestDecisionSequence > requestedThrough) {
          reconcileDecisionHistory(sessionID, currentLatestDecisionSequence);
        }
      }
    }
  }

  function formatGold(value) {
    return `${numberFormat.format(Math.max(0, wholeNumber(value)))} oro`;
  }

  function formatKDA(score) {
    const safe = score && typeof score === "object" ? score : {};
    return `${wholeNumber(safe.kills)} / ${wholeNumber(safe.deaths)} / ${wholeNumber(safe.assists)}`;
  }

  function formatGameTime(seconds) {
    const safe = Math.max(0, wholeNumber(seconds));
    const minutes = Math.floor(safe / 60);
    const remainder = String(safe % 60).padStart(2, "0");
    return `${minutes}:${remainder}`;
  }

  function formatRelativeTime(timestamp) {
    if (!timestamp) {
      return "—";
    }
    const elapsed = Math.max(0, Math.floor((Date.now() - timestamp) / 1000));
    if (elapsed <= 2) {
      return "Ahora";
    }
    if (elapsed < 60) {
      return `Hace ${elapsed} s`;
    }
    const minutes = Math.floor(elapsed / 60);
    if (minutes < 60) {
      return `Hace ${minutes} min`;
    }
    return new Date(timestamp).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
  }

  function setConnection(phase) {
    const phases = {
      connecting: { label: "Conectando", tone: "pending" },
      live: { label: "En vivo", tone: "good" },
      reconnecting: { label: "Reconectando", tone: "bad" },
      demo: { label: "Demo local", tone: "pending" },
    };
    const selected = phases[phase] || phases.connecting;
    setText(elements.connectionStatus, selected.label);
    elements.connectionPill.dataset.tone = selected.tone;
  }

  function trackerPresentation(status) {
    switch (status) {
      case "in_game":
        return { label: "En partida", tone: "good" };
      case "stale":
        return { label: "Desactualizada", tone: "bad" };
      case "waiting_for_game":
        return { label: "En espera", tone: "pending" };
      default:
        return { label: "Verificando", tone: "neutral" };
    }
  }

  function updateSystemStatus(snapshot) {
    const game = objectValue(snapshot.game);
    const catalog = objectValue(snapshot.catalog);
    const tracker = trackerPresentation(game.status);
    setText(elements.trackerStatus, tracker.label);
    elements.trackerPill.dataset.tone = tracker.tone;

    if (catalog.ready) {
      const sourcePresentations = {
        remote: { label: "Data Dragon", tone: "good" },
        cache: { label: "Caché en disco", tone: "good" },
        memory_cache: { label: "Caché en memoria", tone: "pending" },
        remote_unpersisted: { label: "Remoto sin guardar", tone: "pending" },
      };
      const source = sourcePresentations[catalog.source] || { label: "Listo", tone: "neutral" };
      setText(elements.catalogStatus, source.label);
      elements.catalogPill.dataset.tone = catalog.fallback ? "pending" : source.tone;
    } else if (catalog.source === "syncing" || catalog.source === "loading") {
      setText(elements.catalogStatus, "Sincronizando");
      elements.catalogPill.dataset.tone = "pending";
    } else {
      setText(elements.catalogStatus, "No disponible");
      elements.catalogPill.dataset.tone = "bad";
    }

    const recommendation = objectValue(snapshot.recommendation);
    const patch = validPatch(recommendation.patch) || validPatch(catalog.patch);
    setText(elements.patchLabel, patch || "—");
  }

  function catalogWarnings(catalogValue) {
    const catalog = objectValue(catalogValue);
    const warnings = [];
    if (catalog.source === "memory_cache") {
      warnings.push("No se pudo actualizar el parche en línea; se mantiene el catálogo validado en memoria.");
    }
    if (catalog.source === "remote_unpersisted") {
      warnings.push("El catálogo remoto actualizado está activo, pero no pudo guardarse localmente y podría perderse al reiniciar.");
    }
    if (catalog.fallback) {
      warnings.push("El parche exacto no estaba disponible; se seleccionó el catálogo compatible más reciente.");
    }
    return warnings;
  }

  function objectValue(value) {
    return value && typeof value === "object" && !Array.isArray(value) ? value : {};
  }

  function validPatch(value) {
    const patch = typeof value === "string" ? value.trim() : "";
    return PATCH_PATTERN.test(patch) ? patch : "";
  }

  function validItemID(value) {
    const id = Number(value);
    return Number.isSafeInteger(id) && id > 0 && id <= 9999999 ? id : 0;
  }

  function itemIconURL(patch, itemID) {
    const safePatch = validPatch(patch);
    const safeID = validItemID(itemID);
    if (!safePatch || !safeID) {
      return "";
    }
    return `${DDRAGON_CDN}/cdn/${safePatch}/img/item/${safeID}.png`;
  }

  function abbreviation(name) {
    const words = typeof name === "string" ? name.trim().split(/\s+/).filter(Boolean) : [];
    if (words.length === 0) {
      return "?";
    }
    return words.slice(0, 2).map((word) => word.charAt(0)).join("").toLocaleUpperCase("es");
  }

  function renderImage(image, fallback, item, patch, priority) {
    const safeItem = objectValue(item);
    const name = typeof safeItem.name === "string" ? safeItem.name :
      (typeof safeItem.display_name === "string" ? safeItem.display_name : "Objeto");
    const url = itemIconURL(patch, safeItem.id);

    image.onload = null;
    image.onerror = null;
    image.removeAttribute("src");
    image.hidden = true;
    image.alt = name;
    fallback.hidden = false;
    setText(fallback, abbreviation(name), "?");

    if (!url) {
      return;
    }
    image.loading = priority ? "eager" : "lazy";
    image.fetchPriority = priority ? "high" : "auto";
    image.decoding = "async";
    image.referrerPolicy = "no-referrer";
    image.onload = () => {
      image.hidden = false;
      fallback.hidden = true;
    };
    image.onerror = () => {
      image.hidden = true;
      fallback.hidden = false;
      image.removeAttribute("src");
    };
    image.src = url;
  }

  function clearImage(image, fallback) {
    image.onload = null;
    image.onerror = null;
    image.removeAttribute("src");
    image.hidden = true;
    image.alt = "";
    fallback.hidden = false;
    setText(fallback, "?");
  }

  function deriveViewState(snapshot) {
    const gameSnapshot = objectValue(snapshot.game);
    const catalog = objectValue(snapshot.catalog);
    const recommendation = objectValue(snapshot.recommendation);

    // Retained telemetry can be stale after the lifecycle has confirmed an end.
    // A waiting connection alone must never turn an active outage into postgame.
    if (objectValue(gameSnapshot.session).state === "ended" && gameSnapshot.connection === "waiting") {
      return "waiting_for_game";
    }
    if (gameSnapshot.status === "stale" || recommendation.status === "stale") {
      return "stale";
    }
    if (gameSnapshot.status === "waiting_for_game" || recommendation.status === "waiting") {
      return "waiting_for_game";
    }
    if (!catalog.ready) {
      return catalog.source === "syncing" || catalog.source === "loading" ? "catalog_loading" : "catalog_unavailable";
    }
    switch (recommendation.status) {
      case "ready":
      case "save_for_component":
      case "inventory_full":
      case "complete":
      case "unsupported":
      case "insufficient_context":
      case "error":
        return recommendation.status;
      case "catalog_unavailable":
        return "catalog_unavailable";
      default:
        return "error";
    }
  }

  function renderDashboard(snapshot) {
    if (!snapshot || typeof snapshot !== "object" || Array.isArray(snapshot)) {
      showState("error", null, "El servidor devolvió una respuesta que el panel no reconoce.");
      return false;
    }
    const revision = wholeNumber(snapshot.revision, 0);
    const version = dashboardState.evaluateDashboardVersion(dashboardVersion, {
      epoch: snapshot.epoch,
      revision,
    });
    if (!version.accept) {
      return false;
    }
    if (version.epochChanged) {
      resetForServerEpoch();
    }
    dashboardVersion = version.state;
    synchronizePregameCatalogPatch(objectValue(snapshot.catalog).patch);
    renderedDashboard = true;
    updateSystemStatus(snapshot);
    updateTimestamps(snapshot);
    const gameSnapshot = objectValue(snapshot.game);
    currentTrackerStatus = typeof gameSnapshot.status === "string" ? gameSnapshot.status : "waiting_for_game";
    const session = objectValue(gameSnapshot.session);
    const decisionAvailability = objectValue(snapshot.decision_events);
    const latestDecision = objectValue(decisionAvailability.latest);
    synchronizeDecisionHistory(
      typeof session.id === "string" ? session.id : "",
      latestDecision.sequence,
    );
    latestDecisionEvent = dashboardState.acceptsDecisionSession(currentSessionID, latestDecision.session_id) ? latestDecision : {};

    const recommendation = objectValue(snapshot.recommendation);
    synchronizeFeedbackContext(currentSessionID, decisionAvailability, latestDecisionEvent, recommendation);

    const viewState = deriveViewState(snapshot);
    if (viewState !== "ready" && viewState !== "save_for_component") {
      const detailOverride = viewState === "waiting_for_game" && recommendation.status === "stale" ?
        stateCopy.waiting_for_game.detail : undefined;
      showState(viewState, snapshot, detailOverride);
      return true;
    }

    if (!recommendation.next_component || recommendationChoices(recommendation).length < 2) {
      showState("error", snapshot, "La recomendación recibida está incompleta y se ocultó por seguridad.");
      return false;
    }

    elements.statePanel.hidden = true;
    elements.dashboard.hidden = false;
    renderRecommendation(snapshot, viewState);
    synchronizeAdviceAvailability();
    return true;
  }

  function resetForServerEpoch() {
    currentTrackerStatus = "waiting_for_game";
    currentSessionID = "";
    feedbackState = dashboardState.feedbackStateForSession(feedbackState, "");
    latestDecisionEvent = {};
    currentFeedbackRecommendation = {};
    currentDecisionAvailability = {};
    feedbackContextGeneration += 1;
    feedbackContextKey = "";
    currentLatestDecisionSequence = 0;
    highestRESTDecisionSequence = 0;
    loadedDecisionHistorySession = "";
    decisionHistoryInFlight = false;
    decisionHistoryRequestGeneration += 1;
    renderedDecisionIDs = new Set();
    renderedDashboard = false;
    lastUpdatedAt = 0;
    lastAudibleAlertAt = 0;
    activeCatalogPatch = "";
    invalidatePregameOptions("", "El servidor se reinició; las opciones manuales deben validarse otra vez.");
    elements.decisionAlertRegion.replaceChildren();
    elements.decisionHistoryList.replaceChildren();
    setText(elements.decisionHistoryStatus, "Sin avisos en esta sesión");
    showState("loading", null, "El servidor se reinició; esperando una instantánea vigente.");
  }

  function showState(state, snapshot, detailOverride) {
    const copy = stateCopy[state] || stateCopy.error;
    const recommendation = objectValue(snapshot && snapshot.recommendation);
    const catalog = objectValue(snapshot && snapshot.catalog);
    const recommendationWarnings = Array.isArray(recommendation.warnings) ? recommendation.warnings
      .filter((warning) => typeof warning === "string" && warning.trim()) : [];
    const warningDetails = Array.from(new Set([...recommendationWarnings, ...catalogWarnings(catalog)]));
    elements.dashboard.hidden = true;
    elements.dashboard.classList.remove("is-suspended");
    elements.statePanel.hidden = false;
    elements.statePanel.dataset.state = state;
    setText(elements.stateKicker, copy.kicker);
    setText(elements.stateTitle, copy.title);
    setText(elements.stateDetail, detailOverride || recommendation.reason || copy.detail);
    setText(elements.stateSecondary, copy.secondary);
    clearRecommendation();

    if (state === "stale") {
      showAlert("Los datos dejaron de estar vigentes; la compra anterior permanece oculta.", "error", warningDetails);
    } else if (state === "catalog_unavailable") {
      showAlert("Sin un catálogo validado no se generan sugerencias de compra.", "warning", warningDetails);
    } else if (state === "error") {
      showAlert("El motor seguirá escuchando actualizaciones para recuperarse automáticamente.", "error", warningDetails);
    } else if (warningDetails.length > 0) {
      showAlert("Detalles de la recomendación:", "warning", warningDetails);
    } else {
      hideAlert();
    }
  }

  function clearRecommendation() {
    setText(elements.nextName, "—");
    setText(elements.recommendationReason, "—");
    clearImage(elements.nextIcon, elements.nextFallback);
    elements.rankedChoiceList.replaceChildren();
    setText(elements.rankedChoiceCount, "—");
    elements.inventoryList.replaceChildren();
  }

  function renderRecommendation(snapshot, viewState) {
    const gameSnapshot = objectValue(snapshot.game);
    const game = objectValue(gameSnapshot.game);
    const activePlayer = objectValue(game.active_player);
    const recommendation = objectValue(snapshot.recommendation);
    const patch = validPatch(recommendation.patch) || validPatch(objectValue(snapshot.catalog).patch);
    const next = objectValue(recommendation.next_component);
    const currentGold = Math.max(0, wholeNumber(activePlayer.current_gold));
    const componentCost = Math.max(0, wholeNumber(next.cost));
    const goldNeeded = Math.max(0, wholeNumber(recommendation.gold_needed, componentCost - currentGold));
    const saving = viewState === "save_for_component";

    elements.decisionStatus.dataset.state = saving ? "save" : "buy";
    setText(elements.decisionStatus, saving ? "PLAN DE AHORRO" : "OPCIÓN DISPONIBLE");
    setText(elements.purchaseCommand, saving ? "Componente para comparar" : "Componente sugerido");
    setText(elements.nextName, next.name, "Componente sin nombre");
    setText(elements.nextPrice, formatGold(componentCost));
    setText(elements.currentGold, formatGold(currentGold));
    setText(elements.recommendationReason, recommendation.reason, "Recomendación determinista local.");

    if (saving) {
      setText(elements.goldMessage, `Te faltan ${formatGold(goldNeeded)}`);
      setText(elements.goldRemaining, `Faltan ${formatGold(goldNeeded)}`);
    } else {
      setText(elements.goldMessage, "Disponible en la tienda");
      setText(elements.goldRemaining, `${formatGold(Math.max(0, currentGold - componentCost))} después`);
    }

    renderImage(elements.nextIcon, elements.nextFallback, next, patch, true);
    renderRankedChoices(recommendation, patch);
    renderPlayer(activePlayer, recommendation);
    renderDamageMix(recommendation.damage_mix);
    renderThreat(recommendation.main_threat);
    renderInventory(activePlayer.items, patch, recommendation.inventory_slots_used);
    renderSession(snapshot, game);

    const warnings = Array.isArray(recommendation.warnings) ? recommendation.warnings.filter((value) => typeof value === "string" && value.trim()) : [];
    const catalog = objectValue(snapshot.catalog);
    const alertDetails = Array.from(new Set([...warnings, ...catalogWarnings(catalog)]));
    if (alertDetails.length > 0) {
      showAlert("Advertencias asociadas con estas opciones:", "warning", alertDetails);
    } else {
      hideAlert();
    }
  }

  function recommendationChoices(recommendation) {
    const choices = [];
    const seen = new Set();
    const append = (value, primary = false) => {
      const ranked = objectValue(value);
      const item = Object.keys(objectValue(ranked.item)).length > 0 ? objectValue(ranked.item) : ranked;
      const id = validItemID(item.id);
      if (!id || seen.has(id)) {
        return;
      }
      seen.add(id);
      choices.push({
        item,
        score: finiteNumber(ranked.score, NaN),
        category: typeof ranked.category === "string" ? ranked.category : "",
        primary,
      });
    };

    const rankedOptions = Array.isArray(recommendation.ranked_options) ? recommendation.ranked_options : [];
    rankedOptions.forEach((option, index) => append(option, index === 0));
    append(recommendation.target_item, choices.length === 0);
    const alternatives = Array.isArray(recommendation.alternatives) ? recommendation.alternatives : [];
    alternatives.forEach((option) => append(option));
    append(recommendation.alternative);
    return choices;
  }

  function renderRankedChoices(recommendation, patch) {
    const choices = recommendationChoices(recommendation);
    const cards = choices.map((choice, index) => createRankedChoice(choice, index, patch, true));
    elements.rankedChoiceList.replaceChildren(...cards);
    const alternatives = Math.max(choices.length - 1, 0);
    setText(elements.rankedChoiceCount, alternatives === 1 ? "1 alternativa" : `${alternatives} alternativas`);
  }

  function createRankedChoice(choice, index, patch, allowLiveFeedback = false) {
    const item = objectValue(choice.item);
    const card = document.createElement("li");
    card.className = index === 0 ? "destination-card target-card" : "destination-card alternative-card";
    if (index === 0) {
      card.setAttribute("aria-current", "true");
    }

    const label = document.createElement("p");
    label.textContent = index === 0 ? "OPCIÓN PRINCIPAL" : `ALTERNATIVA ${index}`;
    const content = document.createElement("div");
    content.className = "destination-content";
    const art = document.createElement("div");
    art.className = "item-art item-art-sm";
    const image = document.createElement("img");
    image.width = 48;
    image.height = 48;
    image.hidden = true;
    const fallback = document.createElement("span");
    fallback.className = "item-fallback";
    fallback.setAttribute("aria-hidden", "true");
    art.append(image, fallback);
    renderImage(image, fallback, item, patch, false);

    const copy = document.createElement("span");
    const name = document.createElement("strong");
    name.textContent = typeof item.name === "string" && item.name.trim() ? item.name : "Objeto sin nombre";
    const detail = document.createElement("small");
    const score = Number.isFinite(choice.score) ? ` · puntuación ${Math.round(choice.score)}` : "";
    detail.textContent = `${formatGold(item.cost)}${score}`;
    copy.append(name, detail);
    content.append(art, copy);
    card.append(label, content);
    if (allowLiveFeedback) {
      appendFeedbackControls(card, item);
    }
    return card;
  }

  function loadSubmittedFeedback() {
    try {
      const parsed = JSON.parse(sessionStorage.getItem(FEEDBACK_SUBMITTED_KEY) || "{}");
      return dashboardState.feedbackStateForSession(parsed, parsed.sessionID || "");
    } catch {
      return { sessionID: "", submitted: {}, pending: {} };
    }
  }

  function saveSubmittedFeedback() {
    try {
      sessionStorage.setItem(FEEDBACK_SUBMITTED_KEY, JSON.stringify(feedbackState));
    } catch {
      // Text feedback remains available if storage is unavailable.
    }
  }

  function eligibleFeedbackChoice(itemID) {
    if (!personalizationPreferences.feedback_enabled || !currentSessionID ||
        currentDecisionAvailability.status !== "ready" ||
        !dashboardState.acceptsDecisionSession(currentSessionID, latestDecisionEvent.session_id) ||
        !dashboardState.feedbackEvidenceMatchesRecommendation(latestDecisionEvent, currentFeedbackRecommendation)) {
      return false;
    }
    const evidence = objectValue(latestDecisionEvent.evidence);
    return (Array.isArray(evidence.ranked_choices) ? evidence.ranked_choices : []).some((choiceValue) => {
      const choice = objectValue(choiceValue);
      return validItemID(objectValue(choice.item).id) === itemID;
    });
  }

  function synchronizeFeedbackContext(sessionID, availabilityValue, eventValue, recommendationValue) {
    const availability = objectValue(availabilityValue);
    const event = objectValue(eventValue);
    const recommendation = objectValue(recommendationValue);
    const ranked = recommendationChoices(recommendation).map((choice) => validItemID(objectValue(choice.item).id));
    const nextKey = JSON.stringify({
      session: sessionID,
      decision: typeof event.id === "string" ? event.id : "",
      status: recommendation.status,
      patch: recommendation.patch,
      phase: recommendation.phase,
      strategy: recommendation.strategy,
      rules: objectValue(recommendation.rules),
      profile: objectValue(recommendation.profile),
      personalization: objectValue(recommendation.personalization),
      ranked,
      decisionStatus: availability.status,
    });
    if (nextKey !== feedbackContextKey) {
      feedbackContextKey = nextKey;
      feedbackContextGeneration += 1;
    }
    currentDecisionAvailability = availability;
    currentFeedbackRecommendation = recommendation;
  }

  function appendFeedbackControls(card, item) {
    const itemID = validItemID(item.id);
    if (!itemID || !eligibleFeedbackChoice(itemID)) {
      return;
    }
    const decisionID = typeof latestDecisionEvent.id === "string" ? latestDecisionEvent.id : "";
    const key = dashboardState.feedbackChoiceKey(currentSessionID, decisionID, itemID);
    if (!key) {
      return;
    }
    const controls = document.createElement("div");
    controls.className = "feedback-controls";
    controls.setAttribute("aria-label", `Valorar ${typeof item.name === "string" ? item.name : "esta opción"}`);
    const useful = document.createElement("button");
    useful.type = "button";
    useful.textContent = "Útil";
    const notUseful = document.createElement("button");
    notUseful.type = "button";
    notUseful.textContent = "No útil";
    const alreadySubmitted = Boolean(feedbackState.submitted[key]);
    useful.disabled = alreadySubmitted;
    notUseful.disabled = alreadySubmitted;
    if (alreadySubmitted) {
      controls.setAttribute("data-submitted", "true");
    }
    useful.addEventListener("click", () => submitChoiceFeedback(key, decisionID, itemID, "useful", controls));
    notUseful.addEventListener("click", () => submitChoiceFeedback(key, decisionID, itemID, "not_useful", controls));
    controls.append(useful, notUseful);
    card.append(controls);
  }

  async function submitChoiceFeedback(key, decisionID, itemID, value, controls) {
    if (feedbackState.submitted[key]) {
      setText(elements.personalizationStatus, "Esta opción ya fue valorada para esta decisión.");
      return;
    }
    const candidateClientID = typeof crypto.randomUUID === "function" ? crypto.randomUUID() :
      `browser-${Date.now()}-${Math.random().toString(16).slice(2)}`;
    const requestGeneration = feedbackContextGeneration;
    const begun = dashboardState.beginFeedbackSubmission(feedbackState, {
      key, clientID: candidateClientID, decisionEventID: decisionID, itemID, value,
      sessionID: currentSessionID, generation: requestGeneration,
    });
    if (!begun.accepted || !begun.request) {
      return;
    }
    feedbackState = begun.state;
    saveSubmittedFeedback();
    const pending = begun.request;
    Array.from(controls.querySelectorAll("button")).forEach((button) => { button.disabled = true; });
    setText(elements.personalizationStatus, begun.reused ? "Reintentando la misma valoración local…" : "Guardando valoración local…");
    try {
      const response = await fetch("/api/personalization/feedback", {
        method: "POST", cache: "no-store", headers: { Accept: "application/json", "Content-Type": "application/json" },
        body: JSON.stringify({
          client_feedback_id: pending.clientID, decision_event_id: pending.decisionEventID,
          item_id: pending.itemID, value: pending.value,
        }),
      });
      await responseJSON(response);
      const settled = dashboardState.settleFeedbackSubmission(feedbackState, {
        key, clientID: pending.clientID, sessionID: pending.sessionID,
        generation: pending.generation, outcome: "success",
      });
      if (!settled.apply) {
        return;
      }
      feedbackState = settled.state;
      saveSubmittedFeedback();
      if (!dashboardState.canApplyFeedbackEffect(pending, currentSessionID, feedbackContextGeneration) || !eligibleFeedbackChoice(itemID)) {
        return;
      }
      controls.dataset.submitted = "true";
      setText(elements.personalizationStatus, "Valoración local guardada. No se usa para inferir resultados.");
    } catch (error) {
      const outcome = error && error.authoritative === true ? "rejected" : "ambiguous";
      const settled = dashboardState.settleFeedbackSubmission(feedbackState, {
        key, clientID: pending.clientID, sessionID: pending.sessionID,
        generation: pending.generation, outcome,
      });
      if (!settled.apply) {
        return;
      }
      feedbackState = settled.state;
      saveSubmittedFeedback();
      if (!dashboardState.canApplyFeedbackEffect(pending, currentSessionID, feedbackContextGeneration)) {
        return;
      }
      Array.from(controls.querySelectorAll("button")).forEach((button) => { button.disabled = false; });
      setText(elements.personalizationStatus, outcome === "ambiguous" ?
        "No se confirmó la respuesta; reintentar reutilizará exactamente la misma valoración." :
        "La valoración fue rechazada y no se aplicó.");
    }
  }

  function renderPlayer(activePlayer, recommendation) {
    const score = objectValue(activePlayer.score);
    const state = typeof recommendation.player_state === "string" ? recommendation.player_state : "even";
    const stateLabels = { ahead: "En ventaja", even: "Parejo", behind: "Por detrás" };
    elements.playerState.dataset.state = stateLabels[state] ? state : "even";
    setText(elements.playerState, stateLabels[state] || stateLabels.even);
    setText(elements.playerChampion, activePlayer.champion_name, "Garen");
    setText(elements.playerKDA, formatKDA(score));
    setText(elements.playerLevel, wholeNumber(activePlayer.level));
    setText(elements.playerCS, wholeNumber(score.creep_score));

    const confidence = Math.round(clamp(recommendation.confidence) * 100);
    setText(elements.confidenceLabel, `${confidence}%`);
    elements.confidenceBar.style.width = `${confidence}%`;
    elements.confidenceMeter.setAttribute("aria-valuenow", String(confidence));
  }

  function renderDamageMix(mixValue) {
    const mix = objectValue(mixValue);
    const raw = [clamp(mix.physical), clamp(mix.magic), clamp(mix.true)];
    const total = raw.reduce((sum, value) => sum + value, 0);
    const normalized = total > 0 ? raw.map((value) => value / total) : [0, 0, 0];
    setProgress(elements.physicalBar, elements.physicalLabel, elements.physicalMeter, normalized[0]);
    setProgress(elements.magicBar, elements.magicLabel, elements.magicMeter, normalized[1]);
    setProgress(elements.trueBar, elements.trueLabel, elements.trueMeter, normalized[2]);
  }

  function setProgress(bar, label, meter, value) {
    const percentage = Math.round(clamp(value) * 100);
    bar.style.width = `${percentage}%`;
    setText(label, `${percentage}%`);
    meter.setAttribute("aria-valuenow", String(percentage));
  }

  function renderThreat(threatValue) {
    const threat = objectValue(threatValue);
    setText(elements.threatTitle, threat.champion_name, "Sin amenaza detectada");
    setText(elements.threatIdentity, threat.riot_id, "Identidad no disponible");
    setText(elements.threatScore, finiteNumber(threat.score).toFixed(2));
    setText(elements.threatLevel, wholeNumber(threat.level));
    setText(elements.threatKDA, formatKDA(threat));
    setText(elements.threatCS, wholeNumber(threat.creep_score));
    setText(elements.threatValue, formatGold(threat.inventory_value));
  }

  function renderInventory(itemsValue, patch, inventorySlotsUsed) {
    const items = Array.isArray(itemsValue) ? itemsValue.filter((item) => item && typeof item === "object") : [];
    const capacityItems = items
      .filter((item) => wholeNumber(item.slot, -1) !== 6)
      .slice(0, 6);
    const slotsUsed = Math.min(6, Math.max(0, wholeNumber(inventorySlotsUsed, capacityItems.length)));
    const cells = [];
    capacityItems.forEach((item) => cells.push(createInventoryItem(item, patch)));
    for (let index = capacityItems.length; index < 6; index += 1) {
      const empty = document.createElement("li");
      empty.className = "inventory-empty";
      empty.textContent = "Espacio libre";
      cells.push(empty);
    }
    elements.inventoryList.replaceChildren(...cells);
    setText(elements.inventoryCount, `${slotsUsed} / 6 espacios`);
  }

  function createInventoryItem(itemValue, patch) {
    const item = objectValue(itemValue);
    const name = typeof item.display_name === "string" && item.display_name.trim() ? item.display_name : "Objeto";
    const cell = document.createElement("li");
    cell.className = "inventory-item";

    const art = document.createElement("div");
    art.className = "item-art inventory-art";
    const image = document.createElement("img");
    image.width = 48;
    image.height = 48;
    image.hidden = true;
    const fallback = document.createElement("span");
    fallback.className = "item-fallback";
    fallback.setAttribute("aria-hidden", "true");
    art.append(image, fallback);
    renderImage(image, fallback, { id: item.id, name }, patch, false);

    const copy = document.createElement("span");
    copy.className = "inventory-copy";
    const title = document.createElement("strong");
    title.textContent = name;
    const price = document.createElement("small");
    price.textContent = formatGold(item.price);
    copy.append(title, price);
    cell.append(art, copy);

    const count = Math.max(1, wholeNumber(item.count, 1));
    if (count > 1) {
      const badge = document.createElement("span");
      badge.className = "item-count";
      badge.textContent = String(count);
      badge.setAttribute("aria-label", `${count} unidades`);
      cell.append(badge);
    }
    return cell;
  }

  function renderSession(snapshot, game) {
    setText(elements.mapName, game.map_name || (wholeNumber(game.map_number) === 11 ? "Grieta del Invocador" : `Mapa ${wholeNumber(game.map_number)}`));
    setText(elements.gameMode, game.game_mode);
    setText(elements.gameTime, formatGameTime(game.game_time_seconds));
    setText(elements.dashboardRevision, `#${wholeNumber(snapshot.revision)}`);
  }

  function updateTimestamps(snapshot) {
    const raw = typeof snapshot.updated_at === "string" ? Date.parse(snapshot.updated_at) : NaN;
    lastUpdatedAt = Number.isFinite(raw) ? raw : Date.now();
    setText(elements.lastUpdate, formatRelativeTime(lastUpdatedAt));
    elements.lastUpdate.title = new Date(lastUpdatedAt).toLocaleString();
    setText(elements.adviceFreshness, DEMO_MODE ? "Muestra fija" : "En vivo");
  }

  function showAlert(message, tone, details = []) {
    setText(elements.systemAlertText, message);
    elements.systemAlert.dataset.tone = tone === "error" ? "error" : "warning";
    const listItems = Array.isArray(details) ? details
      .filter((detail) => typeof detail === "string" && detail.trim())
      .map((detail) => {
        const item = document.createElement("li");
        item.textContent = detail;
        return item;
      }) : [];
    elements.systemAlertDetails.replaceChildren(...listItems);
    elements.systemAlertDetails.hidden = listItems.length === 0;
    elements.systemAlert.hidden = false;
  }

  function hideAlert() {
    elements.systemAlert.hidden = true;
    setText(elements.systemAlertText, "", "");
    elements.systemAlertDetails.replaceChildren();
    elements.systemAlertDetails.hidden = true;
  }

  function suspendAdvice(message) {
    if (renderedDashboard && !elements.dashboard.hidden) {
      elements.dashboard.classList.add("is-suspended");
      setText(elements.adviceFreshness, "Pausado");
    }
    showAlert(message, "error");
  }

  function synchronizeAdviceAvailability() {
    if (elements.dashboard.hidden) {
      return;
    }
    const adviceIsCurrent = DEMO_MODE || (streamOpen && !dashboardResyncRequired);
    elements.dashboard.classList.toggle("is-suspended", !adviceIsCurrent);
    setText(elements.adviceFreshness, DEMO_MODE ? "Muestra fija" : (adviceIsCurrent ? "En vivo" : "Pausado"));
  }

  function abortDashboardRequest() {
    if (activeDashboardRequest) {
      activeDashboardRequest.abort();
      activeDashboardRequest = null;
    }
  }

  function advanceConnectionGeneration(isOpen) {
    connectionGeneration += 1;
    streamOpen = isOpen;
    if (!isOpen) {
      dashboardResyncRequired = true;
    }
    abortDashboardRequest();
    return connectionGeneration;
  }

  function isCurrentOpenConnection(generation) {
    return streamOpen && generation === connectionGeneration;
  }

  async function responseJSON(response) {
    const value = await response.json();
    if (!response.ok) {
      const envelope = objectValue(value);
      const error = objectValue(envelope.error);
      const failure = new Error(typeof error.message === "string" ? error.message : `HTTP ${response.status}`);
      failure.authoritative = true;
      throw failure;
    }
    return value;
  }

  async function loadDashboard(options = {}) {
    const expectedGeneration = Number.isSafeInteger(options.generation) ? options.generation : connectionGeneration;
    const requireOpen = options.requireOpen === true;
    if (requireOpen && !isCurrentOpenConnection(expectedGeneration)) {
      return null;
    }

    abortDashboardRequest();
    const controller = new AbortController();
    activeDashboardRequest = controller;
    try {
      const response = await fetch("/api/dashboard", {
        method: "GET",
        cache: "no-store",
        headers: { Accept: "application/json" },
        signal: controller.signal,
      });
      const snapshot = await responseJSON(response);
      const generationChanged = expectedGeneration !== connectionGeneration;
      if (controller.signal.aborted || generationChanged || (requireOpen && !isCurrentOpenConnection(expectedGeneration))) {
        return null;
      }
      if (!renderDashboard(snapshot)) {
        return null;
      }
      if (requireOpen && dashboardResyncRequired) {
        dashboardResyncRequired = false;
        setConnection("live");
        loadDecisionHistory(currentSessionID, true);
      }
      synchronizeAdviceAvailability();
      return snapshot;
    } catch (error) {
      if (controller.signal.aborted || (error && error.name === "AbortError")) {
        return null;
      }
      throw error;
    } finally {
      if (activeDashboardRequest === controller) {
        activeDashboardRequest = null;
      }
    }
  }

  function resynchronizeDashboard(generation, retrySuperseded = true) {
    loadDashboard({ generation, requireOpen: true })
      .then((snapshot) => {
        // SSE may advance the revision while REST is in flight. Retry once;
        // further hints can retry again, without spinning on stale responses.
        if (!snapshot && retrySuperseded && isCurrentOpenConnection(generation) &&
            dashboardResyncRequired && !activeDashboardRequest) {
          resynchronizeDashboard(generation, false);
        }
      })
      .catch(() => {
        if (isCurrentOpenConnection(generation) && dashboardResyncRequired) {
          suspendAdvice("Se recuperó el canal, pero la resincronización todavía no responde.");
        }
      });
  }

  function connectEvents() {
    eventSource = new EventSource("/api/events");
    eventSource.addEventListener("open", () => {
      const isReconnect = openedEventStream;
      openedEventStream = true;
      const generation = advanceConnectionGeneration(true);
      if (isReconnect) {
        setConnection("reconnecting");
        suspendAdvice("Canal restablecido; validando una instantánea vigente del servidor.");
        dashboardResyncRequired = true;
        resynchronizeDashboard(generation);
      } else {
        setConnection("live");
        synchronizeAdviceAvailability();
      }
    });
    eventSource.addEventListener("dashboard_snapshot", (event) => {
      if (!streamOpen) {
        suspendAdvice("Llegó una actualización fuera de una conexión activa; se mantiene la recomendación en pausa.");
        return;
      }
      try {
        const snapshot = JSON.parse(event.data);
        if (renderDashboard(snapshot)) {
          synchronizeAdviceAvailability();
          if (!dashboardResyncRequired) {
            setConnection("live");
          }
        }
        if (dashboardResyncRequired && !activeDashboardRequest) {
          resynchronizeDashboard(connectionGeneration);
        }
      } catch {
        suspendAdvice("Se recibió una actualización inválida; se conserva la recomendación en pausa.");
      }
    });
    eventSource.addEventListener("decision_notification", (event) => {
      if (!streamOpen || dashboardResyncRequired) {
        return;
      }
      try {
        handleDecisionEvent(JSON.parse(event.data));
      } catch {
        // REST history remains authoritative if a live hint is malformed.
      }
    });
    eventSource.addEventListener("error", () => {
      advanceConnectionGeneration(false);
      setConnection("reconnecting");
      suspendAdvice("Conexión de eventos interrumpida. Reconectando y evitando mostrar consejos como vigentes.");
    });
  }

  async function recalculate() {
    if (DEMO_MODE) {
      setActionFeedback("Muestra fija: no se modificó el backend.", "success");
      return;
    }
    elements.recalculateButton.disabled = true;
    elements.recalculateButton.setAttribute("aria-busy", "true");
    setText(elements.recalculateLabel, "Recalculando…");
    setActionFeedback("Leyendo la última revisión de la partida.", "pending");
    try {
      const response = await fetch("/api/recommendation/recalculate", {
        method: "POST",
        cache: "no-store",
        headers: { Accept: "application/json" },
      });
      await responseJSON(response);
      const generation = connectionGeneration;
      const snapshot = await loadDashboard({ generation, requireOpen: true });
      if (snapshot && isCurrentOpenConnection(generation)) {
        setActionFeedback("Recomendación actualizada.", "success");
      } else {
        setActionFeedback("Recalculada; esperando que vuelva la conexión en vivo.", "pending");
      }
    } catch {
      setActionFeedback("No se pudo recalcular. Se conserva el panel en pausa.", "error");
      suspendAdvice("La actualización manual falló. Esperando la siguiente lectura automática.");
    } finally {
      elements.recalculateButton.disabled = false;
      elements.recalculateButton.removeAttribute("aria-busy");
      setText(elements.recalculateLabel, "Recalcular");
    }
  }

  function setActionFeedback(message, tone) {
    setText(elements.actionFeedback, message, "");
    elements.actionFeedback.dataset.tone = tone;
  }

  function configurePersonalization() {
    elements.personalizationForm.addEventListener("submit", savePersonalization);
    elements.personalizationReset.addEventListener("click", resetPersonalization);
    elements.personalizationDeleteFeedback.addEventListener("click", deletePersonalizationFeedback);
    elements.personalizationPreferred.addEventListener("change", renderPersonalizationChips);
    elements.personalizationAvoided.addEventListener("change", renderPersonalizationChips);
    if (DEMO_MODE) {
      elements.personalizationSave.disabled = true;
      elements.personalizationReset.disabled = true;
      elements.personalizationDeleteFeedback.disabled = true;
      setText(elements.personalizationStatus, "La muestra no guarda ajustes locales.");
      return;
    }
    loadPersonalization().catch(() => {
      setText(elements.personalizationStatus, "La personalización no está disponible hasta validar el catálogo local.");
    });
  }

  async function loadPersonalization() {
    const generation = ++personalizationLoadGeneration;
    const expectedPatch = activeCatalogPatch;
    const [preferencesResponse, optionsResponse] = await Promise.all([
      fetch("/api/personalization/preferences", { cache: "no-store", headers: { Accept: "application/json" } }),
      fetch("/api/personalization/options", { cache: "no-store", headers: { Accept: "application/json" } }),
    ]);
    const preferences = dashboardState.normalizePersonalizationPreferences(await responseJSON(preferencesResponse));
    const options = objectValue(await responseJSON(optionsResponse));
    if (generation !== personalizationLoadGeneration || (expectedPatch && options.patch !== expectedPatch)) {
      return;
    }
    const optionItems = Array.isArray(options.items) ? options.items : [];
    const availableItemIDs = new Set(optionItems.map((item) => validItemID(objectValue(item).id)).filter(Boolean));
    const catalogMismatch = preferences.preferred_item_ids.concat(preferences.avoided_item_ids)
      .some((itemID) => !availableItemIDs.has(itemID));
    populatePersonalizationItems(optionItems);
    personalizationPreferences = preferences;
    personalizationMutation = { generation: personalizationMutation.generation, visible: preferences, pending: false };
    applyPersonalizationControls(preferences);
    setText(elements.personalizationStatus, catalogMismatch ?
      "Un objeto guardado ya no está disponible en el catálogo activo; la personalización queda pausada hasta guardar otros ajustes o restablecerla." :
      "Ajustes locales cargados. El modo equilibrado no modifica el orden base.");
  }

  function populatePersonalizationItems(items) {
    const build = () => items.map((value) => {
      const item = objectValue(value);
      const option = document.createElement("option");
      option.value = String(validItemID(item.id));
      option.textContent = typeof item.name === "string" ? item.name : `Objeto ${option.value}`;
      return option;
    }).filter((option) => option.value !== "0");
    elements.personalizationPreferred.replaceChildren(...build());
    elements.personalizationAvoided.replaceChildren(...build());
  }

  function selectedPersonalizationIDs(select) {
    return Array.from(select.selectedOptions).map((option) => validItemID(option.value)).filter(Boolean).sort((left, right) => left - right);
  }

  function readPersonalizationControls() {
    return dashboardState.normalizePersonalizationPreferences({
      style: elements.personalizationStyle.value,
      preferred_item_ids: selectedPersonalizationIDs(elements.personalizationPreferred),
      avoided_item_ids: selectedPersonalizationIDs(elements.personalizationAvoided),
      feedback_enabled: elements.personalizationFeedbackEnabled.checked,
      version: personalizationPreferences.version,
    });
  }

  function applyPersonalizationControls(preferences) {
    const normalized = dashboardState.normalizePersonalizationPreferences(preferences);
    elements.personalizationStyle.value = normalized.style;
    elements.personalizationFeedbackEnabled.checked = normalized.feedback_enabled;
    const preferred = new Set(normalized.preferred_item_ids);
    const avoided = new Set(normalized.avoided_item_ids);
    Array.from(elements.personalizationPreferred.options).forEach((option) => { option.selected = preferred.has(validItemID(option.value)); });
    Array.from(elements.personalizationAvoided.options).forEach((option) => { option.selected = avoided.has(validItemID(option.value)); });
    setText(elements.personalizationVersion, normalized.version > 0 ? `Versión local ${normalized.version}` : "Valores predeterminados");
    renderPersonalizationChips();
  }

  function renderPersonalizationChips() {
    const render = (select, container) => {
      const chips = Array.from(select.selectedOptions).map((option) => {
        const item = document.createElement("li");
        item.textContent = option.textContent;
        return item;
      });
      if (chips.length === 0) {
        const empty = document.createElement("li");
        empty.textContent = "Ninguno";
        chips.push(empty);
      }
      container.replaceChildren(...chips);
    };
    render(elements.personalizationPreferred, elements.personalizationPreferredChips);
    render(elements.personalizationAvoided, elements.personalizationAvoidedChips);
  }

  function personalizationOverlap(preferences) {
    const preferred = new Set(preferences.preferred_item_ids);
    return preferences.avoided_item_ids.some((itemID) => preferred.has(itemID));
  }

  async function savePersonalization(event) {
    event.preventDefault();
    const optimistic = readPersonalizationControls();
    if (personalizationOverlap(optimistic)) {
      setText(elements.personalizationStatus, "Un objeto no puede estar a la vez entre preferidos y evitados.");
      elements.personalizationPreferred.focus();
      return;
    }
    await mutatePersonalization(optimistic, "/api/personalization/preferences", "PUT",
      "Personalización guardada. Los ajustes son locales, acotados y explicables.");
  }

  async function resetPersonalization() {
    const defaults = { style: "balanced", preferred_item_ids: [], avoided_item_ids: [], feedback_enabled: false, version: personalizationPreferences.version };
    await mutatePersonalization(defaults, "/api/personalization/preferences/reset", "POST",
      "Personalización restablecida; el orden base vuelve a estar activo.", {});
    if (!personalizationPreferences.feedback_enabled) {
      feedbackState = dashboardState.feedbackStateForSession({}, currentSessionID);
      saveSubmittedFeedback();
    }
  }

  async function deletePersonalizationFeedback() {
    if (!window.confirm("¿Eliminar todas las valoraciones locales? Tus preferencias y el historial de partidas se conservarán.")) {
      return;
    }
    elements.personalizationSave.disabled = true;
    elements.personalizationReset.disabled = true;
    elements.personalizationDeleteFeedback.disabled = true;
    setText(elements.personalizationStatus, "Eliminando valoraciones locales…");
    try {
      const response = await fetch("/api/personalization/feedback", {
        method: "DELETE", cache: "no-store", headers: { Accept: "application/json", "Content-Type": "application/json" },
        body: JSON.stringify({ confirmation: "DELETE_PERSONALIZATION_FEEDBACK" }),
      });
      const result = objectValue(await responseJSON(response));
      feedbackState = dashboardState.feedbackStateForSession({}, currentSessionID);
      saveSubmittedFeedback();
      setText(elements.personalizationStatus, `${wholeNumber(result.deleted)} valoraciones locales eliminadas. El historial de partidas no cambió.`);
    } catch {
      setText(elements.personalizationStatus, "No se eliminaron las valoraciones; los datos locales se conservaron.");
    } finally {
      elements.personalizationSave.disabled = false;
      elements.personalizationReset.disabled = false;
      elements.personalizationDeleteFeedback.disabled = false;
    }
  }

  async function mutatePersonalization(optimistic, endpoint, method, successMessage, explicitBody) {
    personalizationMutation = dashboardState.beginPersonalizationMutation(personalizationPreferences, optimistic, personalizationMutation.generation);
    const requestGeneration = personalizationMutation.generation;
    personalizationPreferences = personalizationMutation.visible;
    applyPersonalizationControls(personalizationPreferences);
    elements.personalizationSave.disabled = true;
    elements.personalizationReset.disabled = true;
    elements.personalizationDeleteFeedback.disabled = true;
    setText(elements.personalizationStatus, "Guardando ajustes locales…");
    try {
      const response = await fetch(endpoint, {
        method, cache: "no-store", headers: { Accept: "application/json", "Content-Type": "application/json" },
        body: JSON.stringify(explicitBody === undefined ? dashboardState.personalizationUpdatePayload(optimistic) : explicitBody),
      });
      const serverPreferences = await responseJSON(response);
      const settled = dashboardState.settlePersonalizationMutation(personalizationMutation, requestGeneration, true, serverPreferences);
      if (!settled.apply) {
        return;
      }
      personalizationMutation = settled.state;
      personalizationPreferences = settled.state.visible;
      applyPersonalizationControls(personalizationPreferences);
      setText(elements.personalizationStatus, successMessage);
    } catch {
      const settled = dashboardState.settlePersonalizationMutation(personalizationMutation, requestGeneration, false, {});
      if (settled.apply) {
        personalizationMutation = settled.state;
        personalizationPreferences = settled.state.visible;
        applyPersonalizationControls(personalizationPreferences);
        setText(elements.personalizationStatus, "No se guardaron los cambios; la interfaz volvió a los valores confirmados.");
      }
    } finally {
      if (personalizationMutation.generation === requestGeneration) {
        elements.personalizationSave.disabled = false;
        elements.personalizationReset.disabled = false;
        elements.personalizationDeleteFeedback.disabled = false;
      }
    }
  }

  function configurePregame() {
    renderPregameInventoryRows();
    renderPregamePressureOptions([
      { value: "unset", label: "Sin indicar" },
      { value: "none", label: "Ninguna" },
      { value: "low", label: "Baja" },
      { value: "medium", label: "Media" },
      { value: "high", label: "Alta" },
    ]);
    elements.liveViewButton.addEventListener("click", () => switchView("live"));
    elements.pregameViewButton.addEventListener("click", () => switchView("pregame"));
    elements.pregameForm.addEventListener("submit", submitPregamePlan);
  }

  function switchView(view) {
    activeView = view === "pregame" ? "pregame" : "live";
    const planning = activeView === "pregame";
    elements.liveView.hidden = planning;
    elements.pregameView.hidden = !planning;
    elements.liveStatusCluster.hidden = planning;
    elements.liveViewButton.classList.toggle("is-active", !planning);
    elements.pregameViewButton.classList.toggle("is-active", planning);
    elements.liveViewButton.setAttribute("aria-pressed", String(!planning));
    elements.pregameViewButton.setAttribute("aria-pressed", String(planning));
    if (DEMO_MODE) {
      elements.demoBanner.hidden = planning;
    }
    if (planning) {
      loadPregameOptions().catch(() => {});
      elements.pregameChampion.focus();
    } else {
      elements.liveViewButton.focus();
    }
  }

  function renderPregameInventoryRows() {
    const rows = [];
    for (let index = 1; index <= 7; index += 1) {
      const row = document.createElement("div");
      row.className = "pregame-inventory-row";

      const itemLabel = document.createElement("label");
      itemLabel.htmlFor = `pregame-item-${index}`;
      const itemText = document.createElement("span");
      itemText.textContent = `Objeto ${index}`;
      const itemSelect = document.createElement("select");
      itemSelect.id = `pregame-item-${index}`;
      itemSelect.dataset.pregameItem = String(index);
      const blank = document.createElement("option");
      blank.value = "";
      blank.textContent = "Sin objeto";
      itemSelect.append(blank);
      itemLabel.append(itemText, itemSelect);

      const countLabel = document.createElement("label");
      countLabel.htmlFor = `pregame-item-count-${index}`;
      const countText = document.createElement("span");
      countText.textContent = "Cantidad";
      const count = document.createElement("input");
      count.id = `pregame-item-count-${index}`;
      count.dataset.pregameItemCount = String(index);
      count.type = "number";
      count.min = "1";
      count.max = "99";
      count.step = "1";
      count.inputMode = "numeric";
      count.value = "1";
      countLabel.append(countText, count);
      row.append(itemLabel, countLabel);
      rows.push(row);
    }
    elements.pregameInventoryRows.replaceChildren(...rows);
  }

  function renderPregamePressureOptions(optionsValue) {
    const options = Array.isArray(optionsValue) ? optionsValue : [];
    elements.pregamePressureSelects.forEach((select) => {
      const selected = select.value || "unset";
      const nodes = options.map((value) => {
        const option = document.createElement("option");
        option.value = typeof value.value === "string" ? value.value : "unset";
        option.textContent = typeof value.label === "string" ? value.label : option.value;
        return option;
      });
      select.replaceChildren(...nodes);
      select.value = nodes.some((option) => option.value === selected) ? selected : "unset";
    });
  }

  function invalidatePregameOptions(expectedPatch, message) {
    pregameOptionsState = dashboardState.beginPregameOptionsRequest(pregameOptionsState, expectedPatch);
    pregamePlanRequestGeneration += 1;
    pregameOptions = null;
    pregameOptionsPromise = null;
    populatePregameCatalogOptions({ champions: [], items: [] });
    elements.pregameResult.hidden = true;
    elements.pregameSubmit.disabled = true;
    elements.pregameSubmit.removeAttribute("aria-busy");
    setText(elements.pregameOptionsStatus, message || "Las opciones manuales deben validarse contra el catálogo activo.");
  }

  function synchronizePregameCatalogPatch(value) {
    const patch = validPatch(value);
    if (patch === activeCatalogPatch) {
      return;
    }
    activeCatalogPatch = patch;
    invalidatePregameOptions(patch, patch ?
      `El catálogo activo cambió a Data Dragon ${patch}; validando de nuevo las opciones manuales.` :
      "El catálogo activo no está disponible; las opciones manuales permanecen deshabilitadas.");
    if (activeView === "pregame" && patch) {
      loadPregameOptions().catch(() => {});
    }
    if (!DEMO_MODE && patch) {
      loadPersonalization().catch(() => {
        setText(elements.personalizationStatus, "El catálogo cambió; los selectores de personalización esperan una validación vigente.");
      });
    }
  }

  function loadPregameOptions() {
    pregameOptionsState = dashboardState.beginPregameOptionsRequest(pregameOptionsState, activeCatalogPatch);
    const requestGeneration = pregameOptionsState.generation;
    pregameOptions = null;
    populatePregameCatalogOptions({ champions: [], items: [] });
    elements.pregameResult.hidden = true;
    elements.pregameSubmit.disabled = true;
    elements.pregameSubmit.removeAttribute("aria-busy");
    setText(elements.pregameOptionsStatus, "Cargando opciones seguras del catálogo…");
    const promise = fetch("/api/pregame/options", {
      method: "GET", cache: "no-store", headers: { Accept: "application/json" },
    }).then(responseJSON).then((options) => {
      const candidate = objectValue(options);
      const evaluated = dashboardState.evaluatePregameOptionsResponse(
        pregameOptionsState,
        requestGeneration,
        candidate.patch,
      );
      if (!evaluated.accept) {
        if (pregameOptionsState.generation === requestGeneration) {
          setText(elements.pregameOptionsStatus, "El catálogo cambió durante la carga; las opciones anteriores no se usarán.");
        }
        return null;
      }
      pregameOptionsState = evaluated.state;
      pregameOptions = candidate;
      populatePregameCatalogOptions(pregameOptions);
      const patch = pregameOptionsState.loadedPatch;
      setText(elements.pregameOptionsStatus, `Opciones validadas contra Data Dragon ${patch}.`);
      elements.pregameSubmit.disabled = false;
      return pregameOptions;
    }).catch((error) => {
      if (pregameOptionsState.generation === requestGeneration) {
        setText(elements.pregameOptionsStatus, "La planificación manual no está disponible hasta validar el catálogo y los perfiles.");
      }
      throw error;
    }).finally(() => {
      if (pregameOptionsPromise && pregameOptionsPromise.generation === requestGeneration) {
        pregameOptionsPromise = null;
      }
    });
    pregameOptionsPromise = { generation: requestGeneration, promise };
    return promise;
  }

  function populatePregameCatalogOptions(optionsValue) {
    const options = objectValue(optionsValue);
    const champions = Array.isArray(options.champions) ? options.champions : [];
    replacePregameSelectOptions(elements.pregameChampion, "Selecciona un campeón", champions, "id", "name");
    replacePregameSelectOptions(elements.pregameLaneOpponent, "Sin oponente indicado", champions, "id", "name");
    const items = Array.isArray(options.items) ? options.items : [];
    elements.pregameInventoryRows.querySelectorAll("[data-pregame-item]").forEach((select) => {
      replacePregameSelectOptions(select, "Sin objeto", items, "id", "name");
    });
    if (Array.isArray(options.pressure_levels) && options.pressure_levels.length > 0) {
      renderPregamePressureOptions(options.pressure_levels);
    }
  }

  function replacePregameSelectOptions(select, placeholder, values, valueKey, labelKey) {
    const selected = select.value;
    const first = document.createElement("option");
    first.value = "";
    first.textContent = placeholder;
    const options = [first];
    values.forEach((value) => {
      const record = objectValue(value);
      const optionValue = record[valueKey];
      const label = record[labelKey];
      if ((typeof optionValue !== "string" && !Number.isSafeInteger(optionValue)) || typeof label !== "string" || !label.trim()) {
        return;
      }
      const option = document.createElement("option");
      option.value = String(optionValue);
      option.textContent = label;
      options.push(option);
    });
    select.replaceChildren(...options);
    if (options.some((option) => option.value === selected)) {
      select.value = selected;
    }
  }

  function collectPregameScenario() {
    const pressures = {};
    elements.pregamePressureSelects.forEach((select) => {
      pressures[select.dataset.pregamePressure] = select.value;
    });
    const inventory = Array.from(elements.pregameInventoryRows.querySelectorAll("[data-pregame-item]")).map((select, index) => ({
      itemID: select.value,
      count: byID(`pregame-item-count-${index + 1}`).value,
    }));
    return dashboardState.buildPregameScenario({
      champion: elements.pregameChampion.value,
      role: elements.pregameRole.value,
      stage: elements.pregameStage.value,
      strategy: elements.pregameStrategy.value,
      laneOpponent: elements.pregameLaneOpponent.value,
      gold: elements.pregameGold.value,
      pressures,
      inventory,
    });
  }

  function clearPregameErrors() {
    elements.pregameErrorSummary.hidden = true;
    elements.pregameErrorList.replaceChildren();
    elements.pregameForm.querySelectorAll("[aria-invalid='true']").forEach((control) => control.removeAttribute("aria-invalid"));
  }

  function renderPregameErrors(errorsValue) {
    const errors = Array.isArray(errorsValue) ? errorsValue : [];
    if (errors.length === 0) {
      clearPregameErrors();
      return;
    }
    const items = errors.map((error) => {
      const field = typeof error.field === "string" ? error.field : "pregame-form";
      const message = typeof error.message === "string" ? error.message : "Revisa este campo.";
      const control = byID(field);
      if (control) {
        control.setAttribute("aria-invalid", "true");
      }
      const item = document.createElement("li");
      const button = document.createElement("button");
      button.type = "button";
      button.className = "pregame-error-link";
      button.textContent = message;
      button.addEventListener("click", () => {
        if (control) {
          control.focus();
        }
      });
      item.append(button);
      return item;
    });
    elements.pregameErrorList.replaceChildren(...items);
    elements.pregameErrorSummary.hidden = false;
    elements.pregameErrorSummary.focus();
  }

  function serverPregameErrors(fieldsValue) {
    const fields = objectValue(fieldsValue);
    return Object.keys(fields).sort().map((path) => ({
      field: pregameFieldID(path),
      message: typeof fields[path] === "string" ? fields[path] : "Revisa este campo.",
    }));
  }

  function pregameFieldID(path) {
    const direct = {
      champion: "pregame-champion", role: "pregame-role", stage: "pregame-stage",
      strategy: "pregame-strategy", lane_opponent: "pregame-lane-opponent", gold: "pregame-gold",
      inventory: "pregame-inventory-rows", body: "pregame-form",
    };
    if (direct[path]) {
      return direct[path];
    }
    const pressure = /^pressures\.([a-z_]+)$/.exec(path);
    if (pressure) {
      const select = elements.pregamePressureSelects.find((value) => value.dataset.pregamePressure === pressure[1]);
      return select ? select.id : "pregame-form";
    }
    const inventory = /^inventory\[(\d+)\]\.(item_id|count)$/.exec(path);
    if (inventory) {
      const row = Number(inventory[1]) + 1;
      return inventory[2] === "count" ? `pregame-item-count-${row}` : `pregame-item-${row}`;
    }
    return "pregame-form";
  }

  function pregamePlanRequestContext(requestGeneration, optionsGeneration, optionsPatch) {
    return {
      requestGeneration,
      currentGeneration: pregamePlanRequestGeneration,
      requestOptionsGeneration: optionsGeneration,
      requestPatch: optionsPatch,
      optionsState: pregameOptionsState,
      activePatch: activeCatalogPatch,
    };
  }

  function isCurrentPregamePlanRequest(requestGeneration, optionsGeneration, optionsPatch) {
    return dashboardState.canApplyPregamePlanRequest(
      pregamePlanRequestContext(requestGeneration, optionsGeneration, optionsPatch),
    );
  }

  function applyCurrentPregamePlanEffect(requestGeneration, optionsGeneration, optionsPatch, effect) {
    return dashboardState.applyPregamePlanEffect(
      pregamePlanRequestContext(requestGeneration, optionsGeneration, optionsPatch),
      effect,
    );
  }

  async function submitPregamePlan(event) {
    event.preventDefault();
    clearPregameErrors();
    if (!dashboardState.canUsePregameOptions(pregameOptionsState, activeCatalogPatch)) {
      renderPregameErrors([{ field: "pregame-form", message: "Las opciones del catálogo cambiaron. Espera a que se validen antes de generar otro plan." }]);
      loadPregameOptions().catch(() => {});
      return;
    }
    const optionsGeneration = pregameOptionsState.generation;
    const optionsPatch = pregameOptionsState.loadedPatch;
    const scenario = collectPregameScenario();
    const clientErrors = dashboardState.validatePregameScenario(scenario);
    if (clientErrors.length > 0) {
      renderPregameErrors(clientErrors);
      return;
    }
    const requestGeneration = pregamePlanRequestGeneration + 1;
    pregamePlanRequestGeneration = requestGeneration;
    elements.pregameSubmit.disabled = true;
    elements.pregameSubmit.setAttribute("aria-busy", "true");
    elements.pregameResult.hidden = true;
    try {
      const response = await fetch("/api/pregame/plan", {
        method: "POST",
        cache: "no-store",
        headers: { Accept: "application/json", "Content-Type": "application/json" },
        body: JSON.stringify(scenario),
      });
      if (!isCurrentPregamePlanRequest(requestGeneration, optionsGeneration, optionsPatch)) {
        return;
      }
      const payload = await response.json();
      if (!isCurrentPregamePlanRequest(requestGeneration, optionsGeneration, optionsPatch)) {
        return;
      }
      if (!response.ok) {
        const error = objectValue(objectValue(payload).error);
        if (response.status === 422) {
          applyCurrentPregamePlanEffect(requestGeneration, optionsGeneration, optionsPatch, () => {
            renderPregameErrors(serverPregameErrors(error.fields));
          });
          return;
        }
        throw new Error(typeof error.message === "string" ? error.message : `HTTP ${response.status}`);
      }
      if (validPatch(objectValue(payload).patch) !== optionsPatch) {
        applyCurrentPregamePlanEffect(requestGeneration, optionsGeneration, optionsPatch, () => {
          elements.pregameResult.hidden = true;
          renderPregameErrors([{ field: "pregame-form", message: "El catálogo cambió mientras se generaba el plan. Las opciones anteriores se descartaron." }]);
          elements.pregameSubmit.removeAttribute("aria-busy");
          loadPregameOptions().catch(() => {});
        });
        return;
      }
      applyCurrentPregamePlanEffect(requestGeneration, optionsGeneration, optionsPatch, () => {
        renderPregamePlan(payload);
      });
    } catch {
      if (!applyCurrentPregamePlanEffect(requestGeneration, optionsGeneration, optionsPatch, () => {
        renderPregameErrors([{ field: "pregame-form", message: "No se pudo generar el plan manual. El escenario en vivo no fue modificado." }]);
      })) {
        return;
      }
    } finally {
      applyCurrentPregamePlanEffect(requestGeneration, optionsGeneration, optionsPatch, () => {
        elements.pregameSubmit.disabled = !dashboardState.canUsePregameOptions(pregameOptionsState, activeCatalogPatch);
        elements.pregameSubmit.removeAttribute("aria-busy");
      });
    }
  }

  function renderPregamePlan(value) {
    const plan = objectValue(value);
    const provenance = objectValue(plan.provenance);
    const profile = objectValue(provenance.profile);
    const rules = objectValue(provenance.rules);
    const statusLabels = {
      ready: "Opción disponible", save_for_component: "Plan de ahorro", inventory_full: "Inventario completo",
      complete: "Ruta completa", insufficient_context: "Contexto insuficiente", error: "No disponible",
    };
    setText(elements.pregameResultStatus, statusLabels[plan.status] || "Resultado manual");
    setText(elements.pregameResultReason, plan.reason, "No hay una comparación manual disponible para este escenario.");
    const provenanceLabels = [`${profile.profile_id || "perfil"} · ${profile.level || "sin nivel"}`];
    if (typeof rules.version === "string" && rules.version && typeof rules.source === "string" && rules.source) {
      provenanceLabels.push(`Reglas Garen v2 ${rules.version} (${rules.source})`);
    }
    setText(elements.pregameProfile, provenanceLabels.join(" · "));
    setText(elements.pregamePatch, plan.patch);
    setText(elements.pregameConfidence, `${Math.round(clamp(plan.confidence) * 100)}%`);
    const sources = Array.isArray(plan.sources) ? plan.sources.filter((source) => typeof source === "string") : [];
    setText(elements.pregameSource, sources.join(" · "), "Escenario suministrado por la persona usuaria");

    const ranked = Array.isArray(plan.ranked_options) ? plan.ranked_options : [];
    const cards = ranked.map((option, index) => createRankedChoice({
      item: objectValue(option).item,
      score: finiteNumber(objectValue(option).score, NaN),
      category: typeof objectValue(option).category === "string" ? objectValue(option).category : "",
      primary: index === 0,
    }, index, validPatch(plan.patch)));
    if (cards.length === 0) {
      const empty = document.createElement("li");
      empty.className = "pregame-empty-result";
      empty.textContent = "El escenario no activa una opción principal y dos alternativas válidas.";
      cards.push(empty);
    }
    elements.pregameRankedOptions.replaceChildren(...cards);

    const warnings = Array.isArray(plan.warnings) ? plan.warnings : [];
    const warningItems = warnings.map((warning) => {
      const item = document.createElement("li");
      const record = objectValue(warning);
      item.textContent = typeof record.message === "string" ? record.message : String(warning);
      return item;
    });
    if (warningItems.length === 0) {
      const item = document.createElement("li");
      item.textContent = "Sin supuestos adicionales declarados.";
      warningItems.push(item);
    }
    elements.pregameWarnings.replaceChildren(...warningItems);
    renderPregameCopy(elements.pregameRunes, plan.rune_plan);
    renderPregameCopy(elements.pregameSummoners, plan.summoner_plan);
    renderPregameCopy(elements.pregameInitialPlan, plan.initial_plan);
    elements.pregameResult.hidden = false;
  }

  function renderPregameCopy(container, value) {
    const copy = objectValue(value);
    const summary = document.createElement("p");
    summary.textContent = typeof copy.summary === "string" ? copy.summary : "Sin opciones curadas para este perfil.";
    const list = document.createElement("ol");
    const options = Array.isArray(copy.options) ? copy.options : [];
    options.forEach((optionValue) => {
      const option = objectValue(optionValue);
      const item = document.createElement("li");
      const title = document.createElement("strong");
      title.textContent = typeof option.label === "string" ? option.label : "Opción";
      const when = document.createElement("p");
      when.textContent = typeof option.when === "string" ? option.when : "";
      const tradeoff = document.createElement("p");
      tradeoff.textContent = typeof option.tradeoff === "string" ? `Intercambio: ${option.tradeoff}` : "";
      item.append(title, when, tradeoff);
      list.append(item);
    });
    container.replaceChildren(summary, list);
  }

  async function start() {
    configureAlertControls();
    configurePersonalization();
    configurePregame();
    elements.recalculateButton.addEventListener("click", recalculate);
    if (DEMO_MODE) {
      elements.demoBanner.hidden = false;
      document.title = "LoL Engine · Modo demo";
      setConnection("demo");
      renderDashboard(demoSnapshot);
      setActionFeedback("Demo local: el botón no llama al backend.", "pending");
      return;
    }

    setConnection("connecting");
    try {
      await loadDashboard({ generation: connectionGeneration, requireOpen: false });
    } catch {
      showState("error", null, "No fue posible leer /api/dashboard. El modo demo solo se activa con ?demo=1.");
      showAlert("El backend local no respondió. El panel no sustituirá datos reales con una muestra.", "error");
    }
    connectEvents();
  }

  window.addEventListener("beforeunload", () => {
    abortDashboardRequest();
    if (eventSource) {
      eventSource.close();
    }
    if (audioContext && typeof audioContext.close === "function") {
      audioContext.close().catch(() => {});
    }
  });
  window.setInterval(() => {
    if (lastUpdatedAt) {
      setText(elements.lastUpdate, formatRelativeTime(lastUpdatedAt));
    }
  }, 1000);

  start();
})();
