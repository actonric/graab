// spaan projection engine.
//
// Pure functions over a plan object; no DOM, no I/O. The browser app imports
// this file as an ES module and so does `node --test test/`.
//
// A plan describes a household in today's money. project() rolls it forward
// year by year and returns one row per year with income, spending, one-off
// costs, net cash flow and the running balance.
//
// Timing. Every income, expense and one-off has a `from`/`to` (or `when`)
// that resolves to a calendar year:
//   null or undefined          open-ended (plan start / plan end)
//   { year: 2031 }             that calendar year
//   { person: "thomas", age: 5 }   the year that person turns 5
// Ranges are inclusive at both ends. The text form used by the UI is
// "" | "2031" | "thomas@5"; see parseWhen()/formatWhen().
//
// Money. Amounts are annual and in `startYear` money. A line with
// `indexed !== false` grows at the plan's inflation rate; a line may set its
// own `growth` rate instead (e.g. a salary you expect to beat inflation).
// One-offs are costs; a windfall is a one-off with a negative amount.
// The balance earns `plan.growth` per year and the year's net is added at
// year end.

export const PLAN_VERSION = 1;

export function emptyPlan(startYear = new Date().getFullYear()) {
  return {
    version: PLAN_VERSION,
    name: "Our plan",
    currency: "USD",
    startYear,
    horizonYears: 20,
    inflation: 0.03,
    growth: 0.04,
    openingBalance: 0,
    people: [],
    incomes: [],
    expenses: [],
    oneOffs: [],
  };
}

export const EXPENSE_CATEGORIES = [
  "housing", "childcare", "education", "food", "transport", "health",
  "insurance", "travel", "activities", "giving", "other",
];

// ---------------------------------------------------------------- timing --

export function personById(plan, id) {
  return (plan.people || []).find((p) => p.id === id) || null;
}

// Resolve a timing object to a calendar year, or null when open-ended.
// Throws on a reference to an unknown person so validatePlan() can report it.
export function resolveYear(when, plan) {
  if (when == null || when === "") return null;
  if (typeof when === "number") return when;
  if (typeof when.year === "number") return when.year;
  if (when.person != null) {
    const p = personById(plan, when.person);
    if (!p) throw new Error(`unknown person "${when.person}"`);
    if (typeof when.age !== "number") throw new Error(`missing age for "${when.person}"`);
    return p.born + when.age;
  }
  throw new Error(`bad timing ${JSON.stringify(when)}`);
}

// "" -> null, "2031" -> {year}, "thomas@5" -> {person, age}. Person may be
// given by id or (case-insensitively) by name.
export function parseWhen(text, plan) {
  const s = String(text ?? "").trim();
  if (s === "") return null;
  if (/^\d{4}$/.test(s)) return { year: Number(s) };
  const m = s.match(/^([^@]+)@\s*(\d{1,3})$/);
  if (m) {
    const key = m[1].trim().toLowerCase();
    const p = (plan.people || []).find(
      (q) => q.id.toLowerCase() === key || String(q.name).toLowerCase() === key,
    );
    if (!p) throw new Error(`unknown person "${m[1].trim()}"`);
    return { person: p.id, age: Number(m[2]) };
  }
  throw new Error(`can't read "${s}": use a year like 2031 or name@age like thomas@5`);
}

export function formatWhen(when) {
  if (when == null) return "";
  if (typeof when === "number") return String(when);
  if (typeof when.year === "number") return String(when.year);
  if (when.person != null) return `${when.person}@${when.age}`;
  return "";
}

// ------------------------------------------------------------ validation --

// Returns [] when the plan is usable, else a list of human-readable problems.
export function validatePlan(plan) {
  const problems = [];
  const bad = (msg) => problems.push(msg);
  if (!plan || typeof plan !== "object") return ["plan is not an object"];
  if (!Number.isInteger(plan.startYear)) bad("startYear must be a whole year");
  if (!Number.isInteger(plan.horizonYears) || plan.horizonYears < 1 || plan.horizonYears > 100) {
    bad("horizonYears must be between 1 and 100");
  }
  for (const k of ["inflation", "growth", "openingBalance"]) {
    if (typeof plan[k] !== "number" || Number.isNaN(plan[k])) bad(`${k} must be a number`);
  }
  const ids = new Set();
  for (const p of plan.people || []) {
    if (!p.id) bad(`person "${p.name || "?"}" has no id`);
    else if (ids.has(p.id)) bad(`duplicate person id "${p.id}"`);
    ids.add(p.id);
    if (!Number.isInteger(p.born)) bad(`person "${p.name || p.id}" needs a birth year`);
  }
  const checkRange = (kind, line) => {
    let a, b;
    try { a = resolveYear(line.from, plan); } catch (e) { bad(`${kind} "${line.name}": ${e.message}`); }
    try { b = resolveYear(line.to, plan); } catch (e) { bad(`${kind} "${line.name}": ${e.message}`); }
    if (a != null && b != null && a > b) bad(`${kind} "${line.name}" ends before it starts`);
    if (typeof line.amount !== "number" || Number.isNaN(line.amount)) bad(`${kind} "${line.name}" needs an amount`);
  };
  for (const l of plan.incomes || []) checkRange("income", l);
  for (const l of plan.expenses || []) checkRange("expense", l);
  for (const l of plan.oneOffs || []) {
    try { if (resolveYear(l.when, plan) == null) bad(`one-off "${l.name}" needs a year or name@age`); }
    catch (e) { bad(`one-off "${l.name}": ${e.message}`); }
    if (typeof l.amount !== "number" || Number.isNaN(l.amount)) bad(`one-off "${l.name}" needs an amount`);
  }
  return problems;
}

// ------------------------------------------------------------ projection --

function lineRate(line, plan) {
  if (typeof line.growth === "number") return line.growth;
  return line.indexed === false ? 0 : plan.inflation;
}

function amountIn(line, year, plan) {
  return line.amount * Math.pow(1 + lineRate(line, plan), year - plan.startYear);
}

function active(line, year, plan) {
  const a = resolveYear(line.from, plan);
  const b = resolveYear(line.to, plan);
  return (a == null || year >= a) && (b == null || year <= b);
}

// Roll the plan forward. Returns { rows, summary }.
//
// row = {
//   year, index, deflator,
//   ages: { [personId]: age | null },      null before birth
//   income, expenses, oneOffs, net, balance,
//   lines: { incomes: {[id]: n}, expenses: {[id]: n}, oneOffs: {[id]: n} },
//   byCategory: { [category]: n },
// }
// All money is nominal (that year's dollars); divide by `deflator` for
// startYear money.
export function project(plan) {
  const problems = validatePlan(plan);
  if (problems.length) throw new Error(problems.join("; "));

  const rows = [];
  let balance = plan.openingBalance;
  for (let i = 0; i < plan.horizonYears; i++) {
    const year = plan.startYear + i;
    const row = {
      year, index: i,
      deflator: Math.pow(1 + plan.inflation, i),
      ages: {},
      income: 0, expenses: 0, oneOffs: 0, net: 0, balance: 0,
      lines: { incomes: {}, expenses: {}, oneOffs: {} },
      byCategory: {},
    };
    for (const p of plan.people) {
      const age = year - p.born;
      row.ages[p.id] = age >= 0 ? age : null;
    }
    for (const l of plan.incomes) {
      if (!active(l, year, plan)) continue;
      const v = amountIn(l, year, plan);
      row.lines.incomes[l.id] = v;
      row.income += v;
    }
    for (const l of plan.expenses) {
      if (!active(l, year, plan)) continue;
      const v = amountIn(l, year, plan);
      row.lines.expenses[l.id] = v;
      row.expenses += v;
      const c = l.category || "other";
      row.byCategory[c] = (row.byCategory[c] || 0) + v;
    }
    for (const l of plan.oneOffs) {
      if (resolveYear(l.when, plan) !== year) continue;
      const v = amountIn(l, year, plan);
      row.lines.oneOffs[l.id] = v;
      row.oneOffs += v;
    }
    row.net = row.income - row.expenses - row.oneOffs;
    balance = balance * (1 + plan.growth) + row.net;
    row.balance = balance;
    rows.push(row);
  }
  return { rows, summary: summarize(rows) };
}

export function summarize(rows) {
  if (!rows.length) return { finalBalance: 0, lowest: null, firstNegativeYear: null, totalIncome: 0, totalSpend: 0 };
  let lowest = rows[0];
  let firstNegativeYear = null;
  let totalIncome = 0, totalSpend = 0;
  for (const r of rows) {
    if (r.balance < lowest.balance) lowest = r;
    if (firstNegativeYear == null && r.balance < 0) firstNegativeYear = r.year;
    totalIncome += r.income;
    totalSpend += r.expenses + r.oneOffs;
  }
  return {
    finalBalance: rows[rows.length - 1].balance,
    lowest: { year: lowest.year, balance: lowest.balance },
    firstNegativeYear,
    totalIncome,
    totalSpend,
  };
}

// ---------------------------------------------------------------- helpers --

let counter = 0;
export function newId(prefix = "l") {
  counter += 1;
  return `${prefix}-${Date.now().toString(36)}-${counter.toString(36)}`;
}

// Turn a display name into a stable id: "Thomas" -> "thomas".
export function slug(name) {
  return String(name).toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, "") || "p";
}
