"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const assets = path.join(__dirname, "../assets");
const settle = () => new Promise((resolve) => setImmediate(resolve));

// A small DOM double for public rendered effects, not a copy of app state/logic.
class Element {
  constructor(attributes = {}) {
    this.attributes = attributes;
    this.children = [];
    this.dataset = {};
    this.style = {};
    this.listeners = new Map();
    this.hidden = "hidden" in attributes;
    this.value = attributes.value || "";
    this.text = "";
    for (const [key, value] of Object.entries(attributes)) {
      if (key.startsWith("data-")) this.dataset[key.slice(5).replace(/-([a-z])/g, (_, c) => c.toUpperCase())] = value;
    }
    const classes = new Set((attributes.class || "").split(/\s+/));
    this.classList = {
      add: (name) => classes.add(name), remove: (name) => classes.delete(name),
      contains: (name) => classes.has(name),
      toggle: (name, enabled) => enabled ? classes.add(name) : classes.delete(name),
    };
  }
  set textContent(value) { this.text = String(value); this.children = []; }
  get textContent() { return this.text + this.children.map((node) => node.textContent).join(""); }
  setAttribute(name, value) { this.attributes[name] = String(value); }
  getAttribute(name) { return this.attributes[name] ?? null; }
  removeAttribute(name) { delete this.attributes[name]; delete this[name]; }
  append(...nodes) { nodes.forEach((node) => { node.parent = this; }); this.children.push(...nodes); }
  prepend(node) { node.parent = this; this.children.unshift(node); }
  remove() { if (this.parent) this.parent.children = this.parent.children.filter((node) => node !== this); }
  replaceChildren(...nodes) { this.text = ""; this.children = []; this.append(...nodes); }
  addEventListener(name, listener) { this.listeners.set(name, listener); }
  get lastElementChild() { return this.children.at(-1); }
  get options() { return this.children; }
  get selectedOptions() { return this.children.filter((node) => node.selected); }
  querySelectorAll() { return []; }
}

async function startDashboard(initial, { appSource } = {}) {
  const nodes = new Map();
  const markup = fs.readFileSync(path.join(assets, "index.html"), "utf8");
  for (const tag of markup.matchAll(/<[^>]+\bid="([^"]+)"[^>]*>/g)) {
    const attributes = {};
    for (const match of tag[0].matchAll(/([\w-]+)(?:="([^"]*)")?/g)) attributes[match[1]] = match[2] || "";
    nodes.set(tag[1], new Element(attributes));
  }
  const document = {
    getElementById: (id) => { assert.ok(nodes.has(id), `missing element ${id}`); return nodes.get(id); },
    createElement: () => new Element(),
    querySelectorAll: () => [],
  };
  const requests = [];
  const dashboardReads = [];
  const streams = [];
  const spoken = [];
  const storageWrites = [];
  class EventSource {
    constructor(url) { this.url = url; this.listeners = new Map(); streams.push(this); }
    addEventListener(name, listener) { this.listeners.set(name, listener); }
    emit(name, payload) { this.listeners.get(name)({ data: JSON.stringify(payload) }); }
  }
  const context = vm.createContext({
    document, EventSource, URLSearchParams, AbortController,
    localStorage: { getItem: () => null, setItem: (...args) => storageWrites.push(args) },
    sessionStorage: { getItem: () => null, setItem: (...args) => storageWrites.push(args) },
    location: { search: "" }, addEventListener() {}, setInterval() {}, setTimeout() {},
    speechSynthesis: { cancel() {}, speak: (value) => spoken.push(value.text) },
    SpeechSynthesisUtterance: function (text) { this.text = text; },
    fetch: (url, options) => {
      requests.push({ url, method: options.method || "GET" });
      if (url === "/api/dashboard") {
        // Intentionally ignore abort: app.js must reject retired responses itself.
        return new Promise((resolve, reject) => dashboardReads.push({
          signal: options.signal, reject,
          respond: (payload, ok = true) => resolve({ ok, status: ok ? 200 : 503, json: async () => structuredClone(payload) }),
        }));
      }
      const payload = url === "/api/personalization/options" ? { patch: "16.18.1", items: [] } : {};
      return Promise.resolve({ ok: true, json: async () => payload });
    },
  });
  context.window = context;
  vm.runInContext(fs.readFileSync(path.join(assets, "state.js"), "utf8"), context, { filename: "state.js" });
  vm.runInContext(appSource ?? fs.readFileSync(path.join(assets, "app.js"), "utf8"), context, { filename: "app.js" });
  assert.equal(dashboardReads.length, 1);
  dashboardReads[0].respond(initial);
  await settle();
  assert.equal(streams.length, 1);
  assert.equal(streams[0].url, "/api/events");
  return {
    node: (id) => document.getElementById(id), requests, dashboardReads, spoken, storageWrites,
    emit: (name, value) => streams[0].emit(name, value),
    click: (id) => document.getElementById(id).listeners.get("click")(),
    enableVoice: () => {
      const mode = document.getElementById("alert-mode");
      mode.value = "voice";
      mode.listeners.get("change")();
    },
  };
}

function ready(revision = 1, epoch = "epoch-a") {
  return {
    epoch, revision, updated_at: "2026-09-20T12:00:00Z",
    game: { status: "in_game", session: { id: "match-a" }, game: { active_player: { champion_name: "Garen", current_gold: 1200 } } },
    catalog: { ready: true, source: "remote", patch: "16.18.1" },
    recommendation: {
      status: "ready", patch: "16.18.1", reason: "Comparación validada.",
      next_component: { id: 1057, name: `${epoch} revision ${revision}`, cost: 850 },
      ranked_options: [4401, 3065, 6665].map((id) => ({ item: { id, name: `Item ${id}`, cost: 2800 } })),
    },
  };
}

module.exports = { startDashboard, ready, settle };
