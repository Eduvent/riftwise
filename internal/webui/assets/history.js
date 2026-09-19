(() => {
  "use strict";

  const state = window.LoLEngineHistoryState;
  if (!state) return;

  const byID = (id) => document.getElementById(id);
  const elements = Object.freeze({
    historyButton: byID("history-view-button"), liveButton: byID("live-view-button"), pregameButton: byID("pregame-view-button"),
    liveView: byID("live-view"), pregameView: byID("pregame-view"), historyView: byID("history-view"),
    liveStatus: byID("live-status-cluster"), demoBanner: byID("demo-banner"),
    status: byID("history-status"), list: byID("history-list"), loadMore: byID("history-load-more"),
    detail: byID("history-detail"), detailTitle: byID("history-detail-title"), detailMeta: byID("history-detail-meta"),
    detailStats: byID("history-detail-stats"), detailTimeline: byID("history-detail-timeline"),
    detailChoices: byID("history-detail-choices"), detailRules: byID("history-detail-rules"),
    detailCaveats: byID("history-detail-caveats"), detailDelete: byID("history-delete-match"),
    retentionForm: byID("history-retention-form"), retentionDays: byID("history-retention-days"),
    retentionMaximum: byID("history-retention-maximum"), privacyStatus: byID("history-privacy-status"),
    clearAll: byID("history-clear-all"),
    riotStatus: byID("riot-history-status"), riotLinkForm: byID("riot-link-form"),
    riotGameName: byID("riot-game-name"), riotTagLine: byID("riot-tag-line"), riotPlatform: byID("riot-platform"),
    riotConsent: byID("riot-consent"), riotAccounts: byID("riot-account-list"),
    riotStatisticsForm: byID("riot-statistics-form"), riotStatsPatch: byID("riot-stats-patch"),
    riotStatsChampion: byID("riot-stats-champion"), riotStatsRole: byID("riot-stats-role"),
    riotStatisticsNotice: byID("riot-statistics-notice"), riotStatisticsResults: byID("riot-statistics-results"),
  });

  let matches = [];
  let nextCursor = "";
  let loading = false;
  let loaded = false;
  let selectedMatchID = "";
  let currentRiotAccountID = "";

  elements.historyButton.addEventListener("click", activateHistory);
  elements.liveButton.addEventListener("click", leaveHistory);
  elements.pregameButton.addEventListener("click", leaveHistory);
  elements.loadMore.addEventListener("click", () => loadHistory(true));
  elements.retentionForm.addEventListener("submit", savePreferences);
  elements.clearAll.addEventListener("click", clearAllHistory);
  elements.detailDelete.addEventListener("click", deleteSelectedMatch);
  elements.riotLinkForm.addEventListener("submit", linkRiotAccount);
  elements.riotStatisticsForm.addEventListener("submit", loadStatistics);

  function activateHistory() {
    elements.liveView.hidden = true;
    elements.pregameView.hidden = true;
    elements.historyView.hidden = false;
    elements.liveStatus.hidden = true;
    elements.demoBanner.hidden = true;
    elements.historyButton.classList.add("is-active");
    elements.liveButton.classList.remove("is-active");
    elements.pregameButton.classList.remove("is-active");
    elements.historyButton.setAttribute("aria-pressed", "true");
    elements.liveButton.setAttribute("aria-pressed", "false");
    elements.pregameButton.setAttribute("aria-pressed", "false");
    if (!loaded) {
      Promise.all([loadHistory(false), loadPreferences(), loadRiotStatus()]).catch(() => {});
    }
    elements.status.focus();
  }

  function leaveHistory() {
    elements.historyView.hidden = true;
    elements.historyButton.classList.remove("is-active");
    elements.historyButton.setAttribute("aria-pressed", "false");
  }

  async function loadHistory(append) {
    if (loading) return;
    loading = true;
    elements.loadMore.disabled = true;
    elements.status.textContent = append ? "Cargando más partidas…" : "Cargando historial local…";
    try {
      const suffix = append && nextCursor ? `&before=${encodeURIComponent(nextCursor)}` : "";
      const response = await fetch(`/api/history/matches?limit=20${suffix}`, { headers: { Accept: "application/json" }, cache: "no-store" });
      if (!response.ok) throw new Error(`history ${response.status}`);
      const page = state.normalizePage(await response.json());
      matches = append ? state.mergePage(matches, page.matches) : page.matches;
      nextCursor = page.nextCursor;
      loaded = true;
      renderHistoryList();
      elements.status.textContent = matches.length ? `${matches.length} partidas locales cargadas.` : "Todavía no hay partidas finalizadas en el historial local.";
    } catch (_error) {
      elements.status.textContent = "No se pudo cargar el historial local. La partida en vivo no se ve afectada.";
    } finally {
      loading = false;
      elements.loadMore.disabled = false;
      elements.loadMore.hidden = !nextCursor;
    }
  }

  function renderHistoryList() {
    const cards = matches.map((match) => {
      const item = document.createElement("li");
      item.className = "history-card";
      if (match.degraded) item.dataset.quality = "degraded";
      const button = document.createElement("button");
      button.type = "button";
      button.className = "history-card-button";
      button.addEventListener("click", () => loadDetail(match.matchID));
      const title = document.createElement("strong");
      title.textContent = `${match.champion} · ${match.role}`;
      const meta = document.createElement("span");
      const date = match.startedAt ? new Date(match.startedAt).toLocaleString("es-PE") : "Fecha no disponible";
      meta.textContent = `${date} · ${state.formatDuration(match.durationSeconds, match.durationLowerBound)}`;
      const stats = document.createElement("span");
      stats.textContent = match.stats ? `${match.stats.kills}/${match.stats.deaths}/${match.stats.assists} · ${match.stats.creepScore} CS` : "KDA/CS no disponible";
      const outcome = document.createElement("span");
      outcome.className = "history-outcome";
      outcome.textContent = `Resultado no disponible · ${state.completionLabel(match.completion)}${match.degraded ? " · Datos parciales" : ""}`;
      button.append(title, meta, stats, outcome);
      item.append(button);
      return item;
    });
    elements.list.replaceChildren(...cards);
  }

  async function loadDetail(matchID) {
    selectedMatchID = matchID;
    elements.detail.hidden = false;
    elements.detailTitle.textContent = "Cargando detalle…";
    elements.detailMeta.textContent = "";
    elements.detailStats.replaceChildren();
    elements.detailTimeline.replaceChildren();
    elements.detailChoices.replaceChildren();
    elements.detailCaveats.replaceChildren();
    try {
      const response = await fetch(`/api/history/matches/${encodeURIComponent(matchID)}`, { headers: { Accept: "application/json" }, cache: "no-store" });
      if (!response.ok) throw new Error(`detail ${response.status}`);
      const summary = await response.json();
      if (selectedMatchID !== matchID) return;
      renderDetail(summary);
      elements.detail.focus();
    } catch (_error) {
      if (selectedMatchID !== matchID) return;
      elements.detailTitle.textContent = "Detalle no disponible";
      elements.detailMeta.textContent = "El resumen local no pudo reconstruirse en este momento.";
    }
  }

  function renderDetail(summary) {
    const normalized = state.normalizeSummary(summary);
    elements.detailTitle.textContent = `${normalized.champion} · ${normalized.role}`;
    elements.detailMeta.textContent = `${state.completionLabel(normalized.completion)} · ${state.formatDuration(normalized.durationSeconds, normalized.durationLowerBound)} · Resultado no disponible`;
    const stats = normalized.stats ? [
      `K / D / A: ${normalized.stats.kills} / ${normalized.stats.deaths} / ${normalized.stats.assists}`,
      `CS: ${normalized.stats.creepScore}`, `Nivel: ${normalized.stats.level}`,
      normalized.stats.quality === "terminal_snapshot" ? "Estadísticas del último estado terminal observado" : "Estadísticas del último estado observado",
    ] : ["KDA/CS no disponible para esta partida antigua."];
    elements.detailStats.replaceChildren(...stats.map(textListItem));

    const timeline = [];
    for (const event of Array.isArray(summary.inventory_changes) ? summary.inventory_changes : []) {
      const sourceLabel = event.source === "observed" ? "Observado" : `Inferido · ${state.confidenceLabel(event.confidence)}`;
      timeline.push(historyTimelineItem(event, "Cambio de inventario", sourceLabel));
    }
    for (const event of Array.isArray(summary.inferred_purchases) ? summary.inferred_purchases : []) {
      timeline.push(historyTimelineItem(event, "Adquisición o compra inferida", `Inferido · ${state.confidenceLabel(event.confidence)}`));
    }
    for (const event of Array.isArray(summary.inferred_base_returns) ? summary.inferred_base_returns : []) {
      timeline.push(historyTimelineItem(event, "Regreso a base inferido", `Inferido · ${state.confidenceLabel(event.confidence)}`));
    }
    timeline.sort((left, right) => Number(left.dataset.gameTime) - Number(right.dataset.gameTime));
    if (!timeline.length) timeline.push(textListItem("No se registraron cambios de inventario o regresos a base."));
    elements.detailTimeline.replaceChildren(...timeline);

    const evidence = summary.final_recommendation && typeof summary.final_recommendation === "object" ? summary.final_recommendation : null;
    const choices = evidence && Array.isArray(evidence.ranked_choices) ? evidence.ranked_choices : [];
    elements.detailChoices.replaceChildren(...(choices.length ? choices.map((choice) => {
      const item = document.createElement("li");
      item.textContent = `${choice.rank || "—"}. ${(choice.item && choice.item.name) || `Objeto ${choice.item && choice.item.id || "—"}`} · puntuación ${choice.score || 0}`;
      return item;
    }) : [textListItem("No hay evidencia estructurada de opciones para esta partida.")]));
    const rules = evidence && evidence.rules ? evidence.rules : {};
    const profile = evidence && evidence.profile ? evidence.profile : null;
    const reasons = evidence && Array.isArray(evidence.reason_codes) ? evidence.reason_codes.filter((value) => typeof value === "string" && value.trim()) : [];
    const confidence = evidence && typeof evidence.confidence === "number" ? state.confidenceLabel(evidence.confidence) : "No disponible";
    const traitConfidence = evidence && typeof evidence.trait_confidence === "number" ? state.confidenceLabel(evidence.trait_confidence) : "No disponible";
    elements.detailRules.textContent = evidence ?
      `Confianza: ${confidence}. Confianza de rasgos: ${traitConfidence}. Reglas: ${rules.version || "versión no disponible"} (${rules.source || "fuente no disponible"}).${profile ? ` Perfil: ${profile.profile_id || profile.level || "no disponible"}.` : ""}${reasons.length ? ` Motivos: ${reasons.join(", ")}.` : ""}` :
      "La evidencia de reglas no está disponible para esta partida antigua.";
    const caveats = Array.isArray(summary.caveats) ? summary.caveats : [];
    const issues = Array.isArray(summary.integrity_issues) ? summary.integrity_issues : [];
    const caveatItems = caveats.map((value) => textListItem(caveatLabel(value)));
    for (const issue of issues) caveatItems.push(textListItem(`Dato parcial: ${issue.code || "integridad desconocida"}.`));
    elements.detailCaveats.replaceChildren(...(caveatItems.length ? caveatItems : [textListItem("Sin advertencias adicionales.")]));
  }

  function historyTimelineItem(event, title, badge) {
    const item = document.createElement("li");
    item.dataset.gameTime = String(Number(event.game_time_seconds) || 0);
    const heading = document.createElement("strong");
    heading.textContent = title;
    const label = document.createElement("span");
    label.className = "history-evidence-badge";
    label.textContent = badge;
    const time = document.createElement("span");
    time.textContent = `Tiempo de partida: ${state.formatDuration(event.game_time_seconds, false)}`;
    const detail = document.createElement("span");
    detail.textContent = timelineItemDetail(event);
    item.append(heading, label, time, detail);
    return item;
  }

  function timelineItemDetail(event) {
    const parts = [];
    const formatItems = (values) => (Array.isArray(values) ? values : [])
      .filter((value) => value && Number(value.item_id) > 0 && Number(value.count) > 0)
      .map((value) => `objeto ${Number(value.item_id)} ×${Number(value.count)}`)
      .join(", ");
    const acquired = formatItems(event.items || event.added);
    const removed = formatItems(event.removed);
    if (acquired) parts.push(`Añadido: ${acquired}`);
    if (removed) parts.push(`Retirado: ${removed}`);
    if (typeof event.reason === "string" && event.reason.trim()) parts.push(`Motivo registrado: ${event.reason.trim().replaceAll("_", " ")}`);
    return parts.join(". ") || "Sin detalle adicional de objetos.";
  }

  function textListItem(text) {
    const item = document.createElement("li");
    item.textContent = text;
    return item;
  }

  function caveatLabel(value) {
    const labels = {
      inferred_purchase: "Las compras se infieren a partir de cambios de inventario y oro.",
      inferred_return_to_base: "Los regresos a base se infieren; no son eventos autoritativos.",
      incomplete_session: "La sesión terminó sin un evento terminal observado.",
      duration_lower_bound: "La duración mostrada es un mínimo basado en el último estado observado.",
      terminal_stats_last_observed: "Las estadísticas son las últimas observadas, no un resultado oficial.",
      outcome_unavailable: "LoL Engine no infiere victoria o derrota con datos locales.",
      malformed_event_payload: "Se omitió un evento local que no pudo validarse.",
      source_integrity_degraded: "Parte del origen local presenta una inconsistencia y se muestra de forma parcial.",
    };
    return labels[value] || `Limitación local: ${String(value).replaceAll("_", " ")}.`;
  }

  async function loadPreferences() {
    try {
      const response = await fetch("/api/privacy/history", { headers: { Accept: "application/json" }, cache: "no-store" });
      if (!response.ok) throw new Error(`privacy ${response.status}`);
      const preferences = await response.json();
      elements.retentionDays.value = preferences.retention_days || "";
      elements.retentionMaximum.value = preferences.max_completed_matches || "";
    } catch (_error) {
      elements.privacyStatus.textContent = "No se pudieron cargar las preferencias de retención.";
    }
  }

  async function savePreferences(event) {
    event.preventDefault();
    const days = positiveIntegerOrNull(elements.retentionDays.value);
    const maximum = positiveIntegerOrNull(elements.retentionMaximum.value);
    elements.privacyStatus.textContent = "Guardando preferencias…";
    try {
      const response = await fetch("/api/privacy/history", {
        method: "PUT", headers: { "Content-Type": "application/json", Accept: "application/json" },
        body: JSON.stringify({ retention_days: days, max_completed_matches: maximum }),
      });
      if (!response.ok) throw new Error(`privacy ${response.status}`);
      elements.privacyStatus.textContent = days || maximum ? "Retención automática actualizada." : "Retención automática desactivada.";
      await loadHistory(false);
    } catch (_error) {
      elements.privacyStatus.textContent = "No se pudieron guardar las preferencias.";
    }
  }

  async function deleteSelectedMatch() {
    if (!selectedMatchID || !window.confirm("¿Eliminar esta partida y sus eventos locales? Esta acción no se puede deshacer.")) return;
    try {
      const response = await fetch(`/api/history/matches/${encodeURIComponent(selectedMatchID)}`, {
        method: "DELETE", headers: { "Content-Type": "application/json", Accept: "application/json" },
        body: JSON.stringify({ confirmation: "DELETE_LOCAL_MATCH_HISTORY" }),
      });
      if (response.status === 409) {
        elements.detailMeta.textContent = "La partida todavía está terminando de guardarse. Inténtalo de nuevo en unos segundos.";
        return;
      }
      if (!response.ok) throw new Error(`delete ${response.status}`);
      elements.detail.hidden = true;
      selectedMatchID = "";
      await loadHistory(false);
    } catch (_error) {
      elements.detailMeta.textContent = "No se pudo eliminar esta partida local.";
    }
  }

  async function clearAllHistory() {
    if (!window.confirm("¿Eliminar todo el historial de partidas finalizadas? La partida activa y las preferencias se conservarán.")) return;
    elements.privacyStatus.textContent = "Eliminando historial finalizado…";
    try {
      const response = await fetch("/api/history/matches", {
        method: "DELETE", headers: { "Content-Type": "application/json", Accept: "application/json" },
        body: JSON.stringify({ confirmation: "DELETE_LOCAL_HISTORY" }),
      });
      if (!response.ok) throw new Error(`clear ${response.status}`);
      elements.detail.hidden = true;
      selectedMatchID = "";
      elements.privacyStatus.textContent = "Historial finalizado eliminado. Las copias de seguridad del sistema quedan fuera del control de LoL Engine.";
      await loadHistory(false);
    } catch (_error) {
      elements.privacyStatus.textContent = "No se pudo eliminar todo el historial; una partida puede seguir terminando de guardarse.";
    }
  }

  async function loadRiotStatus() {
    elements.riotStatus.textContent = "Comprobando configuración opcional…";
    try {
      const response = await fetch("/api/riot/status", { headers: { Accept: "application/json" }, cache: "no-store" });
      if (!response.ok) throw new Error(`riot status ${response.status}`);
      const status = state.normalizeRiotStatus(await response.json());
      renderRiotStatus(status);
    } catch (_error) {
      elements.riotStatus.textContent = "El estado del enriquecimiento opcional no está disponible. El historial local sigue funcionando.";
      elements.riotLinkForm.hidden = true;
      elements.riotStatisticsForm.hidden = true;
    }
  }

  function renderRiotStatus(status) {
    elements.riotAccounts.replaceChildren();
    currentRiotAccountID = status.accounts.length ? status.accounts[0].id : "";
    if (!status.enabled || status.mode === "disabled") {
      elements.riotStatus.textContent = status.accounts.length ?
        "Enriquecimiento Riot desactivado. No se usa la red; aún puedes borrar los datos oficiales vinculados que ya existían." :
        "Enriquecimiento Riot desactivado. No se usa cuenta, clave ni red; el historial local conserva todas sus funciones.";
      elements.riotLinkForm.hidden = true;
      elements.riotStatisticsForm.hidden = true;
      renderRiotAccounts(status.accounts, false);
      return;
    }
    if (!status.configured || status.keyRequired) {
      elements.riotStatus.textContent = status.mode === "direct" ?
        "Modo privado directo sin credencial válida. Configura LOL_ENGINE_RIOT_API_KEY fuera del navegador y reinicia la aplicación." :
        "El backend Riot no está configurado. No se enviará ningún dato.";
      elements.riotLinkForm.hidden = true;
      elements.riotStatisticsForm.hidden = true;
      renderRiotAccounts(status.accounts, false);
      return;
    }
    const latestSync = status.syncs.find((sync) => sync.accountID === currentRiotAccountID);
    const reconciliation = status.reconciliations.find((entry) => entry.accountID === currentRiotAccountID);
    const syncSuffix = latestSync ? ` Última sincronización: ${latestSync.status || "estado no disponible"}${latestSync.errorKind ? ` (${latestSync.errorKind})` : ""}.` : "";
    const reconciliationSuffix = reconciliation && reconciliation.status === "pending" ?
      " La ingesta oficial terminó; la vinculación con el historial local sigue pendiente y se reintentará antes de descargar otra ventana." : "";
    elements.riotStatus.textContent = status.accounts.length ?
      `${status.accounts.length} cuenta local vinculada. Los resultados oficiales son opcionales y observacionales.` :
      "Configuración disponible. Vincular una cuenta requiere consentimiento explícito.";
    elements.riotStatus.textContent += syncSuffix;
    elements.riotStatus.textContent += reconciliationSuffix;
    elements.riotLinkForm.hidden = status.accounts.length > 0;
    elements.riotStatisticsForm.hidden = status.accounts.length === 0;
    renderRiotAccounts(status.accounts, true);
  }

  function renderRiotAccounts(accounts, allowSync) {
    const rows = accounts.map((account) => {
      const item = document.createElement("li");
      const summary = document.createElement("span");
      const linkedAt = account.linkedAt ? new Date(account.linkedAt).toLocaleString("es-PE") : "fecha no disponible";
      summary.textContent = `Cuenta local · ${account.platform || "plataforma no disponible"} · ${account.region || "región no disponible"} · vinculada ${linkedAt}`;
      const actions = document.createElement("div");
      actions.className = "riot-account-actions";
      if (allowSync) {
        const syncButton = document.createElement("button");
        syncButton.type = "button";
        syncButton.className = "recalculate-button";
        syncButton.textContent = "Sincronizar ahora";
        syncButton.addEventListener("click", () => syncRiotAccount(account.id, syncButton));
        actions.append(syncButton);
      }
      const deleteButton = document.createElement("button");
      deleteButton.type = "button";
      deleteButton.className = "history-danger-button";
      deleteButton.textContent = "Desvincular y borrar datos oficiales";
      deleteButton.addEventListener("click", () => deleteRiotAccount(account.id));
      actions.append(deleteButton);
      item.append(summary, actions);
      return item;
    });
    elements.riotAccounts.replaceChildren(...rows);
  }

  async function linkRiotAccount(event) {
    event.preventDefault();
    if (!elements.riotConsent.checked) {
      elements.riotStatus.textContent = "Debes aceptar explícitamente el vínculo y almacenamiento local normalizado.";
      return;
    }
    elements.riotStatus.textContent = "Vinculando cuenta…";
    try {
      const response = await fetch("/api/riot/accounts", {
        method: "POST", headers: { "Content-Type": "application/json", Accept: "application/json" },
        body: JSON.stringify({ game_name: elements.riotGameName.value, tag_line: elements.riotTagLine.value, platform: elements.riotPlatform.value, consent: true }),
      });
      if (!response.ok) throw new Error(`link ${response.status}`);
      elements.riotLinkForm.reset();
      await loadRiotStatus();
    } catch (_error) {
      elements.riotStatus.textContent = "No se pudo vincular la cuenta. Revisa la región, la configuración privada y los límites del servicio.";
    }
  }

  async function syncRiotAccount(accountID, button) {
    button.disabled = true;
    elements.riotStatus.textContent = "Sincronizando una ventana histórica acotada…";
    try {
      const response = await fetch("/api/riot/sync", {
        method: "POST", headers: { "Content-Type": "application/json", Accept: "application/json" },
        body: JSON.stringify({ account_id: accountID, filter: {} }),
      });
      const payload = await response.json();
      const result = payload && payload.result ? payload.result : payload;
      if (!response.ok) {
        elements.riotStatus.textContent = `Sincronización parcial (${result && result.error_kind || "error tipificado"}). Puedes reanudarla sin duplicar partidas.`;
        return;
      }
      if (result && result.status === "complete" && result.reconciliation_status === "pending") {
        elements.riotStatus.textContent = "La ingesta oficial terminó. La vinculación con el historial local quedó pendiente y se reintentará antes de descargar otra ventana.";
        return;
      }
      if (result && result.reconciliation_retried && result.reconciliation_status === "complete") {
        elements.riotStatus.textContent = "La vinculación pendiente con el historial local se completó sin descargar otra ventana oficial.";
        return;
      }
      elements.riotStatus.textContent = `Sincronización ${result.status || "completada"}: ${Number(result.fetched) || 0} partidas normalizadas, ${Number(result.pending) || 0} pendientes.`;
    } catch (_error) {
      elements.riotStatus.textContent = "No se pudo completar la sincronización. El progreso confirmado se conserva para reanudarlo.";
    } finally {
      button.disabled = false;
    }
  }

  async function deleteRiotAccount(accountID) {
    if (!window.confirm("¿Desvincular la cuenta y borrar sus datos oficiales normalizados? El historial local de partidas se conservará.")) return;
    elements.riotStatus.textContent = "Borrando datos oficiales vinculados…";
    try {
      const response = await fetch(`/api/riot/accounts/${encodeURIComponent(accountID)}`, {
        method: "DELETE", headers: { "Content-Type": "application/json", Accept: "application/json" },
        body: JSON.stringify({ confirmation: "DELETE_RIOT_ACCOUNT_DATA" }),
      });
      if (!response.ok) throw new Error(`delete Riot ${response.status}`);
      elements.riotStatisticsResults.replaceChildren();
      elements.riotStatisticsNotice.textContent = "Datos oficiales eliminados. El historial local permanece disponible.";
      await loadRiotStatus();
    } catch (_error) {
      elements.riotStatus.textContent = "No se pudieron borrar los datos oficiales vinculados.";
    }
  }

  async function loadStatistics(event) {
    event.preventDefault();
    if (!currentRiotAccountID) return;
    elements.riotStatisticsNotice.textContent = "Calculando agregados observados…";
    const query = new URLSearchParams({ account_id: currentRiotAccountID, minimum_sample: "10" });
    if (elements.riotStatsPatch.value.trim()) query.set("patch", elements.riotStatsPatch.value.trim());
    if (elements.riotStatsChampion.value.trim()) query.set("champion", elements.riotStatsChampion.value.trim());
    if (elements.riotStatsRole.value) query.set("role", elements.riotStatsRole.value);
    try {
      const response = await fetch(`/api/history/statistics?${query}`, { headers: { Accept: "application/json" }, cache: "no-store" });
      if (!response.ok) throw new Error(`statistics ${response.status}`);
      renderStatistics(state.normalizeStatistics(await response.json()));
    } catch (_error) {
      elements.riotStatisticsNotice.textContent = "No se pudieron construir las estadísticas observadas.";
    }
  }

  function renderStatistics(report) {
    elements.riotStatisticsNotice.textContent = report.observationalNotice || "Resultados observados; no implican causalidad ni una recomendación.";
    if (report.sourceTruncated) elements.riotStatisticsNotice.textContent += " Se alcanzó el límite acotado de fuentes; ajusta los filtros para un agregado completo.";
    const rows = report.groups.map((group) => {
      const item = document.createElement("li");
      item.className = "history-card";
      const heading = document.createElement("strong");
      heading.textContent = `${group.champion} · ${group.role} · parche ${group.patch || "no disponible"}`;
      const result = document.createElement("span");
      result.textContent = `${group.games} partidas observadas · ${group.wins} victorias · ${(group.winRate * 100).toFixed(1)}% observado`;
      const evidence = document.createElement("span");
      const matchup = group.opponentChampion && group.opponentProvenance === "unique_same_role" ? ` · frente a ${group.opponentChampion} (rol único)` : " · enfrentamiento no determinado";
      evidence.textContent = `Rol: ${group.roleProvenance}. Rango: ${group.rankBucket} (${group.rankProvenance}). Objetos finales sin baratija: ${group.finalItems.join(", ") || "ninguno"}${matchup}.`;
      const warning = document.createElement("span");
      warning.textContent = group.minimumSampleWarning ? "Muestra pequeña: interpreta este agregado con cautela." : "Muestra por encima del mínimo configurado.";
      item.append(heading, result, evidence, warning);
      return item;
    });
    if (!rows.length) rows.push(textListItem("No hay partidas oficiales normalizadas para estos filtros."));
    elements.riotStatisticsResults.replaceChildren(...rows);
  }

  function positiveIntegerOrNull(value) {
    const number = Number(value);
    return Number.isSafeInteger(number) && number > 0 ? number : null;
  }
})();
