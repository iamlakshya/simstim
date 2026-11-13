// A copy of the launcher that runs in the page, with the sample data of the
// `demo` command (internal/tui/demo.go). It follows the real screens
// (docs/contributing/ux-spec.md and the images in docs/images): the header
// with the logo and the name in the brand gradient, the rule, the rows with
// their state glyphs and braille traces, the line under the list, the prompt
// line and the keys. Nothing connects anywhere.
//
// Keys work while it has focus: up and down (or j and k), enter, 1 to 9, esc,
// / to search, ? for the keys, y to copy an attach command. Tab leaves it.
//
// It moves only while the reader is using it (it has focus, or the pointer
// is over it), and for the second its boot and each change of view take.
// The rest of the time it is a still screen, so the hero's story is the one
// thing on the page that moves by itself.

import { braille, motionOn, onMotionChange, everyFrame } from "./signal.js";

const NOISE = "⡏⢸⣿⠿⡿⢿⣻⣽⣾⣷⣯⣟⡷⢾⣼⣧";
const SPIN = "⠁⠂⠄⡀⢀⠠⠐⠈";
const ease = (x) => 1 - Math.pow(1 - Math.min(Math.max(x, 0), 1), 3);
const esc = (s) => s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");

// The working glyph: a tiny Game of Life on a braille cell's eight dots.
const LIFE = (() => {
  const B = [[0, 3], [1, 4], [2, 5], [6, 7]];
  const alive = (s, x, y) => (s & (1 << B[(y + 4) % 4][(x + 2) % 2])) !== 0;
  const step = (s) => {
    let n = 0;
    for (let y = 0; y < 4; y++) {
      for (let x = 0; x < 2; x++) {
        let c = 0;
        for (let dy = -1; dy <= 1; dy++) for (let dx = -1; dx <= 1; dx++) if ((dx || dy) && alive(s, x + dx, y + dy)) c++;
        if (c === 3 || (c === 2 && alive(s, x, y))) n |= 1 << B[y][x];
      }
    }
    return n;
  };
  let seed = 0x9e3779b9;
  const rnd = () => {
    seed ^= seed << 13;
    seed >>>= 0;
    seed ^= seed >>> 17;
    seed ^= seed << 5;
    seed >>>= 0;
    return seed & 0xff;
  };
  const out = [];
  let s = rnd() | 0x11;
  while (out.length < 48) {
    out.push(braille.glyph(s));
    let n = step(s);
    if (n === 0 || n === s) n = rnd() | 0x24;
    s = n;
  }
  return out;
})();

function hash(s) {
  let h = 2166136261;
  for (let i = 0; i < s.length; i++) h = Math.imul(h ^ s.charCodeAt(i), 16777619) >>> 0;
  return h;
}

// The sample hosts and sessions. ago is in seconds before the page opened.
function sample() {
  const s = (id, name, agent, cwd, state, ago, mem, max, extra = {}) => ({ id, name, agent, cwd, state, ago, mem, max, ...extra });
  return [
    {
      name: "gcp-dev", online: true, rtt: "38ms", os: "Debian GNU/Linux 13 (trixie)", cpus: 4,
      sessions: [s("t3f6a2zk", "data-pipeline", "gemini", "~/pipeline", "working", 3, "290 MiB", "6 GiB")],
    },
    { name: "hetzner-box", online: false, sessions: [] },
    {
      name: "homelab", online: true, rtt: "4ms", os: "Debian GNU/Linux 13 (trixie)", cpus: 4,
      sessions: [
        s("k2v7m4qa", "auth-refactor", "claude", "~/src/app", "needs", 40, "612 MiB", "8 GiB", { ask: "Do you want to proceed?" }),
        s("p2x3hdte", "api-tests", "codex", "~/src/api", "working", 5, "140 MiB", "8 GiB"),
        s("w5c6n2rb", "docs-site", "opencode", "~/src/docs", "idle", 20 * 60, "380 MiB", "8 GiB"),
        s("j6r4y7uf", "notes", "shell", "~", "exited", 2 * 3600, "", ""),
      ],
    },
  ];
}

// The made-up agent screens, as the demo draws them.
function agentScreen(s) {
  if (s.state === "needs") {
    return [
      [["> ", "b"], ["Move the session store to the new token format, and keep old tokens working.", ""]],
      [],
      [["● ", "m"], ["I'll read the current store first.", ""]],
      [["  Read internal/auth/store.go (212 lines)", "f"]],
      [["  Read internal/auth/token.go (96 lines)", "f"]],
      [],
      [["● ", "m"], ["Plan: add a v2 encoder, read both formats, migrate on write.", ""]],
      [],
      [["● ", "m"], ["Edit internal/auth/token.go", ""]],
      [["  + func encodeV2(t Token) ([]byte, error) {", "o"]],
      [["  +     return json.Marshal(tokenV2{ID: t.ID, Exp: t.Exp})", "o"]],
      [["  - return legacyEncode(t)", "d"]],
      [],
      [["  Run the migration tests?", "b"]],
      [["  go test ./internal/auth/... -run Migrate", ""]],
      [],
      [["  ❯ 1. Yes", "a"]],
      [["    2. No, and tell the agent what to do differently", ""]],
    ];
  }
  if (s.state === "working") {
    return [
      [["> ", "b"], ["Make the API tests pass on the new schema.", ""]],
      [],
      [["  ran go test ./... (3 failed)", "f"]],
      [["  fixing handlers/users.go: the nullable email column", ""]],
      [],
      [["∴ Working (14s · esc to interrupt)", "a"]],
    ];
  }
  return [[["  (idle)", "f"]]];
}

function fmtAgo(sec) {
  if (sec < 60) return Math.floor(sec) + "s";
  if (sec < 3600) return Math.floor(sec / 60) + "m";
  return Math.floor(sec / 3600) + "h";
}

const STATE = {
  needs: { word: "needs input", cls: "w" },
  working: { word: "working", cls: "a" },
  idle: { word: "idle", cls: "f" },
  exited: { word: "exited 0", cls: "f" },
};
const ORDER = { needs: 0, working: 1, idle: 2, exited: 3 };

// A line is a list of [text, class] segments.
function width(line) {
  let n = 0;
  for (const [t] of line) n += t.length;
  return n;
}
function fit(text, w) {
  if (w <= 0) return "";
  if (text.length > w) return text.slice(0, Math.max(w - 1, 0)) + "…";
  return text + " ".repeat(w - text.length);
}
function right(text, w) {
  return text.length >= w ? text.slice(0, w) : " ".repeat(w - text.length) + text;
}

export class Launcher {
  constructor(screen, opts) {
    this.screen = screen;
    this.name = opts.name;
    this.onSay = opts.say;
    this.hosts = sample();
    for (const h of this.hosts) h.sessions.sort((a, b) => ORDER[a.state] - ORDER[b.state]);
    this.t0 = performance.now() / 1000;
    this.view = "sessions"; // sessions, hosts, search, attached
    this.host = 2; // homelab
    this.sel = { sessions: 0, hosts: 2, search: 0 };
    this.query = "";
    this.typed = "";
    this.sheet = null;
    this.notice = null;
    this.bootAt = -1;
    this.slideAt = -1;
    this.slideDir = 1;
    this.revealAt = -1;
    this.activityAt = -1;
    this.jack = null;
    this.engaged = false;
    this.events = new Map();
    this.seedTraces();
    this.build();
    this.measure();
    const r = screen.getBoundingClientRect();
    if (r.top < innerHeight && r.bottom > 0) this.bootAt = this.now();
    this.render(this.now());
    this.loop = everyFrame(20, () => this.render(this.now()), () => (motionOn() && this.engaged) || this.busy(), () => this.render(this.now()), screen);
    const engage = (on) => {
      this.engaged = on || screen.matches(":focus-within, :hover");
      this.loop.update();
    };
    screen.addEventListener("focusin", () => engage(true));
    screen.addEventListener("focusout", () => engage(false));
    screen.addEventListener("pointerenter", () => engage(true));
    screen.addEventListener("pointerleave", () => engage(false));
    onMotionChange(() => this.render(this.now()));
    new ResizeObserver(() => {
      if (this.measure()) this.render(this.now());
    }).observe(screen);
    // Measure again once the monospace face has loaded: the cells change size.
    if (document.fonts) {
      const again = () => this.measure(true) && this.render(this.now());
      document.fonts.load(`${this.size}px "Site Mono"`).then(again, () => {});
      document.fonts.ready.then(again);
    }
  }

  now() {
    return performance.now() / 1000;
  }

  // Busy: an attach is playing, or the boot or a change of view is still
  // easing in. These run to their end even when the reader looks away.
  busy() {
    if (this.jack) return true;
    if (!motionOn()) return false;
    const t = this.now();
    return [this.bootAt, this.slideAt, this.revealAt, this.activityAt].some((at) => at >= 0 && t - at < 1.2);
  }

  // Output times for each session: steady bursts for the working ones, and,
  // for the one that asked a question, the work before it went quiet.
  seedTraces() {
    const t = this.t0;
    for (const h of this.hosts) {
      for (const s of h.sessions) {
        const ev = [];
        if (s.state === "working") for (let x = -34; x <= 0; x += 1.1 + (hash(s.id + x) % 7) / 10) ev.push(t + x);
        if (s.state === "needs") for (let x = -36; x <= -15; x += 1.4) ev.push(t + x);
        this.events.set(s.id, ev);
      }
    }
  }

  // New output from working sessions, while motion is on.
  feed(t) {
    for (const h of this.hosts) {
      for (const s of h.sessions) {
        if (s.state !== "working") continue;
        const ev = this.events.get(s.id);
        const last = ev[ev.length - 1] || 0;
        if (t - last > 0.9 + (hash(s.id + Math.floor(last)) % 9) / 10) ev.push(t);
        while (ev.length && t - ev[0] > 40) ev.shift();
      }
    }
  }

  trace(events, t) {
    let out = "";
    for (let c = 0; c < 8; c++) {
      let bits = 0;
      for (let dx = 0; dx < 2; dx++) {
        const col = c * 2 + dx;
        const at = t - (32 * (15 - col)) / 15;
        let v = 0;
        for (const e of events) {
          const d = (at - e) / 1.7;
          v = Math.max(v, Math.exp(-d * d));
        }
        const hgt = 1 + Math.round(v * 3);
        for (let dy = 0; dy < hgt; dy++) bits |= braille.bit(3 - dy, dx);
      }
      out += braille.glyph(bits);
    }
    return out;
  }

  build() {
    this.screen.textContent = "";
    this.screen.tabIndex = 0;
    this.screen.setAttribute("role", "application");
    this.screen.setAttribute("aria-roledescription", "demo terminal");
    this.screen.setAttribute("aria-label", `The ${this.name} launcher, with sample data. Up and down move, enter opens, slash searches, escape goes back, question mark shows the keys.`);
    this.tty = document.createElement("div");
    this.tty.className = "tty";
    this.tty.setAttribute("aria-hidden", "true");
    this.selBar = document.createElement("div");
    this.selBar.className = "tty-selbar";
    this.screen.append(this.tty);
    this.live = document.createElement("div");
    this.live.className = "sr-only";
    this.live.setAttribute("aria-live", "polite");
    this.screen.append(this.live);
    this.rowEls = [];
    this.screen.addEventListener("keydown", (e) => this.key(e));
    this.screen.addEventListener("click", (e) => this.click(e));
  }

  boot() {
    this.bootAt = this.now();
    this.loop.update();
  }

  measure(force) {
    const w = this.screen.clientWidth;
    if (!w) return false;
    const size = Math.max(9.5, Math.min(14.5, (w - 8) / 0.6 / 58));
    this.tty.style.setProperty("--tty-size", size.toFixed(2) + "px");
    // The frame sizes itself, and places its notes, from the cell size.
    this.screen.parentElement.style.setProperty("--tty-size-px", size.toFixed(2) + "px");
    const probe = document.createElement("span");
    probe.textContent = "0".repeat(20);
    this.tty.append(probe);
    const cw = probe.getBoundingClientRect().width / 20 || size * 0.6;
    probe.remove();
    const h = this.screen.clientHeight;
    const pad = parseFloat(getComputedStyle(this.tty).paddingTop) + parseFloat(getComputedStyle(this.tty).paddingBottom);
    const cols = Math.max(40, Math.floor((w - 8) / cw));
    const rows = Math.max(12, Math.floor((h - pad) / (size * 1.25)));
    if (!force && cols === this.cols && rows === this.rows) return false;
    this.cols = cols;
    this.rows = rows;
    this.cellW = cw;
    this.size = size;
    this.tty.textContent = "";
    this.rowEls = [];
    for (let i = 0; i < rows; i++) {
      const r = document.createElement("div");
      r.className = "tty-row";
      this.tty.append(r);
      this.rowEls.push({ el: r, html: null });
    }
    return true;
  }

  say(text) {
    this.live.textContent = text;
  }

  // ---------- Model ----------

  curHost() {
    return this.hosts[this.host];
  }

  list() {
    if (this.view === "hosts") return this.hosts;
    if (this.view === "search") return this.results();
    return this.curHost().sessions;
  }

  results() {
    const q = this.query.toLowerCase();
    const out = [];
    for (const h of this.hosts) {
      for (const s of h.sessions) {
        const hay = (s.name + " " + s.agent + " " + h.name + ":" + s.cwd).toLowerCase();
        if (!q || this.fuzzy(q, hay)) out.push({ host: h, s });
      }
    }
    out.sort((a, b) => ORDER[a.s.state] - ORDER[b.s.state]);
    return out;
  }

  fuzzy(q, text) {
    let i = 0;
    for (const ch of text) if (ch === q[i]) i++;
    return i === q.length;
  }

  // The letters of name that match q, for underlining.
  hits(q, name) {
    const out = new Set();
    let i = 0;
    for (let k = 0; k < name.length && i < q.length; k++) {
      if (name[k].toLowerCase() === q[i]) {
        out.add(k);
        i++;
      }
    }
    return i === q.length ? out : new Set();
  }

  selected() {
    const l = this.list();
    const i = Math.min(this.sel[this.view] || 0, l.length - 1);
    return i >= 0 ? l[i] : null;
  }

  move(d) {
    const l = this.list();
    if (!l.length) return;
    const v = this.view;
    this.sel[v] = (Math.min(this.sel[v] || 0, l.length - 1) + d + l.length) % l.length;
    this.glideAt = this.now();
    this.announce();
  }

  announce() {
    const x = this.selected();
    if (!x) return this.say("Nothing here.");
    if (this.view === "hosts") {
      return this.say(x.online ? `${x.name}, online, ${x.sessions.length} sessions` : `${x.name}, offline`);
    }
    const s = this.view === "search" ? x.s : x;
    const where = this.view === "search" ? ` on ${x.host.name}` : "";
    this.say(`${s.name}${where}, ${s.agent}, ${STATE[s.state].word}`);
  }

  open() {
    const t = this.now();
    if (this.view === "hosts") {
      const h = this.selected();
      if (!h.online) {
        this.notice = { at: t, line: [["✕ ", "d"], [`cannot reach ${h.name}: connection timed out`, "d"], ["   → ", "a"], [`ssh ${h.name}`, "f"]] };
        this.say(`Cannot reach ${h.name}: connection timed out. Try: ssh ${h.name}`);
        return;
      }
      this.host = this.sel.hosts;
      this.view = "sessions";
      this.sel.sessions = 0;
      this.slideAt = t;
      this.slideDir = 1;
      this.revealAt = t;
      this.say(`${h.name}: ${h.sessions.length} sessions. Enter attaches.`);
      return;
    }
    const x = this.selected();
    if (!x) return;
    const s = this.view === "search" ? x.s : x;
    if (this.view === "search") this.host = this.hosts.indexOf(x.host);
    this.attach(s);
  }

  attach(s) {
    const t = this.now();
    const row = this.listTop + Math.min(this.sel[this.view] || 0, 99);
    if (motionOn()) {
      this.jack = { at: t, row, s };
      this.loop.update();
    } else {
      this.enter(s);
    }
  }

  enter(s) {
    this.jack = null;
    this.from = this.view;
    this.view = "attached";
    this.agent = s;
    this.typed = "";
    this.say(`Attached to ${s.name}. The agent owns the screen now. Press escape, or control and backslash, to detach.`);
  }

  back() {
    const t = this.now();
    if (this.view === "attached") {
      this.view = this.from === "search" ? "sessions" : this.from || "sessions";
      this.bootAt = t;
      this.say(`Detached. ${this.agent.name} keeps running.`);
      return;
    }
    if (this.view === "search") {
      this.view = this.before || "sessions";
      this.query = "";
      this.say("Search closed.");
      return;
    }
    if (this.view === "sessions") {
      this.view = "hosts";
      this.sel.hosts = this.host;
      this.slideAt = t;
      this.slideDir = -1;
      this.say("Hosts. Enter opens a host.");
    }
  }

  // ---------- Input ----------

  key(e) {
    if (e.metaKey || e.altKey) return;
    const k = e.key;
    let used = true;
    if (this.sheet) {
      if (k === "Escape" || k === "?" || (k === "o" && e.ctrlKey)) this.sheet = null;
      else if (k === "ArrowDown" || k === "j") this.sheet.sel = (this.sheet.sel + 1) % this.sheet.items.length;
      else if (k === "ArrowUp" || k === "k") this.sheet.sel = (this.sheet.sel + this.sheet.items.length - 1) % this.sheet.items.length;
      else if (k === "Enter") this.runSheet();
      else used = false;
    } else if (this.view === "attached") {
      if (k === "Escape" || (k === "\\" && e.ctrlKey)) this.back();
      else if (k === "Backspace") this.typed = this.typed.slice(0, -1);
      else if (k === "Enter") this.typed = "";
      else if (k.length === 1 && !e.ctrlKey) this.typed = (this.typed + k).slice(-60);
      else used = false;
    } else if (this.view === "search") {
      if (k === "Escape") this.back();
      else if (k === "Enter") this.open();
      else if (k === "ArrowDown") this.move(1);
      else if (k === "ArrowUp") this.move(-1);
      else if (k === "Backspace") {
        this.query = this.query.slice(0, -1);
        this.sel.search = 0;
      } else if (k.length === 1 && !e.ctrlKey && k !== " ") {
        this.query = (this.query + k).slice(0, 24);
        this.sel.search = 0;
        const n = this.results().length;
        this.say(n ? `${n} match${n === 1 ? "" : "es"}` : "No session matches.");
      } else used = false;
    } else {
      if (k === "ArrowDown" || k === "j") this.move(1);
      else if (k === "ArrowUp" || k === "k") this.move(-1);
      else if (k === "Enter" || k === "l" || k === "ArrowRight") this.open();
      else if (k === "Escape" || k === "h" || k === "ArrowLeft") this.back();
      else if (k >= "1" && k <= "9") {
        const i = Number(k) - 1;
        if (i < this.list().length) {
          this.sel[this.view] = i;
          this.open();
        }
      } else if (k === "/") {
        this.before = this.view;
        this.view = "search";
        this.query = "";
        this.sel.search = 0;
        this.say("Search every host. Type to filter, enter attaches.");
      } else if (k === "?" || (k === "o" && e.ctrlKey)) this.openSheet(k === "?" ? "Keys" : "Commands");
      else if (k === "y" && this.view === "sessions") this.copyAttach();
      else if (k === "n" || k === "r" || k === "d" || k === " " || k === "a" || k === "i") {
        this.notice = { at: this.now(), line: [["• ", "a"], ["This copy shows the main keys. Run ", "f"], [`${this.name} demo`, "t"], [" for all of them.", "f"]] };
        this.say(`This copy shows the main keys. Run ${this.name} demo for all of them.`);
      } else used = false;
    }
    if (used) {
      e.preventDefault();
      if (e.stopPropagation) e.stopPropagation(); // the docs' own "/" must not open search here
      this.render(this.now());
      this.loop.update();
    }
  }

  click(e) {
    const row = e.target.closest("[data-row]");
    this.screen.focus({ preventScroll: true });
    if (!row || this.sheet || this.view === "attached") return;
    const i = Number(row.dataset.row);
    if (i === (this.sel[this.view] || 0)) this.open();
    else {
      this.sel[this.view] = i;
      this.glideAt = this.now();
      this.announce();
    }
    this.render(this.now());
  }

  copyAttach() {
    const s = this.selected();
    if (!s) return;
    const cmd = `${this.name} attach ${this.curHost().name}/${s.name}`;
    if (navigator.clipboard) navigator.clipboard.writeText(cmd).catch(() => {});
    this.notice = { at: this.now(), line: [["✓ ", "o"], [`Copied: ${cmd}.`, ""]] };
    this.say(`Copied: ${cmd}.`);
  }

  openSheet(title) {
    const items = [
      ["Attach session", "enter"],
      ["Search every host", "/"],
      ["Back to the hosts", "esc"],
      ["Copy the attach command", "y"],
      ["Move", "↑↓"],
      ["Open a row", "1–9"],
      ["Keys", "?"],
    ];
    this.sheet = { title, items, sel: 0, at: this.now() };
    this.say(`${title}. Up and down move, enter runs, escape closes.`);
  }

  runSheet() {
    const name = this.sheet.items[this.sheet.sel][0];
    this.sheet = null;
    if (name === "Attach session") this.open();
    else if (name === "Search every host") this.key({ key: "/", preventDefault() {}, ctrlKey: false });
    else if (name === "Back to the hosts") this.back();
    else if (name === "Copy the attach command") this.copyAttach();
  }

  // ---------- Drawing ----------

  decode(text, start, t) {
    if (!motionOn() || start < 0) return text;
    const s = t - start;
    if (s >= 0.42) return text;
    const frame = Math.floor(t / 0.033);
    let out = "";
    for (let i = 0; i < text.length; i++) {
      const settle = (0.42 * (i + 1)) / (text.length + 1);
      if (text[i] === " " || s >= settle) out += text[i];
      else if (s < settle - 0.21) out += " ";
      else out += NOISE[hash(i + ":" + frame) % NOISE.length];
    }
    return out;
  }

  gradientLine(text) {
    const css = getComputedStyle(this.screen);
    const a = css.getPropertyValue("--brand-a").trim() || "#0e7c7b";
    const b = css.getPropertyValue("--brand-b").trim() || "#00e5ff";
    const pa = [1, 3, 5].map((i) => parseInt(a.slice(i, i + 2), 16));
    const pb = [1, 3, 5].map((i) => parseInt(b.slice(i, i + 2), 16));
    const n = text.length;
    return [...text].map((ch, i) => {
      const k = n > 1 ? i / (n - 1) : 0;
      const c = pa.map((v, j) => Math.round(v + (pb[j] - v) * k));
      return [ch, "b", `color:rgb(${c.join(",")})`];
    });
  }

  logo(t) {
    let s = "";
    const tt = motionOn() ? t - this.t0 : 0;
    for (let c = 0; c < 3; c++) {
      let bits = 0;
      for (let dx = 0; dx < 2; dx++) {
        const x = c * 2 + dx;
        const y = Math.round(1.5 + 1.5 * Math.sin(x * 0.9 - tt * 4));
        bits |= braille.bit(Math.min(Math.max(y, 0), 3), dx);
      }
      s += braille.glyph(bits);
    }
    return s;
  }

  hints(hs, w) {
    const out = [];
    let n = 0;
    for (const [k, d] of hs) {
      const add = (out.length ? 3 : 0) + k.length + 1 + d.length;
      if (n + add > w) break;
      if (out.length) out.push(["   ", ""]);
      out.push([k, "a"], [" " + d, "f"]);
      n += add;
    }
    return out;
  }

  glyph(s, t) {
    const on = motionOn();
    if (s.state === "working") {
      if (!on) return ["⠶", "a"];
      const i = Math.floor((t - this.t0) / 0.14) + (hash(s.id) % LIFE.length);
      return [LIFE[i % LIFE.length], "a"];
    }
    if (s.state === "needs") {
      const k = on ? 0.55 + 0.45 * Math.cos(((t - this.t0) / 1.6) * Math.PI * 2) : 1;
      return ["●", "w", `opacity:${k.toFixed(2)}`];
    }
    if (s.state === "exited") return ["✓", "o"];
    return ["○", "f"];
  }

  sessionRow(s, i, cw, t, sel, hostName, q) {
    const showFolder = cw >= 70;
    const showAgent = cw >= 56;
    const fixed = 6 + (showAgent ? 10 : 0) + 12 + 6 + 2 + 8;
    let nameW = Math.min(26, cw - fixed - (showFolder ? 14 : 2));
    const folderW = showFolder ? cw - fixed - nameW - 4 : 0;
    nameW = Math.max(nameW, 6);
    const st = STATE[s.state];
    const ago = s.ago + (t - this.t0);
    const g = this.glyph(s, t);
    const line = [];
    line.push(sel ? ["▍", "a"] : [" ", ""]);
    line.push([" ", ""], [i < 9 ? String(i + 1) : " ", sel ? "" : "f"], [" ", ""]);
    line.push(g, [" ", ""]);
    const name = fit(s.name, nameW);
    const hit = q ? this.hits(q, s.name) : new Set();
    if (hit.size) {
      for (let k = 0; k < name.length; k++) line.push([name[k], hit.has(k) ? "a b u" : "b t"]);
    } else line.push([name, "b t"]);
    line.push(["  ", ""]);
    // On the selected row, the details are in the text colour, as in the launcher.
    const detail = sel ? "t" : "f";
    if (showAgent) line.push([fit(s.agent, 8), detail], ["  ", ""]);
    if (showFolder) line.push([fit(hostName ? `${hostName}:${s.cwd}` : s.cwd, folderW), detail], ["  ", ""]);
    line.push([fit(st.word, 12), st.cls === "f" && sel ? "t" : st.cls]);
    line.push([right(fmtAgo(ago), 6), sel ? "" : "f"], ["  ", ""]);
    const trCls = s.state === "needs" ? "w" : s.state === "working" ? "a" : "f";
    line.push([this.trace(this.events.get(s.id) || [], motionOn() ? t : this.t0), trCls]);
    return line;
  }

  hostRow(h, i, cw, t, sel) {
    const line = [];
    line.push(sel ? ["▍", "a"] : [" ", ""]);
    line.push([" ", ""], [String(i + 1), sel ? "" : "f"], [" ", ""]);
    line.push(h.online ? ["●", "o"] : ["◌", "f"], [" ", ""]);
    line.push([fit(h.name, 11), h.online ? "b t" : "f"], ["  ", ""]);
    const used = 6 + 13;
    let summary;
    if (!h.online) summary = [["offline", "f"]];
    else {
      const n = h.sessions.length;
      const needs = h.sessions.filter((s) => s.state === "needs").length;
      summary = [[`${n} session${n === 1 ? "" : "s"}`, sel ? "" : "f"]];
      if (needs) summary.push([" · ", "f"], [`${needs} needs you`, "w"]);
    }
    line.push(...summary);
    const sw = width(summary);
    const rest = cw - used - sw - 6 - 2 - 8;
    line.push([" ".repeat(Math.max(rest, 1)), ""]);
    line.push([right(h.online ? h.rtt : "", 6), sel ? "t" : "f"], ["  ", ""]);
    const ev = [];
    for (const s of h.sessions) ev.push(...(this.events.get(s.id) || []));
    line.push([this.trace(ev, motionOn() ? t : this.t0), h.online ? "a" : "f"]);
    return line;
  }

  frame(t) {
    const cols = this.cols;
    const rows = this.rows;
    const cw = Math.min(cols - 4, 112);
    const x0 = Math.max(2, Math.floor((cols - cw) / 2));
    const lines = Array.from({ length: rows }, () => []);
    const meta = Array.from({ length: rows }, () => ({}));
    const put = (y, line, m) => {
      if (y < 0 || y >= rows) return;
      lines[y] = [[" ".repeat(x0), ""], ...line];
      if (m) meta[y] = m;
    };
    if (this.view === "attached") return this.attachedFrame(t, cols, rows);

    const motion = motionOn();
    const boot = this.bootAt < 0 || !motion ? 99 : t - this.bootAt;

    // Header.
    const h = this.curHost();
    let crumb;
    let facts;
    if (this.view === "hosts") {
      crumb = [["hosts", "f"]];
      facts = [[`${this.hosts.length} hosts`, "f"], [" · ", "f"], ["1 needs you", "w"]];
    } else if (this.view === "search") {
      crumb = [["› ", "f"], ["search", "b t"]];
      facts = [["every host", "f"]];
    } else {
      const n = h.sessions.length;
      const needs = h.sessions.filter((s) => s.state === "needs").length;
      crumb = [["› ", "f"], [this.decode(h.name, this.revealAt, t), "b t"]];
      facts = [[h.rtt, "f"], [" · ", "f"], [`${n} session${n === 1 ? "" : "s"}`, "f"]];
      if (needs) facts.push([" · ", "f"], [`${needs} needs you`, "w"]);
    }
    const left = [];
    for (const ch of this.gradientLine(this.logo(t))) left.push(ch);
    left.push([" ", ""]);
    for (const ch of this.gradientLine(this.name)) left.push(ch);
    left.push(["  ", ""], ...crumb);
    const gap = Math.max(cw - width(left) - width(facts), 1);
    put(1, [...left, [" ".repeat(gap), ""], ...(cw - width(left) > width(facts) + 2 ? facts : [])]);
    const ruleLen = Math.round(cw * ease(boot / 0.26));
    put(2, [["─".repeat(ruleLen), "f"]]);

    // The list.
    const top = 4;
    this.listTop = top;
    const list = this.list();
    const selI = Math.min(this.sel[this.view] || 0, list.length - 1);
    const maxRows = rows - top - 6;
    const slide = motion && this.slideAt >= 0 ? ease((t - this.slideAt) / 0.18) : 1;
    list.slice(0, Math.max(maxRows, 1)).forEach((item, i) => {
      const sel = i === selI;
      let line;
      if (this.view === "hosts") line = this.hostRow(item, i, cw, t, sel);
      else if (this.view === "search") line = this.sessionRow(item.s, i, cw, t, sel, item.host.name, this.query.toLowerCase());
      else line = this.sessionRow(item, i, cw, t, sel);
      const fade = motion ? ease((boot - i * 0.022) / 0.14) : 1;
      put(top + i, line, { row: i, sel, x0, cw, opacity: Math.min(fade, slide), shift: (1 - slide) * 18 * this.slideDir });
    });
    if (this.view === "search" && !list.length) put(top, [["  No session matches. ", "f"], ["esc", "a"], [" closes the search.", "f"]]);

    // The line under the list.
    const under = top + Math.min(list.length, maxRows) + 1;
    const x = list[selI];
    if (x) {
      let l;
      let r = [];
      if (this.view === "hosts") {
        l = x.online
          ? [[`  ssh ${x.name} · ${x.os} · ${x.cpus} CPUs · ${[...new Set(x.sessions.map((s) => s.agent))].join(", ")}`, "f"]]
          : [[`  ssh ${x.name} · offline`, "f"]];
      } else {
        const s = this.view === "search" ? x.s : x;
        if (s.state === "needs") l = [["  ● ", "w"], [s.ask, "w"]];
        else if (s.state === "working") l = [["  ▸ ", "a"], ["∴ Working (14s · esc to interrupt)", ""]];
        else if (s.state === "exited") l = [["  ✓ ", "o"], ["exited 0", "f"]];
        else l = [["  ○ ", "f"], ["quiet for " + fmtAgo(s.ago + t - this.t0), "f"]];
        r = [[[s.cwd, s.mem ? `${s.mem} of ${s.max}` : "", s.id].filter(Boolean).join(" · "), "f"]];
      }
      const room = cw - width(l) - width(r);
      put(under, room > 2 ? [...l, [" ".repeat(room), ""], ...r] : l, { opacity: motion ? ease((boot - 0.15) / 0.2) : 1 });
    }

    // Notices rise above the bottom rule.
    if (this.notice) {
      const age = t - this.notice.at;
      const life = 4;
      if (age > life) this.notice = null;
      else put(rows - 4, [["  ", ""], ...this.notice.line], { opacity: motion ? Math.min(1, age / 0.15, (life - age) / 0.3) : 1 });
    }

    // The bottom: a rule, the prompt line, the keys.
    put(rows - 3, [["─".repeat(cw), "f"]]);
    if (this.view === "search") {
      const blink = !motion || Math.floor((t - this.t0) * 1.6) % 2 === 0;
      put(rows - 2, [["  search ", "a"], ["❯ ", "a b"], [this.query, "t"], [" ", blink ? "rv" : ""]]);
      put(rows - 1, [["  ", ""], ...this.hints([["enter", "attach"], ["↑↓", "move"], ["esc", "close"]], cw - 2)]);
    } else {
      put(rows - 2, [["  ❯ ", "f"], ["/ search every host   n start a session", "f"]]);
      const hs = this.view === "hosts"
        ? [["enter", "open"], ["1–9", "jump"], ["/", "search"], ["?", "keys"]]
        : [["enter", "attach"], ["y", "copy attach"], ["/", "search"], ["esc", "hosts"], ["?", "keys"]];
      put(rows - 1, [["  ", ""], ...this.hints(hs, cw - 2)]);
    }

    // A sheet rises from the bottom over the dimmed list.
    if (this.sheet) {
      for (let y = 0; y < rows; y++) {
        for (const seg of lines[y]) {
          seg[1] = "f";
          seg[2] = "";
        }
      }
      const sh = this.sheet;
      const hgt = sh.items.length + 4;
      const rise = motion ? ease((t - sh.at) / 0.16) : 1;
      const y0 = rows - 1 - Math.round(hgt * rise) + 1;
      const titleRule = [["──", "f"], [` ${sh.title} `, "a b"], ["─".repeat(Math.max(cw - sh.title.length - 4, 0)), "f"]];
      put(y0, titleRule);
      put(y0 + 1, [[" ".repeat(cw), ""]]);
      sh.items.forEach(([name, k], i) => {
        const sel = i === sh.sel;
        const pad = Math.max(cw - 4 - name.length - k.length, 1);
        put(y0 + 2 + i, [[sel ? "  ▍ " : "    ", "a"], [name, "b t"], [" ".repeat(pad), ""], [k, "a b"]], { sel, x0, cw });
      });
      put(y0 + 2 + sh.items.length, [[" ".repeat(cw), ""]]);
      put(rows - 1, [["  ", ""], ...this.hints([["enter", "run"], ["↑↓", "move"], ["esc", "close"]], cw - 2)]);
    }

    // Jack in: the other rows fade, and a cyan line runs down from the chosen row.
    if (this.jack) {
      const j = this.jack;
      const p = (t - j.at) / 0.17;
      for (let y = 0; y < rows; y++) if (y !== j.row && meta[y]) meta[y].opacity = Math.min(meta[y].opacity ?? 1, 1 - 0.7 * ease(p));
      if (p >= 1) {
        this.enter(j.s);
        return this.attachedFrame(t, cols, rows);
      }
      return { lines, meta, jack: { x: x0, y: j.row, p } };
    }
    return { lines, meta };
  }

  attachedFrame(t, cols, rows) {
    const s = this.agent;
    const lines = Array.from({ length: rows }, () => []);
    const meta = Array.from({ length: rows }, () => ({}));
    let talk = agentScreen(s);
    const room = rows - 5;
    if (talk.length > room) talk = talk.slice(talk.length - room);
    talk.forEach((l, i) => {
      lines[1 + i] = [[" ", ""], ...l.map(([a, b]) => [a.length > cols - 2 ? a.slice(0, cols - 3) + "…" : a, b])];
    });
    const w = cols - 2;
    lines[rows - 4] = [["╭" + "─".repeat(w) + "╮", "f"]];
    const typed = this.typed.slice(-(w - 4));
    const blink = !motionOn() || Math.floor((t - this.t0) * 1.6) % 2 === 0;
    lines[rows - 3] = [["│", "f"], [" > ", ""], [typed, ""], [" ", blink ? "rv" : ""], [" ".repeat(Math.max(w - 4 - typed.length, 0)), ""], ["│", "f"]];
    lines[rows - 2] = [["╰" + "─".repeat(w) + "╯", "f"]];
    const where = `${s.cwd} · ${s.agent}`;
    const leftHint = " ? for shortcuts";
    lines[rows - 1] = [[leftHint + " ".repeat(Math.max(cols - leftHint.length - where.length - 1, 1)) + where, "f"]];
    return { lines, meta };
  }

  render(t) {
    if (!this.cols) return;
    if (motionOn()) this.feed(t);
    const { lines, meta, jack } = this.frame(t);
    for (let y = 0; y < this.rows; y++) {
      const m = meta[y] || {};
      let html = "";
      for (const [text, cls, style] of lines[y]) {
        if (!text) continue;
        const c = cls ? ` class="${cls}"` : "";
        const st = style ? ` style="${style}"` : "";
        html += c || st ? `<span${c}${st}>${esc(text)}</span>` : esc(text);
      }
      const r = this.rowEls[y];
      const key = html + "|" + (m.sel ? 1 : 0) + "|" + (m.row ?? "");
      if (r.html !== key) {
        r.el.innerHTML = html;
        r.html = key;
        r.el.classList.toggle("sel", !!m.sel);
        if (m.sel) {
          r.el.style.setProperty("--x0", `${m.x0}ch`);
          r.el.style.setProperty("--x1", `${m.x0 + m.cw}ch`);
        }
        if (m.row !== undefined) r.el.dataset.row = m.row;
        else delete r.el.dataset.row;
      }
      const op = m.opacity ?? 1;
      r.el.style.opacity = op < 1 ? op.toFixed(3) : "";
      r.el.style.transform = m.shift ? `translateX(${m.shift.toFixed(1)}px)` : "";
    }
    let bar = this.tty.querySelector(".tty-jack");
    if (jack) {
      if (!bar) {
        bar = document.createElement("div");
        bar.className = "tty-jack";
        this.tty.append(bar);
      }
      const pitch = this.size * 1.25;
      bar.style.setProperty("--jx", `calc(${jack.x}ch + 0.25rem)`);
      bar.style.setProperty("--jy", `calc(0.75rem + ${(jack.y + 1) * pitch}px)`);
      bar.style.setProperty("--jh", `${(this.rows - jack.y - 1) * pitch}px`);
    } else if (bar) bar.remove();
  }
}
