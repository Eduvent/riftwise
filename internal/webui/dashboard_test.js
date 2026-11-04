"use strict";

const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

// Only the DOM operations used by app.js are doubled; the application and its
// state helpers execute unchanged, through their normal fetch and SSE inputs.
class Element {
  constructor(tagName, attributes = {}) {
    this.tagName = tagName;
    this.attributes = { ...attributes };
    this.children = [];
    this.dataset = {};
    this.style = {};
    this.hidden = "hidden" in attributes;
    this.value = attributes.value || "";
    this.className = attributes.class || "";
    this.listeners = new Map();
    for (const [key, value] of Object.entries(attributes)) {
      if (key.startsWith("data-")) {
        this.dataset[key.slice(5).replace(/-([a-z])/g, (_, letter) => letter.toUpperCase())] = value;
      }
    }
    const classes = new Set(this.className.split(/\s+/).filter(Boolean));
    this.classList = {
      add: (name) => classes.add(name),
      remove: (name) => classes.delete(name),
      contains: (name) => classes.has(name),
      toggle: (name, enabled) => enabled ? classes.add(name) : classes.delete(name),
    };
    this.text = "";
  }
  set textContent(value) { this.text = String(value); this.children = []; }
  get textContent() { return this.text + this.children.map((child) => child.textContent).join(""); }
  setAttribute(name, value) { this.attributes[name] = String(value); }
  getAttribute(name) { return this.attributes[name] ?? null; }
  removeAttribute(name) { delete this.attributes[name]; delete this[name]; }
  append(...nodes) { this.children.push(...nodes); }
  replaceChildren(...nodes) { this.text = ""; this.children = [...nodes]; }
  addEventListener(name, listener) { this.listeners.set(name, listener); }
  get options() { return this.children.filter((child) => child.tagName === "option"); }
  get selectedOptions() { return this.options.filter((option) => option.selected); }
  querySelectorAll(selector) {
    const descendants = this.children.flatMap((child) => [child, ...child.querySelectorAll(selector)]);
    const attribute = /^\[([^\]]+)\]$/.exec(selector);
    if (attribute) {
      const property = attribute[1].replace(/^data-/, "").replace(/-([a-z])/g, (_, letter) => letter.toUpperCase());
      return descendants.filter((child) => attribute[1].startsWith("data-") ? property in child.dataset : child.getAttribute(attribute[1]) !== null);
    }
    return descendants.filter((child) => child.tagName === selector);
  }
}

function createDocument() {
  const root = new Element("document");
  const stack = [root];
  const ids = new Map();
  const markup = fs.readFileSync(path.join(__dirname, "assets/index.html"), "utf8");
  const voidTags = new Set(["meta", "link", "input", "img", "br"]);
  for (const token of markup.matchAll(/<[^>]+>|[^<]+/g)) {
    const value = token[0];
    if (value.startsWith("<!")) continue;
    if (value.startsWith("</")) { stack.pop(); continue; }
    if (!value.startsWith("<")) { stack.at(-1).text += value; continue; }
    const tagName = /^<([\w-]+)/.exec(value)[1];
    const attributes = {};
    for (const match of value.slice(tagName.length + 1, -1).matchAll(/([\w-]+)(?:="([^"]*)")?/g)) {
      attributes[match[1]] = match[2] || "";
    }
    const element = new Element(tagName, attributes);
    stack.at(-1).append(element);
    if (attributes.id) ids.set(attributes.id, element);
    if (!voidTags.has(tagName)) stack.push(element);
  }
  return {
    getElementById: (id) => ids.get(id) || null,
    createElement: (tagName) => new Element(tagName),
    querySelectorAll: (selector) => root.querySelectorAll(selector),
  };
}

async function startDashboard(snapshot) {
  const document = createDocument();
  const streams = [];
  const requests = [];
  class EventSource {
    constructor(url) { this.url = url; this.listeners = new Map(); streams.push(this); }
    addEventListener(name, listener) { this.listeners.set(name, listener); }
    emit(name, payload) {
      assert.ok(this.listeners.has(name), `missing SSE listener: ${name}`);
      this.listeners.get(name)({ data: JSON.stringify(payload) });
    }
  }
  const storage = () => ({ getItem: () => null, setItem() {} });
  const context = vm.createContext({
    document, EventSource, URLSearchParams, AbortController,
    localStorage: storage(), sessionStorage: storage(),
    location: { search: "" }, addEventListener() {}, setInterval() {}, setTimeout() {},
    fetch: async (url) => {
      requests.push(url);
      let payload;
      switch (url) {
        case "/api/dashboard": payload = snapshot; break;
        case "/api/personalization/preferences": payload = {}; break;
        case "/api/personalization/options": payload = { patch: "16.18.1", items: [] }; break;
        default: throw new Error(`Unexpected fetch: ${url}`);
      }
      return { ok: true, json: async () => structuredClone(payload) };
    },
  });
  context.window = context;
  for (const asset of ["state.js", "app.js"]) {
    vm.runInContext(fs.readFileSync(path.join(__dirname, "assets", asset), "utf8"), context, { filename: asset });
  }
  // Let startup's async dashboard read and independent preference reads settle.
  await new Promise((resolve) => setImmediate(resolve));
  assert.ok(requests.includes("/api/dashboard"));
  assert.equal(streams.length, 1, "startup must reach the normal SSE connection");
  assert.equal(streams[0].url, "/api/events");
  streams[0].emit("open");
  return { node: (id) => document.getElementById(id), send: (value) => streams[0].emit("dashboard_snapshot", value) };
}

function snapshot(status, revision = 1, overrides = {}) {
  return {
    epoch: "test-epoch", revision, updated_at: "2026-09-20T12:00:00Z",
    game: { status: "in_game", game: { active_player: { champion_name: "Garen", current_gold: 1200, items: [] } } },
    catalog: { ready: true, source: "remote", patch: "16.18.1" },
    recommendation: { status, ...overrides },
  };
}

function ready(revision = 1) {
  return snapshot("ready", revision, {
    patch: "16.18.1", reason: "Comparación validada.",
    next_component: { id: 1057, name: "Capa negatrón", cost: 850 },
    ranked_options: [
      { item: { id: 4401, name: "Fuerza de la naturaleza", cost: 2800 } },
      { item: { id: 3065, name: "Rostro espiritual", cost: 2900 } },
      { item: { id: 6665, name: "Jak'Sho, el Proteico", cost: 3200 } },
    ],
  });
}

function assertReady(ui) {
  assert.equal(ui.node("dashboard-content").hidden, false);
  assert.equal(ui.node("state-panel").hidden, true);
  assert.equal(ui.node("decision-status").textContent, "OPCIÓN DISPONIBLE");
  assert.equal(ui.node("purchase-command").textContent, "Componente sugerido");
  assert.equal(ui.node("next-name").textContent, "Capa negatrón");
  assert.equal(ui.node("gold-message").textContent, "Disponible en la tienda");
  assert.equal(ui.node("ranked-choice-list").children.length, 3);
  assert.equal(ui.node("dashboard-content").classList.contains("is-suspended"), false);
  assert.equal(ui.node("system-alert").hidden, true);
}

function assertCleared(ui) {
  assert.equal(ui.node("dashboard-content").hidden, true);
  assert.equal(ui.node("next-name").textContent, "—");
  assert.equal(ui.node("recommendation-reason").textContent, "—");
  assert.equal(ui.node("next-icon").hidden, true);
  assert.equal(ui.node("next-icon").getAttribute("src"), null);
  assert.equal(ui.node("next-icon").src, undefined);
  assert.equal(ui.node("ranked-choice-list").children.length, 0);
  assert.equal(ui.node("ranked-choice-count").textContent, "—");
  assert.equal(ui.node("inventory-list").children.length, 0);
}

function assertInsufficientContext(ui, detail) {
  assert.equal(ui.node("state-panel").hidden, false);
  assert.equal(ui.node("state-panel").dataset.state, "insufficient_context");
  assert.equal(ui.node("state-panel").getAttribute("aria-live"), "polite");
  assert.equal(ui.node("state-kicker").textContent, "CONTEXTO INSUFICIENTE");
  assert.equal(ui.node("state-title").textContent, "Aún no hay suficientes opciones para comparar");
  assert.equal(ui.node("state-detail").textContent, detail);
  assert.equal(ui.node("state-secondary").textContent, "No se sugiere una compra hasta contar con una opción principal y dos alternativas válidas.");
  assert.equal(ui.node("system-alert").hidden, true);
  assert.equal(ui.node("system-alert-text").textContent, "");
  assertCleared(ui);
}

const insufficientDetail = "El contexto actual no permite una comparación de objetos válida.";

test("initial dashboard fetch presents insufficient context without a calculation error", async () => {
  const ui = await startDashboard(snapshot("insufficient_context"));
  assertInsufficientContext(ui, insufficientDetail);
});

test("SSE insufficient context clears ready advice, retains the reason and warnings, then recovers", async () => {
  const ui = await startDashboard(ready());
  assertReady(ui);
  const reason = "Las preferencias dejan menos de tres opciones válidas.";
  // Even if a snapshot retains item fields, a non-actionable status must win.
  const next = ready(2);
  Object.assign(next.recommendation, { status: "insufficient_context", reason });
  ui.send(next);
  assertInsufficientContext(ui, reason);
  ui.send(snapshot("insufficient_context", 3, { reason, warnings: ["Perfil neutral conservador."] }));
  assertCleared(ui);
  assert.equal(ui.node("state-panel").dataset.state, "insufficient_context");
  assert.equal(ui.node("system-alert").hidden, false);
  assert.equal(ui.node("system-alert").dataset.tone, "warning");
  assert.equal(ui.node("system-alert-text").textContent, "Detalles de la recomendación:");
  assert.equal(ui.node("system-alert-details").children[0].textContent, "Perfil neutral conservador.");
  ui.send(ready(4));
  assertReady(ui);
});

for (const [status, viewState, title, alert] of [
  ["waiting", "waiting_for_game", "Esperando una partida", null],
  ["stale", "stale", "Recomendación pausada", "Los datos dejaron de estar vigentes; la compra anterior permanece oculta."],
  ["error", "error", "Recomendación no disponible", "El motor seguirá escuchando actualizaciones para recuperarse automáticamente."],
]) {
  test(`existing ${status} presentation still clears ready advice`, async () => {
    const ui = await startDashboard(ready());
    assertReady(ui);
    ui.send(snapshot(status, 2));
    assertCleared(ui);
    assert.equal(ui.node("state-panel").hidden, false);
    assert.equal(ui.node("state-panel").dataset.state, viewState);
    assert.equal(ui.node("state-title").textContent, title);
    assert.equal(ui.node("system-alert").hidden, alert === null);
    if (alert) {
      assert.equal(ui.node("system-alert").dataset.tone, "error");
      assert.equal(ui.node("system-alert-text").textContent, alert);
    }
  });
}
