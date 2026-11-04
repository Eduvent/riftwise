"use strict";

const test = require("node:test");
const assert = require("node:assert/strict");
const { startDashboard, ready, settle } = require("./test-support/reconnect-dashboard");

function assertPaused(ui) {
  assert.equal(ui.node("connection-status").textContent, "Reconectando");
  assert.equal(ui.node("advice-freshness").textContent, "Pausado");
  assert.equal(ui.node("dashboard-content").classList.contains("is-suspended"), true);
}

function assertLive(ui, revision, epoch = "epoch-a") {
  assert.equal(ui.node("connection-status").textContent, "En vivo");
  assert.equal(ui.node("advice-freshness").textContent, "En vivo");
  assert.equal(ui.node("dashboard-content").hidden, false);
  assert.equal(ui.node("dashboard-content").classList.contains("is-suspended"), false);
  assert.equal(ui.node("dashboard-revision").textContent, `#${revision}`);
  assert.equal(ui.node("next-name").textContent, `${epoch} revision ${revision}`);
  assert.equal(ui.node("ranked-choice-list").children.length, 3);
}

function reconnect(ui) { ui.emit("error"); assertPaused(ui); ui.emit("open"); assertPaused(ui); }

function notify(ui, id) {
  ui.emit("decision_notification", {
    id, session_id: "match-a", kind: "target_changed", importance: "important",
    current_summary: "Opciones actualizadas.", occurred_at: "2026-09-20T12:00:00Z",
  });
}

function assertNoNotification(ui, id) {
  const writes = ui.storageWrites.length;
  const history = ui.node("decision-history-list").textContent;
  notify(ui, id);
  assert.equal(ui.node("decision-alert-region").children.length, 0);
  assert.equal(ui.node("decision-history-list").textContent, history);
  assert.equal(ui.spoken.length, 0);
  assert.equal(ui.storageWrites.length, writes, "paused hints must not consume dedupe IDs");
}

test("reopen plus SSE while REST is delayed stays paused until an accepted fresh read", async () => {
  const ui = await startDashboard(ready());
  ui.emit("open");
  assertLive(ui, 1);
  ui.enableVoice();
  reconnect(ui);
  assert.equal(ui.dashboardReads.length, 2);
  ui.emit("dashboard_snapshot", ready(2));
  assertPaused(ui);
  assertNoNotification(ui, "pending");
  ui.dashboardReads[1].respond(ready(2));
  await settle();
  assertLive(ui, 2);
  notify(ui, "pending");
  assert.equal(ui.node("decision-alert-region").children.length, 1);
  assert.deepEqual(ui.spoken, ["Tus opciones de compra cambiaron."]);
});

test("failed reconnect REST and subsequent SSE cannot resume advice; a later read can", async () => {
  const ui = await startDashboard(ready());
  ui.emit("open");
  ui.enableVoice();
  reconnect(ui);
  ui.emit("dashboard_snapshot", ready(2));
  ui.dashboardReads[1].respond({ error: { message: "Unavailable" } }, false);
  await settle();
  assertPaused(ui);
  assertNoNotification(ui, "failed");
  ui.emit("dashboard_snapshot", ready(3));
  assertPaused(ui);
  assert.equal(ui.dashboardReads.length, 3, "a new hint retries the failed fresh read");
  assertNoNotification(ui, "retrying");
  ui.dashboardReads[2].respond(ready(3));
  await settle();
  assertLive(ui, 3);
});

test("a superseded reconnect REST read retries without exposing the older revision as current", async () => {
  const ui = await startDashboard(ready());
  ui.emit("open");
  reconnect(ui);
  ui.emit("dashboard_snapshot", ready(4));
  ui.dashboardReads[1].respond(ready(2));
  await settle();
  assertPaused(ui);
  assert.equal(ui.node("next-name").textContent, "epoch-a revision 4");
  assert.equal(ui.dashboardReads.length, 3, "one replacement read prevents a silent resync deadlock");
  ui.emit("dashboard_snapshot", ready(5));
  ui.dashboardReads[2].respond(ready(4));
  await settle();
  assertPaused(ui);
  assert.equal(ui.dashboardReads.length, 3, "superseded reads cannot spin an unbounded retry loop");
  ui.emit("dashboard_snapshot", ready(5));
  assert.equal(ui.dashboardReads.length, 4);
  ui.dashboardReads[3].respond(ready(5));
  await settle();
  assertLive(ui, 5);
  ui.emit("dashboard_snapshot", ready(3));
  assertLive(ui, 5);
});

test("retired connection responses are ignored even when fetch ignores abort, including failures", async () => {
  const ui = await startDashboard(ready());
  ui.emit("open");
  reconnect(ui);
  const retired = ui.dashboardReads[1];
  ui.emit("error");
  assert.equal(retired.signal.aborted, true);
  ui.emit("dashboard_snapshot", ready(99));
  assertPaused(ui);
  assert.equal(ui.node("next-name").textContent, "epoch-a revision 1");
  ui.emit("open");
  const current = ui.dashboardReads[2];
  retired.respond(ready(99, "retired-connection"));
  await settle();
  assertPaused(ui);
  assert.equal(ui.node("next-name").textContent, "epoch-a revision 1");
  current.respond(ready(2));
  await settle();
  assertLive(ui, 2);
  reconnect(ui);
  const failedRetired = ui.dashboardReads[3];
  ui.emit("error");
  ui.emit("open");
  ui.dashboardReads[4].respond(ready(3));
  await settle();
  failedRetired.reject(new Error("Late network failure"));
  await settle();
  assertLive(ui, 3);
  assert.equal(ui.node("system-alert").hidden, true);
});

test("new server epochs accept lower revisions, but retired epochs cannot finish resynchronization", async () => {
  const ui = await startDashboard(ready(10));
  ui.emit("open");
  reconnect(ui);
  ui.emit("dashboard_snapshot", ready(1, "epoch-b"));
  assertPaused(ui);
  ui.dashboardReads[1].respond(ready(50, "epoch-a"));
  await settle();
  assertPaused(ui);
  assert.equal(ui.node("next-name").textContent, "epoch-b revision 1");
  ui.dashboardReads[2].respond(ready(1, "epoch-b"));
  await settle();
  assertLive(ui, 1, "epoch-b");
  ui.emit("dashboard_snapshot", ready(100, "epoch-a"));
  assertLive(ui, 1, "epoch-b");
});

test("startup, live updates, and manual recalculation retain their existing behavior", async () => {
  const ui = await startDashboard(ready());
  assert.equal(ui.node("connection-status").textContent, "Conectando");
  assert.equal(ui.node("advice-freshness").textContent, "Pausado");
  ui.emit("open");
  assert.equal(ui.dashboardReads.length, 1, "initial open does not force a reconnect read");
  assertLive(ui, 1);
  ui.emit("dashboard_snapshot", ready(2));
  assertLive(ui, 2);
  const recalculation = ui.click("recalculate-button");
  assert.equal(ui.node("recalculate-button").disabled, true);
  assert.equal(ui.node("action-feedback").dataset.tone, "pending");
  await settle();
  assert.equal(ui.requests.at(-2).url, "/api/recommendation/recalculate");
  assert.equal(ui.requests.at(-2).method, "POST");
  ui.dashboardReads[1].respond(ready(3));
  await recalculation;
  assertLive(ui, 3);
  assert.equal(ui.node("action-feedback").textContent, "Recomendación actualizada.");
  assert.equal(ui.node("action-feedback").dataset.tone, "success");
  assert.equal(ui.node("recalculate-button").disabled, false);
  assert.equal(ui.node("recalculate-button").getAttribute("aria-busy"), null);
});

test("manual recalculation can satisfy reconnect resync but cannot outlive its connection", async () => {
  const ui = await startDashboard(ready());
  ui.emit("open");
  reconnect(ui);
  const recalculation = ui.click("recalculate-button");
  await settle();
  assert.equal(ui.dashboardReads[1].signal.aborted, true);
  ui.emit("dashboard_snapshot", ready(2));
  assertPaused(ui);
  ui.dashboardReads[1].respond(ready(99));
  ui.dashboardReads[2].respond(ready(2));
  await recalculation;
  await settle();
  assertLive(ui, 2);
  const disconnected = ui.click("recalculate-button");
  await settle();
  ui.emit("error");
  ui.dashboardReads[3].respond(ready(3));
  await disconnected;
  assertPaused(ui);
  assert.equal(ui.node("next-name").textContent, "epoch-a revision 2");
  assert.equal(ui.node("action-feedback").textContent, "Recalculada; esperando que vuelva la conexión en vivo.");
});
