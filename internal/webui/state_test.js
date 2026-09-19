"use strict";

const test = require("node:test");
const assert = require("node:assert/strict");
const state = require("./assets/state.js");

test("dashboard revision only rolls back for a new epoch", () => {
  const current = { epoch: "epoch-a", revision: 90, retiredEpochs: [] };
  const sameEpoch = state.evaluateDashboardVersion(current, { epoch: "epoch-a", revision: 2 });
  assert.equal(sameEpoch.accept, false);

  const restarted = state.evaluateDashboardVersion(current, { epoch: "epoch-b", revision: 2 });
  assert.equal(restarted.accept, true);
  assert.equal(restarted.epochChanged, true);
  assert.equal(restarted.state.epoch, "epoch-b");
  assert.equal(restarted.state.revision, 2);

  const delayedOldSnapshot = state.evaluateDashboardVersion(restarted.state, { epoch: "epoch-a", revision: 91 });
  assert.equal(delayedOldSnapshot.accept, false);
});

test("decision hints are accepted only for the current session", () => {
  assert.equal(state.acceptsDecisionSession("match-a", "match-a"), true);
  assert.equal(state.acceptsDecisionSession("match-a", "match-b"), false);
  assert.equal(state.acceptsDecisionSession("", "match-a"), false);
});

test("dashboard sequence gaps trigger authoritative REST reconciliation", () => {
  const base = {
    currentSessionID: "match-a",
    loadedSessionID: "match-a",
    dashboardSessionID: "match-a",
    highestRESTSequence: 4,
    dashboardLatestSequence: 6,
    inFlight: false,
  };
  assert.equal(state.shouldReconcileHistory(base), true);
  assert.equal(state.shouldReconcileHistory({ ...base, highestRESTSequence: 6 }), false);
  assert.equal(state.shouldReconcileHistory({ ...base, dashboardSessionID: "match-b" }), false);
  assert.equal(state.shouldReconcileHistory({ ...base, inFlight: true }), false);
});

test("manual pregame draft builds only the isolated scenario contract", () => {
  const scenario = state.buildPregameScenario({
    champion: " Ahri ", role: "middle", stage: "first_recall", strategy: "balanced",
    laneOpponent: " Zed ", gold: "1250",
    pressures: { physical: "none", magic: "high", crowd_control: "medium", unexpected: "high" },
    inventory: [
      { itemID: "1001", count: "1" },
      { itemID: "", count: "9" },
      { itemID: "1058", count: "2" },
    ],
    game: { kills: 12 }, revision: 99,
  });
  assert.deepEqual(scenario, {
    champion: "Ahri", role: "middle", stage: "first_recall", strategy: "balanced",
    lane_opponent: "Zed", gold: 1250,
    pressures: {
      physical: "none", magic: "high", crowd_control: "medium", healing: "unset",
      shielding: "unset", armor: "unset", magic_resist: "unset", burst: "unset",
      sustained: "unset", slows: "unset", lane_pressure: "unset",
    },
    inventory: [{ item_id: 1001, count: 1 }, { item_id: 1058, count: 2 }],
  });
  assert.equal("game" in scenario, false);
  assert.equal("revision" in scenario, false);
});

test("manual pregame validation reports accessible field targets", () => {
  const invalid = state.buildPregameScenario({ champion: "", role: "", stage: "", strategy: "", gold: "100001", inventory: [] });
  const errors = state.validatePregameScenario(invalid);
  assert.deepEqual(errors.map((error) => error.field), ["pregame-champion", "pregame-role", "pregame-stage", "pregame-strategy", "pregame-gold"]);

  const valid = state.buildPregameScenario({
    champion: "Garen", role: "top", stage: "lane_start", strategy: "split_push", gold: "0",
    pressures: {}, inventory: [{ itemID: "1001", count: "1" }],
  });
  assert.deepEqual(state.validatePregameScenario(valid), []);
});

test("manual pregame options reject stale patch responses and request races", () => {
  const requestA = state.beginPregameOptionsRequest({}, "16.18.1");
  const acceptedA = state.evaluatePregameOptionsResponse(requestA, requestA.generation, "16.18.1");
  assert.equal(acceptedA.accept, true);
  assert.equal(state.canUsePregameOptions(acceptedA.state, "16.18.1"), true);

  const requestB = state.beginPregameOptionsRequest(acceptedA.state, "16.19.1");
  assert.equal(state.canUsePregameOptions(requestB, "16.19.1"), false);

  const lateA = state.evaluatePregameOptionsResponse(requestB, requestA.generation, "16.18.1");
  assert.equal(lateA.accept, false);
  assert.equal(lateA.state.expectedPatch, "16.19.1");
  assert.equal(state.canUsePregameOptions(lateA.state, "16.19.1"), false);

  const currentRequestWithOldPatch = state.evaluatePregameOptionsResponse(requestB, requestB.generation, "16.18.1");
  assert.equal(currentRequestWithOldPatch.accept, false);
  assert.equal(state.canUsePregameOptions(currentRequestWithOldPatch.state, "16.19.1"), false);

  const acceptedB = state.evaluatePregameOptionsResponse(lateA.state, requestB.generation, "16.19.1");
  assert.equal(acceptedB.accept, true);
  assert.equal(state.canUsePregameOptions(acceptedB.state, "16.19.1"), true);

  const evenLaterA = state.evaluatePregameOptionsResponse(acceptedB.state, requestA.generation, "16.18.1");
  assert.equal(evenLaterA.accept, false);
  assert.equal(evenLaterA.state.loadedPatch, "16.19.1");
});

const stalePlanOutcomes = [
  { name: "success", stale: { requestGeneration: 4 }, mutate(ui) { ui.result = "old plan"; } },
  { name: "422 validation", stale: { requestOptionsGeneration: 8 }, mutate(ui) { ui.errors = ["old validation"]; ui.ariaInvalid = true; ui.focus = "old field"; } },
  { name: "non-2xx response", stale: { requestPatch: "16.18.1" }, mutate(ui) { ui.errors = ["old server error"]; ui.focus = "old summary"; } },
  { name: "network rejection", stale: { requestGeneration: 4 }, mutate(ui) { ui.errors = ["old network error"]; ui.focus = "old summary"; } },
];

stalePlanOutcomes.forEach((outcome) => {
  test(`stale manual plan ${outcome.name} cannot mutate the new generation`, () => {
    const ui = {
      errors: ["new generation error"],
      ariaInvalid: false,
      result: "new generation result",
      focus: "new generation focus",
    };
    const before = structuredClone(ui);
    const context = {
      requestGeneration: 5,
      currentGeneration: 5,
      requestOptionsGeneration: 9,
      requestPatch: "16.19.1",
      optionsState: { generation: 9, expectedPatch: "16.19.1", loadedPatch: "16.19.1", ready: true },
      activePatch: "16.19.1",
      ...outcome.stale,
    };
    const applied = state.applyPregamePlanEffect(context, () => outcome.mutate(ui));
    assert.equal(applied, false);
    assert.deepEqual(ui, before);
  });
});

test("current manual plan request may apply its response", () => {
  let appliedEffects = 0;
  const context = {
    requestGeneration: 5,
    currentGeneration: 5,
    requestOptionsGeneration: 9,
    requestPatch: "16.19.1",
    optionsState: { generation: 9, expectedPatch: "16.19.1", loadedPatch: "16.19.1", ready: true },
    activePatch: "16.19.1",
  };
  assert.equal(state.canApplyPregamePlanRequest(context), true);
  assert.equal(state.applyPregamePlanEffect(context, () => { appliedEffects += 1; }), true);
  assert.equal(appliedEffects, 1);
});

test("personalization optimistic mutation rolls back only its current failed request", () => {
  const baseline = { style: "balanced", preferred_item_ids: [3001], avoided_item_ids: [], feedback_enabled: false, version: 4 };
  const mutation = state.beginPersonalizationMutation(baseline, { ...baseline, style: "safe", feedback_enabled: true }, 8);
  assert.equal(mutation.visible.style, "safe");
  const stale = state.settlePersonalizationMutation(mutation, 8, false, {});
  assert.equal(stale.apply, false);
  const rolledBack = state.settlePersonalizationMutation(mutation, 9, false, {});
  assert.equal(rolledBack.apply, true);
  assert.equal(rolledBack.state.rolledBack, true);
  assert.deepEqual(rolledBack.state.visible, state.normalizePersonalizationPreferences(baseline));

  const succeeded = state.settlePersonalizationMutation(mutation, 9, true, { ...baseline, style: "aggressive", version: 5 });
  assert.equal(succeeded.state.visible.style, "aggressive");
  assert.equal(succeeded.state.rolledBack, false);
});

test("personalization update payload contains only fields accepted by the strict API", () => {
  const payload = state.personalizationUpdatePayload({
    style: "safe",
    preferred_item_ids: [3002, 3001],
    avoided_item_ids: [3003],
    feedback_enabled: true,
    version: 91,
    updated_at: "private implementation detail",
  });
  assert.deepEqual(payload, {
    style: "safe",
    preferred_item_ids: [3001, 3002],
    avoided_item_ids: [3003],
    feedback_enabled: true,
  });
  assert.equal(JSON.stringify(payload).includes("version"), false);
});

test("feedback ambiguous retry reuses one request tuple and logical id", () => {
  const key = state.feedbackChoiceKey("match-a", "decision-1", 3001);
  let current = state.feedbackStateForSession({}, "match-a");
  const first = state.beginFeedbackSubmission(current, {
    key, clientID: "client-1", decisionEventID: "decision-1", itemID: 3001,
    value: "useful", sessionID: "match-a", generation: 4,
  });
  assert.equal(first.accepted, true);
  assert.equal(first.request.clientID, "client-1");

  const ambiguous = state.settleFeedbackSubmission(first.state, {
    key, clientID: "client-1", sessionID: "match-a", generation: 4,
    outcome: "ambiguous",
  });
  assert.equal(ambiguous.apply, true);
  assert.equal(ambiguous.state.pending[key].clientID, "client-1");

  const retry = state.beginFeedbackSubmission(ambiguous.state, {
    key, clientID: "client-2", decisionEventID: "decision-1", itemID: 3001,
    value: "not_useful", sessionID: "match-a", generation: 4,
  });
  assert.equal(retry.accepted, true);
  assert.equal(retry.reused, true);
  assert.deepEqual(retry.request, first.request);

  const committed = state.settleFeedbackSubmission(retry.state, {
    key, clientID: "client-1", sessionID: "match-a", generation: 4,
    outcome: "success",
  });
  assert.equal(committed.apply, true);
  assert.equal(committed.state.submitted[key], "client-1");
  assert.equal(committed.state.pending[key], undefined);

  const duplicate = state.beginFeedbackSubmission(committed.state, {
    key, clientID: "client-3", decisionEventID: "decision-1", itemID: 3001,
    value: "useful", sessionID: "match-a", generation: 4,
  });
  assert.equal(duplicate.accepted, false);
  const nextSession = state.feedbackStateForSession(committed.state, "match-b");
  assert.deepEqual(nextSession.submitted, {});
  assert.deepEqual(nextSession.pending, {});
  assert.equal(nextSession.sessionID, "match-b");
});

test("late feedback failure cannot roll match A over match B or newer context", () => {
  const keyA = state.feedbackChoiceKey("match-a", "decision-a", 3001);
  const started = state.beginFeedbackSubmission(state.feedbackStateForSession({}, "match-a"), {
    key: keyA, clientID: "client-a", decisionEventID: "decision-a", itemID: 3001,
    value: "useful", sessionID: "match-a", generation: 7,
  });
  const matchB = state.feedbackStateForSession(started.state, "match-b");
  const lateSessionFailure = state.settleFeedbackSubmission(matchB, {
    key: keyA, clientID: "client-a", sessionID: "match-a", generation: 7,
    outcome: "rejected",
  });
  assert.equal(lateSessionFailure.apply, false);
  assert.deepEqual(lateSessionFailure.state, matchB);
	assert.equal(state.canApplyFeedbackEffect(started.request, "match-b", 7), false);
	assert.equal(state.canApplyFeedbackEffect(started.request, "match-a", 8), false);
	assert.equal(state.canApplyFeedbackEffect(started.request, "match-a", 7), true);

  const sameSessionNewGeneration = state.feedbackStateForSession(started.state, "match-a");
  const lateContextFailure = state.settleFeedbackSubmission(sameSessionNewGeneration, {
    key: keyA, clientID: "client-a", sessionID: "match-a", generation: 6,
    outcome: "rejected",
  });
  assert.equal(lateContextFailure.apply, false);
  assert.deepEqual(lateContextFailure.state, sameSessionNewGeneration);
});

test("feedback eligibility requires exact current persisted recommendation evidence", () => {
  const recommendation = {
    status: "ready",
    patch: "16.18.1",
    phase: "mid",
    strategy: "teamfight",
    rules: { version: "garen-v2", patch: "16.18.1", source: "external_exact", fallback: false, legacy: false },
    ranked_options: [
      { item: { id: 3001 }, score: 100, category: "core", reason_codes: ["core"] },
      { item: { id: 3002 }, score: 90, category: "situational", reason_codes: ["armor"] },
      { item: { id: 3003 }, score: 80, category: "boots", reason_codes: [] },
    ],
  };
  const event = {
    id: "decision-1", session_id: "match-a",
    evidence: {
      status: "ready", patch: "16.18.1", phase: "mid", strategy: "teamfight",
      rules: { version: "garen-v2", patch: "16.18.1", source: "external_exact", fallback: false, legacy: false },
      ranked_choices: [
        { rank: 1, item: { id: 3001 }, score: 100, category: "core", reason_codes: ["core"] },
        { rank: 2, item: { id: 3002 }, score: 90, category: "situational", reason_codes: ["armor"] },
        { rank: 3, item: { id: 3003 }, score: 80, category: "boots" },
      ],
    },
  };
  assert.equal(state.feedbackEvidenceMatchesRecommendation(event, recommendation), true);
  assert.equal(state.feedbackEvidenceMatchesRecommendation(event, { ...recommendation, phase: "late" }), false);
  assert.equal(state.feedbackEvidenceMatchesRecommendation(event, { ...recommendation, strategy: "split_push" }), false);
  assert.equal(state.feedbackEvidenceMatchesRecommendation(event, { ...recommendation, patch: "16.19.1" }), false);
  assert.equal(state.feedbackEvidenceMatchesRecommendation(event, { ...recommendation, ranked_options: recommendation.ranked_options.slice(0, 2) }), false);
  assert.equal(state.feedbackEvidenceMatchesRecommendation({ ...event, evidence: { ...event.evidence, rules: { ...event.evidence.rules, version: "old" } } }, recommendation), false);
});
