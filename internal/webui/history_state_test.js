"use strict";

const test = require("node:test");
const assert = require("node:assert/strict");
const state = require("./assets/history-state.js");

test("history page normalization keeps only identity-free display fields", () => {
  const page = state.normalizePage({ matches: [{
    match_id: "m1", digest: "digest", champion_name: "Garen", role: "top", role_known: true,
    completion: "complete_terminal", duration: { seconds: 125, lower_bound: false },
    stats: { kills: 4, deaths: 2, assists: 3, creep_score: 120, level: 11 },
    player_riot_id: "must-not-be-copied",
  }], next_cursor: "next" });
  assert.equal(page.matches[0].champion, "Garen");
  assert.equal(page.matches[0].role, "Superior");
  assert.equal("playerRiotID" in page.matches[0], false);
  assert.equal(JSON.stringify(page.matches[0]).includes("must-not-be-copied"), false);
  assert.equal(page.nextCursor, "next");
});

test("keyset pages merge without duplicate matches", () => {
  const first = state.normalizePage({ matches: [{ match_id: "m2" }, { match_id: "m1" }] });
  const second = state.normalizePage({ matches: [{ match_id: "m1" }, { match_id: "m0" }] });
  assert.deepEqual(state.mergePage(first.matches, second.matches).map((match) => match.matchID), ["m2", "m1", "m0"]);
});

test("duration and confidence labels include text meaning", () => {
  assert.equal(state.formatDuration(125, false), "2:05");
  assert.equal(state.formatDuration(125, true), "al menos 2:05");
  assert.equal(state.completionLabel("complete_terminal"), "Final observado");
  assert.match(state.confidenceLabel(0.55), /^Baja/);
});

test("Riot status normalization drops credentials and account identity", () => {
  const status = state.normalizeRiotStatus({
    mode: "direct", enabled: true, configured: true, region: "americas",
    api_key: "must-not-copy", accounts: [{ id: "local-1", platform: "NA1", region: "americas", linked_at: "2026-09-19T00:00:00Z", puuid: "must-not-copy", game_name: "Secret Name" }],
    syncs: [{ account_id: "local-1", status: "partial", last_error_kind: "server_error", updated_at: "2026-09-19T01:00:00Z", response_body: "must-not-copy" }],
    reconciliations: [{ account_id: "local-1", status: "pending", last_error_kind: "storage", updated_at: "2026-09-19T01:01:00Z", raw_match_id: "must-not-copy" }],
  });
  assert.equal(status.accounts[0].id, "local-1");
  assert.equal(JSON.stringify(status).includes("must-not-copy"), false);
  assert.equal(JSON.stringify(status).includes("Secret Name"), false);
  assert.equal(status.syncs[0].errorKind, "server_error");
  assert.equal(status.reconciliations[0].status, "pending");
  assert.equal(status.reconciliations[0].errorKind, "storage");
});

test("statistics normalization keeps observational aggregate fields", () => {
  const report = state.normalizeStatistics({ total_games: 2, minimum_sample: 10, observational_notice: "Observed only", groups: [{
    key: { patch: "16.18", champion_name: "Garen", role: "top", role_provenance: "team_position", rank_bucket: "unknown", rank_provenance: "unavailable", opponent_champion: "Darius", opponent_provenance: "unique_same_role", final_items: [3078, 3006] },
    games: 2, wins: 1, win_rate: 0.5, minimum_sample_warning: true, opponent_puuid: "forbidden",
  }] });
  assert.equal(report.groups[0].role, "Superior");
  assert.deepEqual(report.groups[0].finalItems, [3006, 3078]);
  assert.equal(JSON.stringify(report).includes("forbidden"), false);
});
