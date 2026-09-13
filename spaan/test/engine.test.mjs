import { test } from "node:test";
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import {
  emptyPlan, project, resolveYear, parseWhen, formatWhen, validatePlan, summarize,
} from "../public/engine.js";

const people = [
  { id: "ada", name: "Ada", born: 1985, role: "adult" },
  { id: "tom", name: "Tom", born: 2025, role: "child" },
];

function plan(overrides = {}) {
  return { ...emptyPlan(2026), horizonYears: 5, inflation: 0, growth: 0, people, ...overrides };
}

test("resolveYear handles open, year and person@age", () => {
  const p = plan();
  assert.equal(resolveYear(null, p), null);
  assert.equal(resolveYear(undefined, p), null);
  assert.equal(resolveYear({ year: 2031 }, p), 2031);
  assert.equal(resolveYear({ person: "tom", age: 5 }, p), 2030);
  assert.throws(() => resolveYear({ person: "nobody", age: 1 }, p), /unknown person/);
});

test("parseWhen / formatWhen round-trip the text form", () => {
  const p = plan();
  assert.equal(parseWhen("", p), null);
  assert.deepEqual(parseWhen("2031", p), { year: 2031 });
  assert.deepEqual(parseWhen("tom@5", p), { person: "tom", age: 5 });
  assert.deepEqual(parseWhen("Tom @ 18", p), { person: "tom", age: 18 });
  assert.throws(() => parseWhen("someday", p), /can't read/);
  assert.throws(() => parseWhen("zed@3", p), /unknown person/);
  assert.equal(formatWhen(null), "");
  assert.equal(formatWhen({ year: 2031 }), "2031");
  assert.equal(formatWhen({ person: "tom", age: 5 }), "tom@5");
});

test("validatePlan reports the obvious mistakes", () => {
  assert.deepEqual(validatePlan(plan()), []);
  const bad = plan({
    people: [...people, { id: "ada", name: "Dup", born: 1990 }],
    incomes: [{ id: "x", name: "Backwards", amount: 1, from: { year: 2030 }, to: { year: 2028 } }],
    oneOffs: [{ id: "y", name: "Whenever", amount: 5, when: null }],
  });
  const problems = validatePlan(bad);
  assert.ok(problems.some((m) => /duplicate person id/.test(m)), problems.join("\n"));
  assert.ok(problems.some((m) => /ends before it starts/.test(m)), problems.join("\n"));
  assert.ok(problems.some((m) => /one-off "Whenever"/.test(m)), problems.join("\n"));
  assert.throws(() => project(bad), /duplicate person id/);
});

test("ages are null before birth and count up from 0", () => {
  const { rows } = project(plan({ people: [{ id: "kid", name: "Kid", born: 2028 }] }));
  assert.deepEqual(rows.map((r) => r.ages.kid), [null, null, 0, 1, 2]);
});

test("a flat plan with no inflation or growth is plain arithmetic", () => {
  const { rows, summary } = project(plan({
    openingBalance: 100,
    incomes: [{ id: "i", name: "Job", amount: 50, from: null, to: null }],
    expenses: [{ id: "e", name: "Rent", amount: 30, from: null, to: null, category: "housing" }],
    oneOffs: [{ id: "o", name: "Car", amount: 45, when: { year: 2028 } }],
  }));
  assert.deepEqual(rows.map((r) => r.net), [20, 20, -25, 20, 20]);
  assert.deepEqual(rows.map((r) => r.balance), [120, 140, 115, 135, 155]);
  assert.equal(rows[2].oneOffs, 45);
  assert.equal(rows[0].byCategory.housing, 30);
  assert.equal(summary.finalBalance, 155);
  assert.deepEqual(summary.lowest, { year: 2028, balance: 115 });
  assert.equal(summary.firstNegativeYear, null);
});

test("person@age windows switch lines on and off, inclusive at both ends", () => {
  const { rows } = project(plan({
    horizonYears: 8, // 2026..2033; Tom is 1 in 2026 and 4 in 2029
    expenses: [{ id: "d", name: "Daycare", amount: 10, from: { person: "tom", age: 1 }, to: { person: "tom", age: 4 } }],
  }));
  assert.deepEqual(rows.map((r) => r.expenses), [10, 10, 10, 10, 0, 0, 0, 0]);
});

test("indexed amounts grow with inflation, unindexed ones do not, growth overrides", () => {
  const { rows } = project(plan({
    inflation: 0.10, horizonYears: 3,
    incomes: [
      { id: "a", name: "Indexed", amount: 100, from: null, to: null },
      { id: "b", name: "Fixed", amount: 100, from: null, to: null, indexed: false },
      { id: "c", name: "Fast", amount: 100, from: null, to: null, growth: 0.5 },
    ],
  }));
  const r = rows[2];
  assert.ok(Math.abs(r.lines.incomes.a - 121) < 1e-9);
  assert.equal(r.lines.incomes.b, 100);
  assert.equal(r.lines.incomes.c, 225);
  assert.ok(Math.abs(r.deflator - 1.21) < 1e-9);
});

test("balance compounds before the year's net is added", () => {
  const { rows } = project(plan({
    growth: 0.10, horizonYears: 2, openingBalance: 1000,
    incomes: [{ id: "i", name: "Job", amount: 100, from: null, to: null }],
  }));
  assert.ok(Math.abs(rows[0].balance - 1200) < 1e-9);
  assert.ok(Math.abs(rows[1].balance - 1420) < 1e-9);
});

test("summary finds the first negative year and the trough", () => {
  const s = summarize([
    { year: 2026, balance: 10, income: 1, expenses: 1, oneOffs: 0 },
    { year: 2027, balance: -5, income: 1, expenses: 1, oneOffs: 0 },
    { year: 2028, balance: -20, income: 1, expenses: 1, oneOffs: 0 },
    { year: 2029, balance: 3, income: 1, expenses: 1, oneOffs: 0 },
  ]);
  assert.equal(s.firstNegativeYear, 2027);
  assert.deepEqual(s.lowest, { year: 2028, balance: -20 });
  assert.equal(s.finalBalance, 3);
  assert.equal(s.totalIncome, 4);
});

test("the shipped example plan is valid and projects the full horizon", async () => {
  const example = JSON.parse(await readFile(new URL("../public/example-plan.json", import.meta.url), "utf8"));
  assert.deepEqual(validatePlan(example), []);
  const { rows, summary } = project(example);
  assert.equal(rows.length, example.horizonYears);
  assert.equal(rows[0].year, example.startYear);
  // Daycare for kid1 (born 2025) runs ages 1..4 = 2026..2029.
  assert.ok(rows[0].lines.expenses.daycare1 > 0);
  assert.equal(rows[4].lines.expenses.daycare1, undefined);
  assert.ok(Number.isFinite(summary.finalBalance));
});
