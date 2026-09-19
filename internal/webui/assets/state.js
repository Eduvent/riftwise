(function (root, factory) {
  "use strict";

  const api = factory();
  if (typeof module === "object" && module.exports) {
    module.exports = api;
  }
  if (root) {
    root.LoLEngineState = api;
  }
})(typeof globalThis === "object" ? globalThis : this, function () {
  "use strict";

  function cleanString(value) {
    return typeof value === "string" ? value.trim() : "";
  }

  function wholeNonNegative(value, fallback) {
    const number = Number(value);
    return Number.isSafeInteger(number) && number >= 0 ? number : fallback;
  }

  function evaluateDashboardVersion(currentValue, nextValue) {
    const current = currentValue && typeof currentValue === "object" ? currentValue : {};
    const next = nextValue && typeof nextValue === "object" ? nextValue : {};
    const currentEpoch = cleanString(current.epoch);
    const nextEpoch = cleanString(next.epoch);
    const currentRevision = wholeNonNegative(current.revision, -1);
    const nextRevision = wholeNonNegative(next.revision, -1);
    const retiredEpochs = Array.isArray(current.retiredEpochs) ?
      current.retiredEpochs.map(cleanString).filter(Boolean) : [];

    if (!nextEpoch || nextRevision < 0) {
      return { accept: false, epochChanged: false, state: { epoch: currentEpoch, revision: currentRevision, retiredEpochs } };
    }
    if (!currentEpoch) {
      return { accept: true, epochChanged: false, state: { epoch: nextEpoch, revision: nextRevision, retiredEpochs } };
    }
    if (nextEpoch === currentEpoch) {
      return {
        accept: nextRevision >= currentRevision,
        epochChanged: false,
        state: { epoch: currentEpoch, revision: Math.max(currentRevision, nextRevision), retiredEpochs },
      };
    }
    if (retiredEpochs.includes(nextEpoch)) {
      return { accept: false, epochChanged: false, state: { epoch: currentEpoch, revision: currentRevision, retiredEpochs } };
    }
    const retired = retiredEpochs.includes(currentEpoch) ? retiredEpochs : [...retiredEpochs, currentEpoch];
    return { accept: true, epochChanged: true, state: { epoch: nextEpoch, revision: nextRevision, retiredEpochs: retired } };
  }

  function acceptsDecisionSession(currentSessionID, eventSessionID) {
    const current = cleanString(currentSessionID);
    return current !== "" && current === cleanString(eventSessionID);
  }

  function shouldReconcileHistory(value) {
    const state = value && typeof value === "object" ? value : {};
    const currentSession = cleanString(state.currentSessionID);
    const loadedSession = cleanString(state.loadedSessionID);
    const dashboardSession = cleanString(state.dashboardSessionID);
    const highestREST = wholeNonNegative(state.highestRESTSequence, 0);
    const dashboardLatest = wholeNonNegative(state.dashboardLatestSequence, 0);
    return state.inFlight !== true && currentSession !== "" && currentSession === loadedSession &&
      currentSession === dashboardSession && dashboardLatest > highestREST;
  }

  function pregameOptionsState(value) {
    const state = value && typeof value === "object" ? value : {};
    return {
      generation: wholeNonNegative(state.generation, 0),
      expectedPatch: cleanString(state.expectedPatch),
      loadedPatch: cleanString(state.loadedPatch),
      ready: state.ready === true,
    };
  }

  function beginPregameOptionsRequest(currentValue, activePatch) {
    const current = pregameOptionsState(currentValue);
    return {
      generation: current.generation + 1,
      expectedPatch: cleanString(activePatch),
      loadedPatch: "",
      ready: false,
    };
  }

  function evaluatePregameOptionsResponse(currentValue, requestGeneration, responsePatch) {
    const current = pregameOptionsState(currentValue);
    const generation = wholeNonNegative(requestGeneration, -1);
    const patch = cleanString(responsePatch);
    if (generation !== current.generation || !patch || (current.expectedPatch && current.expectedPatch !== patch)) {
      return { accept: false, state: current };
    }
    return {
      accept: true,
      state: {
        generation: current.generation,
        expectedPatch: current.expectedPatch || patch,
        loadedPatch: patch,
        ready: true,
      },
    };
  }

  function canUsePregameOptions(value, activePatch) {
    const state = pregameOptionsState(value);
    const patch = cleanString(activePatch);
    return state.ready && state.loadedPatch !== "" && (!patch || patch === state.loadedPatch);
  }

  function canApplyPregamePlanRequest(value) {
    const request = value && typeof value === "object" ? value : {};
    const requestGeneration = wholeNonNegative(request.requestGeneration, -1);
    const currentGeneration = wholeNonNegative(request.currentGeneration, -1);
    const requestOptionsGeneration = wholeNonNegative(request.requestOptionsGeneration, -1);
    const options = pregameOptionsState(request.optionsState);
    const requestPatch = cleanString(request.requestPatch);
    return requestGeneration > 0 && requestGeneration === currentGeneration &&
      requestOptionsGeneration === options.generation && requestPatch !== "" &&
      requestPatch === options.loadedPatch && canUsePregameOptions(options, request.activePatch);
  }

  function applyPregamePlanEffect(value, effect) {
    if (!canApplyPregamePlanRequest(value)) {
      return false;
    }
    if (typeof effect === "function") {
      effect();
    }
    return true;
  }

  const personalizationStyles = new Set(["safe", "balanced", "aggressive"]);

  function normalizePersonalizationPreferences(value) {
    const source = value && typeof value === "object" ? value : {};
    const normalizedIDs = (values) => Array.from(new Set((Array.isArray(values) ? values : [])
      .map((item) => wholeNonNegative(item, 0)).filter((item) => item > 0))).sort((left, right) => left - right);
    const style = cleanString(source.style);
    return {
      style: personalizationStyles.has(style) ? style : "balanced",
      preferred_item_ids: normalizedIDs(source.preferred_item_ids),
      avoided_item_ids: normalizedIDs(source.avoided_item_ids),
      feedback_enabled: source.feedback_enabled === true,
      version: wholeNonNegative(source.version, 0),
    };
  }

  function personalizationUpdatePayload(value) {
    const normalized = normalizePersonalizationPreferences(value);
    return {
      style: normalized.style,
      preferred_item_ids: normalized.preferred_item_ids,
      avoided_item_ids: normalized.avoided_item_ids,
      feedback_enabled: normalized.feedback_enabled,
    };
  }

  function beginPersonalizationMutation(currentValue, optimisticValue, generation) {
    return {
      generation: wholeNonNegative(generation, 0) + 1,
      previous: normalizePersonalizationPreferences(currentValue),
      visible: normalizePersonalizationPreferences(optimisticValue),
      pending: true,
    };
  }

  function settlePersonalizationMutation(mutationValue, requestGeneration, success, serverValue) {
    const mutation = mutationValue && typeof mutationValue === "object" ? mutationValue : {};
    if (wholeNonNegative(requestGeneration, -1) !== wholeNonNegative(mutation.generation, -2)) {
      return { apply: false, state: mutation };
    }
    const visible = success === true ? normalizePersonalizationPreferences(serverValue) :
      normalizePersonalizationPreferences(mutation.previous);
    return { apply: true, state: { ...mutation, visible, pending: false, rolledBack: success !== true } };
  }

  function feedbackChoiceKey(sessionID, decisionEventID, itemID) {
    const session = cleanString(sessionID);
    const decision = cleanString(decisionEventID);
    const item = wholeNonNegative(itemID, 0);
    return session && decision && item > 0 ? `${session}:${decision}:${item}` : "";
  }

  function feedbackStateForSession(value, sessionID) {
    const current = value && typeof value === "object" ? value : {};
    const nextSession = cleanString(sessionID);
    if (cleanString(current.sessionID) !== nextSession) {
      return { sessionID: nextSession, submitted: {}, pending: {} };
    }
    return {
      sessionID: nextSession,
      submitted: { ...(current.submitted || {}) },
      pending: { ...(current.pending || {}) },
    };
  }

  function markFeedbackSubmitted(value, key, clientID) {
    const current = feedbackStateForSession(value, value && value.sessionID);
    const normalizedKey = cleanString(key);
    if (!normalizedKey || current.submitted[normalizedKey]) {
      return { changed: false, state: current };
    }
    return { changed: true, state: { ...current, submitted: { ...current.submitted, [normalizedKey]: cleanString(clientID) } } };
  }

  function beginFeedbackSubmission(value, requestValue) {
    const request = requestValue && typeof requestValue === "object" ? requestValue : {};
    const current = feedbackStateForSession(value, value && value.sessionID);
    const key = cleanString(request.key);
    const sessionID = cleanString(request.sessionID);
    if (!key || !sessionID || sessionID !== current.sessionID || current.submitted[key]) {
      return { accepted: false, reused: false, state: current, request: null };
    }
    const existing = current.pending[key];
    if (existing && typeof existing === "object") {
      return { accepted: true, reused: true, state: current, request: { ...existing } };
    }
    const pending = {
      key,
      clientID: cleanString(request.clientID),
      decisionEventID: cleanString(request.decisionEventID),
      itemID: wholeNonNegative(request.itemID, 0),
      value: request.value === "not_useful" ? "not_useful" : "useful",
      sessionID,
      generation: wholeNonNegative(request.generation, 0),
    };
    if (!pending.clientID || !pending.decisionEventID || pending.itemID <= 0 || pending.generation <= 0) {
      return { accepted: false, reused: false, state: current, request: null };
    }
    const next = { ...current, pending: { ...current.pending, [key]: pending } };
    return { accepted: true, reused: false, state: next, request: { ...pending } };
  }

  function settleFeedbackSubmission(value, settlementValue) {
    const settlement = settlementValue && typeof settlementValue === "object" ? settlementValue : {};
    const current = feedbackStateForSession(value, value && value.sessionID);
    const key = cleanString(settlement.key);
    const pending = current.pending[key];
    if (!pending || cleanString(settlement.sessionID) !== current.sessionID ||
        cleanString(settlement.clientID) !== cleanString(pending.clientID) ||
        wholeNonNegative(settlement.generation, -1) !== wholeNonNegative(pending.generation, -2)) {
      return { apply: false, state: current };
    }
    const outcome = cleanString(settlement.outcome);
    if (outcome === "ambiguous") {
      return { apply: true, state: current };
    }
    const pendingNext = { ...current.pending };
    delete pendingNext[key];
    if (outcome === "success") {
      return {
        apply: true,
        state: { ...current, pending: pendingNext, submitted: { ...current.submitted, [key]: pending.clientID } },
      };
    }
    if (outcome === "rejected") {
      return { apply: true, state: { ...current, pending: pendingNext } };
    }
    return { apply: false, state: current };
  }

  function canApplyFeedbackEffect(requestValue, currentSessionID, currentGeneration) {
    const request = requestValue && typeof requestValue === "object" ? requestValue : {};
    return cleanString(request.sessionID) !== "" && cleanString(request.sessionID) === cleanString(currentSessionID) &&
      wholeNonNegative(request.generation, -1) === wholeNonNegative(currentGeneration, -2);
  }

  function feedbackEvidenceMatchesRecommendation(eventValue, recommendationValue) {
    const event = eventValue && typeof eventValue === "object" ? eventValue : {};
    const evidence = event.evidence && typeof event.evidence === "object" ? event.evidence : null;
    const recommendation = recommendationValue && typeof recommendationValue === "object" ? recommendationValue : null;
    if (!evidence || !recommendation || !["ready", "save_for_component"].includes(cleanString(recommendation.status))) {
      return false;
    }
    const itemID = (value) => wholeNonNegative(value && value.id, 0);
    const strings = (values) => (Array.isArray(values) ? values : []).map(cleanString).filter(Boolean).sort();
    const normalizeRules = (value) => {
      const rules = value && typeof value === "object" ? value : {};
      return {
        version: cleanString(rules.version), patch: cleanString(rules.patch), source: cleanString(rules.source),
        fallback: rules.fallback === true, legacy: rules.legacy === true,
      };
    };
    const normalizeProfile = (value) => {
      if (!value || typeof value !== "object") return null;
      return {
        level: cleanString(value.level), profile_id: cleanString(value.profile_id),
        family_ids: strings(value.family_ids), ruleset_version: cleanString(value.ruleset_version),
        patch: cleanString(value.patch), champion_id: cleanString(value.champion_id), role: cleanString(value.role),
        champion_tag: cleanString(value.champion_tag), fallback: value.fallback === true,
        confidence_cap: Number.isFinite(value.confidence_cap) ? value.confidence_cap : 0,
      };
    };
    const normalizePersonalization = (value) => {
      if (!value || typeof value !== "object") return null;
      return {
        version: wholeNonNegative(value.version, 0), style: cleanString(value.style),
        reason_codes: strings(value.reason_codes), warning_codes: strings(value.warning_codes),
      };
    };
    const evidenceChoices = (Array.isArray(evidence.ranked_choices) ? evidence.ranked_choices : []).map((entry, index) => {
      const choice = entry && typeof entry === "object" ? entry : {};
      return {
        rank: wholeNonNegative(choice.rank, index + 1), item: itemID(choice.item),
        score: Number.isSafeInteger(choice.score) ? choice.score : 0, category: cleanString(choice.category),
        reason_codes: strings(choice.reason_codes), critical: choice.critical === true,
      };
    });
    const currentChoices = (Array.isArray(recommendation.ranked_options) ? recommendation.ranked_options : []).map((entry, index) => {
      const choice = entry && typeof entry === "object" ? entry : {};
      return {
        rank: index + 1, item: itemID(choice.item), score: Number.isSafeInteger(choice.score) ? choice.score : 0,
        category: cleanString(choice.category), reason_codes: strings(choice.reason_codes), critical: choice.critical === true,
      };
    });
    const persisted = {
      status: cleanString(evidence.status), patch: cleanString(evidence.patch), phase: cleanString(evidence.phase),
      strategy: cleanString(evidence.strategy), target: itemID(evidence.target), component: itemID(evidence.component),
      rules: normalizeRules(evidence.rules), profile: normalizeProfile(evidence.profile),
      personalization: normalizePersonalization(evidence.personalization), choices: evidenceChoices,
    };
    const current = {
      status: cleanString(recommendation.status), patch: cleanString(recommendation.patch), phase: cleanString(recommendation.phase),
      strategy: cleanString(recommendation.strategy), target: itemID(recommendation.target_item), component: itemID(recommendation.next_component),
      rules: normalizeRules(recommendation.rules), profile: normalizeProfile(recommendation.profile),
      personalization: normalizePersonalization(recommendation.personalization), choices: currentChoices,
    };
    return JSON.stringify(persisted) === JSON.stringify(current);
  }

  const pregamePressureKeys = Object.freeze([
    "physical", "magic", "crowd_control", "healing", "shielding", "armor",
    "magic_resist", "burst", "sustained", "slows", "lane_pressure",
  ]);
  const pregamePressureValues = new Set(["unset", "none", "low", "medium", "high"]);
  const pregameRoles = new Set(["top", "jungle", "middle", "bottom", "support"]);
  const pregameStages = new Set(["lane_start", "first_recall", "mid_game", "late_game"]);
  const pregameStrategies = new Set(["balanced", "split_push", "teamfight"]);

  function buildPregameScenario(value) {
    const draft = value && typeof value === "object" ? value : {};
    const pressureDraft = draft.pressures && typeof draft.pressures === "object" ? draft.pressures : {};
    const pressures = {};
    pregamePressureKeys.forEach((key) => {
      const selected = cleanString(pressureDraft[key]);
      pressures[key] = pregamePressureValues.has(selected) ? selected : "unset";
    });
    const inventoryDraft = Array.isArray(draft.inventory) ? draft.inventory : [];
    const inventory = inventoryDraft.flatMap((entry) => {
      const row = entry && typeof entry === "object" ? entry : {};
      const itemID = wholeNonNegative(row.itemID, 0);
      if (itemID <= 0) {
        return [];
      }
      return [{ item_id: itemID, count: wholeNonNegative(row.count, 0) }];
    });
    return {
      champion: cleanString(draft.champion),
      role: cleanString(draft.role),
      stage: cleanString(draft.stage),
      strategy: cleanString(draft.strategy),
      lane_opponent: cleanString(draft.laneOpponent),
      gold: wholeNonNegative(draft.gold, -1),
      pressures,
      inventory,
    };
  }

  function validatePregameScenario(value) {
    const scenario = value && typeof value === "object" ? value : {};
    const errors = [];
    const add = (field, message) => errors.push({ field, message });
    if (!cleanString(scenario.champion)) {
      add("pregame-champion", "Selecciona un campeón.");
    }
    if (!pregameRoles.has(cleanString(scenario.role))) {
      add("pregame-role", "Selecciona un rol.");
    }
    if (!pregameStages.has(cleanString(scenario.stage))) {
      add("pregame-stage", "Selecciona una etapa.");
    }
    if (!pregameStrategies.has(cleanString(scenario.strategy))) {
      add("pregame-strategy", "Selecciona una estrategia.");
    }
    const gold = Number(scenario.gold);
    if (!Number.isSafeInteger(gold) || gold < 0 || gold > 100000) {
      add("pregame-gold", "El oro debe estar entre 0 y 100000.");
    }
    const inventory = Array.isArray(scenario.inventory) ? scenario.inventory : [];
    if (inventory.length > 7) {
      add("pregame-inventory", "Usa como máximo siete pilas de objetos.");
    }
    inventory.forEach((stack, index) => {
      if (!Number.isSafeInteger(stack.count) || stack.count < 1 || stack.count > 99) {
        add(`pregame-item-count-${index + 1}`, "La cantidad debe estar entre 1 y 99.");
      }
    });
    return errors;
  }

  return Object.freeze({
    evaluateDashboardVersion,
    acceptsDecisionSession,
    shouldReconcileHistory,
    beginPregameOptionsRequest,
    evaluatePregameOptionsResponse,
    canUsePregameOptions,
    canApplyPregamePlanRequest,
    applyPregamePlanEffect,
    normalizePersonalizationPreferences,
    personalizationUpdatePayload,
    beginPersonalizationMutation,
    settlePersonalizationMutation,
    feedbackChoiceKey,
    feedbackStateForSession,
    markFeedbackSubmitted,
    beginFeedbackSubmission,
    settleFeedbackSubmission,
    canApplyFeedbackEffect,
    feedbackEvidenceMatchesRecommendation,
    buildPregameScenario,
    validatePregameScenario,
  });
});
