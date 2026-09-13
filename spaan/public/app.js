// spaan UI. All the arithmetic lives in engine.js; this file only binds the
// plan to the page and keeps it in localStorage.
import {
  emptyPlan, project, validatePlan, parseWhen, formatWhen, resolveYear,
  newId, slug, EXPENSE_CATEGORIES, PLAN_VERSION,
} from "./engine.js";

const STORAGE_KEY = "spaan.plan";
const $ = (sel) => document.querySelector(sel);

let plan = loadPlan();
let realMoney = false;
let lastProjection = null;

// ----------------------------------------------------------- persistence --

function loadPlan() {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    if (raw) {
      const p = JSON.parse(raw);
      if (p && typeof p === "object" && p.version === PLAN_VERSION) return p;
    }
  } catch { /* fall through to a fresh plan */ }
  return emptyPlan();
}

function save() {
  try { localStorage.setItem(STORAGE_KEY, JSON.stringify(plan)); } catch { /* private mode etc. */ }
}

// ------------------------------------------------------------- formatting --

function money(n) {
  const cur = /^[A-Z]{3}$/.test(plan.currency || "") ? plan.currency : "USD";
  try {
    return new Intl.NumberFormat(undefined, { style: "currency", currency: cur, maximumFractionDigits: 0 }).format(n);
  } catch {
    return Math.round(n).toLocaleString();
  }
}
function esc(s) {
  return String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
}
function signed(n) { return n < 0 ? "neg" : n > 0 ? "pos" : ""; }

// ---------------------------------------------------------------- render --

function renderAll() {
  $("#planName").value = plan.name || "";
  for (const el of document.querySelectorAll("[data-setting]")) {
    const v = plan[el.dataset.setting];
    el.value = el.dataset.pct ? Math.round(v * 1000) / 10 : (v ?? "");
  }
  renderPeople();
  renderLines("incomes", ["name", "amount", "from", "to", "growth"]);
  renderLines("expenses", ["name", "category", "amount", "from", "to", "indexed"]);
  renderLines("oneOffs", ["name", "amount", "when"]);
  renderProjection();
}

function renderPeople() {
  const rows = plan.people.map((p, i) => `
    <tr data-list="people" data-idx="${i}">
      <td class="w-name"><input data-field="name" value="${esc(p.name)}" placeholder="Name"></td>
      <td class="w-amt"><input data-field="born" class="num" type="number" value="${esc(p.born ?? "")}" placeholder="Born"></td>
      <td class="w-cat"><select data-field="role">
        <option value="adult"${p.role === "adult" ? " selected" : ""}>adult</option>
        <option value="child"${p.role === "child" ? " selected" : ""}>child</option>
      </select></td>
      <td class="w-x"><button class="small danger" data-remove title="Remove">×</button></td>
    </tr>`).join("");
  $("#people").innerHTML = `
    <thead><tr><th>Name</th><th class="n">Born</th><th>Role</th><th></th></tr></thead>
    <tbody>${rows || `<tr><td colspan="4" class="empty">No one yet.</td></tr>`}</tbody>
    <tfoot><tr><td colspan="4"><button class="small" data-add="people">+ Add person</button></td></tr></tfoot>`;
}

const HEAD = {
  name: "Name", category: "Category", amount: "Amount / yr", from: "From", to: "To",
  when: "When", growth: "Growth % / yr", indexed: "Indexed",
};

function cell(list, line, field) {
  switch (field) {
    case "name":
      return `<td class="w-name"><input data-field="name" value="${esc(line.name)}" placeholder="Name"></td>`;
    case "category":
      return `<td class="w-cat"><select data-field="category">${EXPENSE_CATEGORIES.map((c) =>
        `<option value="${c}"${(line.category || "other") === c ? " selected" : ""}>${c}</option>`).join("")}</select></td>`;
    case "amount":
      return `<td class="w-amt"><input data-field="amount" class="num" type="number" step="100" value="${esc(line.amount ?? "")}"></td>`;
    case "from": case "to": case "when":
      return `<td class="w-when"><input data-field="${field}" data-when value="${esc(formatWhen(line[field]))}" placeholder="${field === "when" ? "2031 or kid@18" : "open"}"></td>`;
    case "growth":
      return `<td class="w-amt"><input data-field="growth" class="num" type="number" step="0.1" value="${typeof line.growth === "number" ? Math.round(line.growth * 1000) / 10 : ""}" placeholder="inflation"></td>`;
    case "indexed":
      return `<td class="w-x"><input data-field="indexed" type="checkbox"${line.indexed === false ? "" : " checked"} title="Rises with inflation"></td>`;
    default:
      return "<td></td>";
  }
}

function renderLines(list, fields) {
  const rows = plan[list].map((line, i) =>
    `<tr data-list="${list}" data-idx="${i}">${fields.map((f) => cell(list, line, f)).join("")}
     <td class="w-x"><button class="small danger" data-remove title="Remove">×</button></td></tr>`).join("");
  const label = { incomes: "income", expenses: "expense", oneOffs: "one-off" }[list];
  const head = (f) => (f === "amount" && list === "oneOffs" ? "Amount" : HEAD[f]);
  $(`#${list}`).innerHTML = `
    <thead><tr>${fields.map((f) => `<th${f === "amount" || f === "growth" ? ' class="n"' : ""}>${head(f)}</th>`).join("")}<th></th></tr></thead>
    <tbody>${rows || `<tr><td colspan="${fields.length + 1}" class="empty">Nothing yet.</td></tr>`}</tbody>
    <tfoot><tr><td colspan="${fields.length + 1}"><button class="small" data-add="${list}">+ Add ${label}</button></td></tr></tfoot>`;
}

function renderProjection() {
  const problems = validatePlan(plan);
  const box = $("#problems");
  if (problems.length) {
    box.className = "banner";
    box.textContent = problems.join("\n");
    box.hidden = false;
    if (!lastProjection) { $("#projection").innerHTML = ""; $("#chart").innerHTML = ""; $("#tiles").innerHTML = ""; }
    return;
  }
  box.hidden = true;
  lastProjection = project(plan);
  const { rows, summary } = lastProjection;
  const val = (r, n) => (realMoney ? n / r.deflator : n);

  // Summary tiles.
  const tiles = [
    ["Final balance", money(val(rows[rows.length - 1], summary.finalBalance)), summary.finalBalance < 0],
    ["Lowest point", `${money(val(rows.find((r) => r.year === summary.lowest.year), summary.lowest.balance))} in ${summary.lowest.year}`, summary.lowest.balance < 0],
    ["Runs out", summary.firstNegativeYear ? String(summary.firstNegativeYear) : "never", !!summary.firstNegativeYear],
    ["Horizon", `${rows[0].year}–${rows[rows.length - 1].year}`, false],
  ];
  $("#tiles").innerHTML = tiles.map(([k, v, bad]) =>
    `<div class="tile"><div class="k">${k}</div><div class="v${bad ? " neg" : ""}">${esc(v)}</div></div>`).join("");

  // Year-by-year table.
  const kids = plan.people.filter((p) => p.role === "child");
  const head = ["Year", ...kids.map((k) => esc(k.name)), "Income", "Spending", "One-offs", "Net", "Balance"];
  const body = rows.map((r) => `<tr>
    <td>${r.year}</td>
    ${kids.map((k) => `<td class="n">${r.ages[k.id] == null ? "–" : r.ages[k.id]}</td>`).join("")}
    <td class="n">${money(val(r, r.income))}</td>
    <td class="n">${money(val(r, r.expenses))}</td>
    <td class="n">${r.oneOffs ? money(val(r, r.oneOffs)) : ""}</td>
    <td class="n ${signed(r.net)}">${money(val(r, r.net))}</td>
    <td class="n ${signed(r.balance)}">${money(val(r, r.balance))}</td>
  </tr>`).join("");
  $("#projection").innerHTML = `<thead><tr>${head.map((h, i) => `<th${i ? ' class="n"' : ""}>${h}</th>`).join("")}</tr></thead><tbody>${body}</tbody>`;

  $("#chart").innerHTML = chart(rows.map((r) => ({ year: r.year, v: val(r, r.balance) })));
}

// A small SVG line chart of the balance, with the below-zero area tinted.
function chart(points) {
  const W = 720, H = 220, L = 64, R = 12, T = 12, B = 26;
  const xs = points.map((p) => p.year), ys = points.map((p) => p.v);
  let lo = Math.min(0, ...ys), hi = Math.max(0, ...ys);
  if (hi === lo) hi = lo + 1;
  const pad = (hi - lo) * 0.08; lo -= pad; hi += pad;
  const x = (yr) => L + ((yr - xs[0]) / Math.max(1, xs[xs.length - 1] - xs[0])) * (W - L - R);
  const y = (v) => T + (1 - (v - lo) / (hi - lo)) * (H - T - B);
  const path = points.map((p, i) => `${i ? "L" : "M"}${x(p.year).toFixed(1)},${y(p.v).toFixed(1)}`).join(" ");
  const ticks = 4;
  const grid = Array.from({ length: ticks + 1 }, (_, i) => {
    const v = lo + ((hi - lo) * i) / ticks;
    return `<line class="grid" x1="${L}" x2="${W - R}" y1="${y(v).toFixed(1)}" y2="${y(v).toFixed(1)}"/>
            <text x="${L - 6}" y="${(y(v) + 4).toFixed(1)}" text-anchor="end">${compact(v)}</text>`;
  }).join("");
  const step = Math.max(1, Math.ceil(points.length / 8));
  const labels = points.filter((_, i) => i % step === 0 || i === points.length - 1)
    .map((p) => `<text x="${x(p.year).toFixed(1)}" y="${H - 8}" text-anchor="middle">${p.year}</text>`).join("");
  const z = y(0).toFixed(1);
  const first = x(xs[0]).toFixed(1), last = x(xs[xs.length - 1]).toFixed(1);
  return `<svg class="chart" viewBox="0 0 ${W} ${H}" role="img" aria-label="Balance by year">
    ${grid}
    <line class="zero" x1="${L}" x2="${W - R}" y1="${z}" y2="${z}"/>
    <clipPath id="above"><rect x="0" y="0" width="${W}" height="${z}"/></clipPath>
    <clipPath id="below"><rect x="0" y="${z}" width="${W}" height="${H}"/></clipPath>
    <path class="area" clip-path="url(#above)" d="${path} L${last},${z} L${first},${z} Z"/>
    <path class="under" clip-path="url(#below)" d="${path} L${last},${z} L${first},${z} Z"/>
    <path class="bal" d="${path}"/>
    ${labels}
  </svg>`;
}

function compact(v) {
  const a = Math.abs(v);
  const s = a >= 1e6 ? `${(a / 1e6).toFixed(1)}M` : a >= 1e3 ? `${Math.round(a / 1e3)}k` : String(Math.round(a));
  return (v < 0 ? "−" : "") + s;
}

// ---------------------------------------------------------------- events --

$("#planName").addEventListener("change", (e) => { plan.name = e.target.value; save(); });

for (const el of document.querySelectorAll("[data-setting]")) {
  el.addEventListener("change", () => {
    const k = el.dataset.setting;
    if (k === "currency") plan[k] = el.value.trim().toUpperCase();
    else {
      const n = Number(el.value);
      plan[k] = el.dataset.pct ? n / 100 : n;
    }
    save();
    renderProjection();
  });
}

$("#realMoney").addEventListener("change", (e) => { realMoney = e.target.checked; renderProjection(); });

document.addEventListener("change", (e) => {
  const el = e.target;
  const tr = el.closest("tr[data-list]");
  if (!tr || !el.dataset.field) return;
  const line = plan[tr.dataset.list][Number(tr.dataset.idx)];
  const f = el.dataset.field;
  el.classList.remove("bad");
  if (el.dataset.when !== undefined) {
    try { line[f] = parseWhen(el.value, plan); el.value = formatWhen(line[f]); el.title = ""; }
    catch (err) { el.classList.add("bad"); el.title = err.message; return; }
  } else if (f === "indexed") {
    if (el.checked) delete line.indexed; else line.indexed = false;
  } else if (f === "growth") {
    if (el.value.trim() === "") delete line.growth; else line.growth = Number(el.value) / 100;
  } else if (el.type === "number") {
    line[f] = el.value === "" ? NaN : Number(el.value);
  } else if (f === "name" && tr.dataset.list === "people") {
    renamePerson(line, el.value);
  } else {
    line[f] = el.value;
  }
  save();
  renderProjection();
});

document.addEventListener("click", (e) => {
  const add = e.target.closest("[data-add]");
  if (add) {
    const list = add.dataset.add;
    if (list === "people") plan.people.push({ id: newId("p"), name: "", born: NaN, role: plan.people.length < 2 ? "adult" : "child" });
    else if (list === "oneOffs") plan.oneOffs.push({ id: newId("o"), name: "", amount: 0, when: { year: plan.startYear } });
    else plan[list].push({ id: newId(list[0]), name: "", amount: 0, from: null, to: null, ...(list === "expenses" ? { category: "other" } : {}) });
    save(); renderAll();
    const last = document.querySelector(`#${list} tbody tr:last-child input`);
    if (last) last.focus();
    return;
  }
  const rm = e.target.closest("[data-remove]");
  if (rm) {
    const tr = rm.closest("tr[data-list]");
    const list = tr.dataset.list, i = Number(tr.dataset.idx);
    if (list === "people") {
      const p = plan.people[i];
      if (usesPerson(p.id)) {
        alert(`${p.name || "This person"} is referenced by a line's timing. Change those lines first.`);
        return;
      }
    }
    plan[list].splice(i, 1);
    save(); renderAll();
  }
});

// Person ids are what timing references point at, and the UI types them by
// name; keep id = slug(name) when that is unambiguous so "thomas@5" works.
function renamePerson(p, name) {
  p.name = name;
  const want = slug(name);
  if (want !== p.id && !plan.people.some((q) => q.id === want)) {
    const old = p.id;
    p.id = want;
    for (const list of ["incomes", "expenses", "oneOffs"]) {
      for (const l of plan[list]) {
        for (const k of ["from", "to", "when"]) {
          if (l[k] && l[k].person === old) l[k] = { ...l[k], person: want };
        }
      }
    }
    renderAll();
  }
}

function usesPerson(id) {
  return ["incomes", "expenses", "oneOffs"].some((list) =>
    plan[list].some((l) => ["from", "to", "when"].some((k) => l[k] && l[k].person === id)));
}

$("#loadExample").addEventListener("click", async () => {
  if (hasContent() && !confirm("Replace the current plan with the example?")) return;
  const res = await fetch("example-plan.json", { cache: "no-store" });
  plan = await res.json();
  save(); renderAll();
});

$("#resetBtn").addEventListener("click", () => {
  if (hasContent() && !confirm("Start over with an empty plan? Export first if you want to keep this one.")) return;
  plan = emptyPlan();
  lastProjection = null;
  save(); renderAll();
});

$("#exportBtn").addEventListener("click", () => {
  const blob = new Blob([JSON.stringify(plan, null, 2)], { type: "application/json" });
  const a = document.createElement("a");
  a.href = URL.createObjectURL(blob);
  a.download = `${slug(plan.name || "spaan")}.json`;
  a.click();
  setTimeout(() => URL.revokeObjectURL(a.href), 1000);
});

$("#importBtn").addEventListener("click", () => $("#importFile").click());
$("#importFile").addEventListener("change", async (e) => {
  const file = e.target.files[0];
  e.target.value = "";
  if (!file) return;
  try {
    const p = JSON.parse(await file.text());
    const problems = validatePlan(p);
    if (problems.length) throw new Error(problems.join("\n"));
    if (hasContent() && !confirm(`Replace the current plan with "${p.name || file.name}"?`)) return;
    plan = { ...emptyPlan(), ...p, version: PLAN_VERSION };
    save(); renderAll();
  } catch (err) {
    alert(`Couldn't import ${file.name}:\n${err.message}`);
  }
});

function hasContent() {
  return plan.people.length + plan.incomes.length + plan.expenses.length + plan.oneOffs.length > 0;
}

renderAll();
