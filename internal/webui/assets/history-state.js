(function (root, factory) {
  "use strict";

  const api = factory();
  if (typeof module === "object" && module.exports) {
    module.exports = api;
  }
  if (root) {
    root.LoLEngineHistoryState = api;
  }
})(typeof globalThis === "object" ? globalThis : this, function () {
  "use strict";

  const roleLabels = Object.freeze({
    top: "Superior", jungle: "Jungla", middle: "Central", bottom: "Inferior", support: "Soporte",
  });

  function cleanString(value) {
    return typeof value === "string" ? value.trim() : "";
  }

  function finiteNonNegative(value, fallback = 0) {
    const number = Number(value);
    return Number.isFinite(number) && number >= 0 ? number : fallback;
  }

  function normalizeSummary(value) {
    const source = value && typeof value === "object" ? value : {};
    const stats = source.stats && typeof source.stats === "object" ? source.stats : null;
    return Object.freeze({
      matchID: cleanString(source.match_id),
      digest: cleanString(source.digest),
      champion: cleanString(source.champion_name) || "Campeón no disponible",
      role: roleLabels[cleanString(source.role)] || "Rol no disponible",
      roleKnown: source.role_known === true,
      completion: cleanString(source.completion) || "incomplete_inferred",
      startedAt: cleanString(source.started_at),
      endedAt: cleanString(source.ended_at),
      durationSeconds: finiteNonNegative(source.duration && source.duration.seconds),
      durationLowerBound: Boolean(source.duration && source.duration.lower_bound),
      stats: stats ? Object.freeze({
        kills: finiteNonNegative(stats.kills), deaths: finiteNonNegative(stats.deaths),
        assists: finiteNonNegative(stats.assists), creepScore: finiteNonNegative(stats.creep_score),
        level: finiteNonNegative(stats.level), quality: cleanString(stats.quality),
      }) : null,
      degraded: Array.isArray(source.integrity_issues) && source.integrity_issues.length > 0,
    });
  }

  function normalizePage(value) {
    const source = value && typeof value === "object" ? value : {};
    const matches = Array.isArray(source.matches) ? source.matches.map(normalizeSummary).filter((match) => match.matchID) : [];
    return Object.freeze({ matches, nextCursor: cleanString(source.next_cursor) });
  }

  function mergePage(current, page) {
    const prior = Array.isArray(current) ? current : [];
    const normalized = Array.isArray(page) ? page : (page && Array.isArray(page.matches) ? page.matches : []);
    const seen = new Set();
    const result = [];
    for (const match of [...prior, ...normalized]) {
      if (!match || !match.matchID || seen.has(match.matchID)) continue;
      seen.add(match.matchID);
      result.push(match);
    }
    return result;
  }

  function formatDuration(seconds, lowerBound) {
    const total = Math.floor(finiteNonNegative(seconds));
    const minutes = Math.floor(total / 60);
    const remainder = total % 60;
    const formatted = `${minutes}:${String(remainder).padStart(2, "0")}`;
    return lowerBound ? `al menos ${formatted}` : formatted;
  }

  function completionLabel(value) {
    if (value === "complete_terminal") return "Final observado";
    if (value === "active") return "Partida activa";
    return "Final incompleto o inferido";
  }

  function confidenceLabel(value) {
    const confidence = Math.max(0, Math.min(1, finiteNonNegative(value)));
    if (confidence >= 0.85) return `Alta (${Math.round(confidence * 100)}%)`;
    if (confidence >= 0.6) return `Media (${Math.round(confidence * 100)}%)`;
    return `Baja (${Math.round(confidence * 100)}%)`;
  }

  function normalizeRiotStatus(value) {
    const source = value && typeof value === "object" ? value : {};
    const accounts = (Array.isArray(source.accounts) ? source.accounts : []).map((account) => Object.freeze({
      id: cleanString(account && account.id), region: cleanString(account && account.region),
      platform: cleanString(account && account.platform), linkedAt: cleanString(account && account.linked_at),
      updatedAt: cleanString(account && account.updated_at), autoSync: Boolean(account && account.auto_sync),
    })).filter((account) => account.id);
    const syncs = (Array.isArray(source.syncs) ? source.syncs : []).map((sync) => Object.freeze({
      accountID: cleanString(sync && sync.account_id), status: cleanString(sync && sync.status),
      errorKind: cleanString(sync && sync.last_error_kind), updatedAt: cleanString(sync && sync.updated_at),
      nextStart: finiteNonNegative(sync && sync.next_start), windowEndUnix: finiteNonNegative(sync && sync.window_end_unix),
    })).filter((sync) => sync.accountID);
    const reconciliations = (Array.isArray(source.reconciliations) ? source.reconciliations : []).map((entry) => Object.freeze({
      accountID: cleanString(entry && entry.account_id), status: cleanString(entry && entry.status),
      errorKind: cleanString(entry && entry.last_error_kind), updatedAt: cleanString(entry && entry.updated_at),
      windowEndUnix: finiteNonNegative(entry && entry.window_end_unix),
    })).filter((entry) => entry.accountID);
    return Object.freeze({
      mode: cleanString(source.mode) || "disabled", enabled: source.enabled === true,
      configured: source.configured === true, keyRequired: source.key_required === true,
      region: cleanString(source.region), accounts, syncs, reconciliations,
    });
  }

  function normalizeStatistics(value) {
    const source = value && typeof value === "object" ? value : {};
    const groups = (Array.isArray(source.groups) ? source.groups : []).map((entry) => {
      const key = entry && entry.key && typeof entry.key === "object" ? entry.key : {};
      const finalItems = (Array.isArray(key.final_items) ? key.final_items : [])
        .map((item) => Number(item)).filter((item) => Number.isSafeInteger(item) && item > 0).sort((left, right) => left - right);
      return Object.freeze({
        patch: cleanString(key.patch), champion: cleanString(key.champion_name) || "Campeón no disponible",
        role: roleLabels[cleanString(key.role)] || "Rol no disponible", roleProvenance: cleanString(key.role_provenance) || "unknown",
        rankBucket: cleanString(key.rank_bucket) || "unknown", rankProvenance: cleanString(key.rank_provenance) || "unavailable",
        opponentChampion: cleanString(key.opponent_champion), opponentProvenance: cleanString(key.opponent_provenance) || "unknown",
        finalItems, games: finiteNonNegative(entry && entry.games), wins: finiteNonNegative(entry && entry.wins),
        winRate: Math.max(0, Math.min(1, finiteNonNegative(entry && entry.win_rate))),
        minimumSampleWarning: Boolean(entry && entry.minimum_sample_warning),
      });
    });
    return Object.freeze({
      totalGames: finiteNonNegative(source.total_games), minimumSample: finiteNonNegative(source.minimum_sample),
      observationalNotice: cleanString(source.observational_notice), sourceTruncated: source.source_truncated === true, groups,
    });
  }

  return Object.freeze({ normalizeSummary, normalizePage, mergePage, formatDuration, completionLabel, confidenceLabel, normalizeRiotStatus, normalizeStatistics });
});
