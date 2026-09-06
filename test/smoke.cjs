// Smoke test for the customizable <select> demo.
//
// Runs headless Chromium (135+ required for appearance: base-select), opens
// index.html from disk, and checks that:
//   - the browser reports base-select support and the page shows the banner
//   - the "fruit" select renders its rich <button>/<selectedcontent> markup
//   - clicking the select opens the ::picker(select) popover
//   - choosing an option changes the value and fires `change`
//   - the theme picker re-themes the page
// Screenshots are written to test/screenshots/.
//
// Usage: npm test          (needs playwright resolvable; `npm i` or a global
//        install with NODE_PATH=$(npm root -g) both work)

const path = require("node:path");
const fs = require("node:fs");
const assert = require("node:assert/strict");

let chromium;
try {
  ({ chromium } = require("playwright"));
} catch (e) {
  console.error(
    "playwright is not resolvable. Run `npm install` or `NODE_PATH=$(npm root -g) npm test`."
  );
  process.exit(2);
}

const root = path.resolve(__dirname, "..");
const url = "file://" + path.join(root, "index.html");
const shots = path.join(__dirname, "screenshots");
fs.mkdirSync(shots, { recursive: true });

(async () => {
  const launchOpts = {};
  if (process.env.CHROMIUM_PATH) launchOpts.executablePath = process.env.CHROMIUM_PATH;
  const browser = await chromium.launch(launchOpts);
  const page = await browser.newPage({ viewport: { width: 1100, height: 900 } });
  page.on("pageerror", (err) => { throw err; });

  await page.goto(url);

  const version = browser.version();
  const supported = await page.evaluate(() => CSS.supports("appearance", "base-select"));
  console.log(`Chromium ${version} · base-select supported: ${supported}`);
  assert.equal(supported, true, "this Chromium does not support appearance: base-select (need 135+)");

  // Banner reflects support.
  assert.equal(await page.getAttribute("#support", "data-state"), "yes");

  // Rich markup survived the parser and <selectedcontent> is populated.
  const fruit = page.locator("#fruit");
  assert.equal(await fruit.locator("button > selectedcontent").count(), 1);
  const hasSelectedContentEl = await page.evaluate(
    () => typeof HTMLSelectedContentElement !== "undefined"
  );
  console.log(`HTMLSelectedContentElement present: ${hasSelectedContentEl}`);
  const buttonText = (await fruit.locator("button").innerText()).replace(/\s+/g, " ").trim();
  assert.match(buttonText, /Apple/, `expected selectedcontent to mirror "Apple", got "${buttonText}"`);

  await page.screenshot({ path: path.join(shots, "01-closed.png"), fullPage: true });

  // Open the picker and check :open matches.
  await fruit.click();
  await page.waitForFunction(() => document.querySelector("#fruit").matches(":open"));
  await page.waitForTimeout(250); // let the entry transition finish for the screenshot
  await page.screenshot({ path: path.join(shots, "02-open.png") });

  // Pick "Mango".
  const changed = page.evaluate(
    () => new Promise((resolve) =>
      document.querySelector("#fruit").addEventListener("change", () => resolve(true), { once: true })
    )
  );
  await fruit.locator("option[value=mango]").click();
  assert.equal(await changed, true, "change event did not fire");
  assert.equal(await fruit.inputValue(), "mango");
  assert.equal(await page.locator("#fruit-out").innerText(), "mango");
  await page.waitForFunction(() => !document.querySelector("#fruit").matches(":open"));
  const afterText = (await fruit.locator("button").innerText()).replace(/\s+/g, " ").trim();
  assert.match(afterText, /Mango/, `expected selectedcontent to update to "Mango", got "${afterText}"`);

  // Keyboard still works. With base-select, ArrowDown on a closed select opens
  // the picker, ArrowDown moves through the options, Enter commits.
  const sort = page.locator("select[name=sort]");
  await sort.focus();
  const before = await sort.inputValue();
  await page.keyboard.press("ArrowDown");
  await page.waitForFunction(() => document.querySelector("select[name=sort]").matches(":open"));
  await page.keyboard.press("ArrowDown");
  await page.keyboard.press("Enter");
  await page.waitForFunction(() => !document.querySelector("select[name=sort]").matches(":open"));
  const after = await sort.inputValue();
  assert.notEqual(before, after, "ArrowDown + Enter should change the selected option");
  console.log(`keyboard: ${before} → ${after}`);

  // Theme picker re-themes the page.
  await page.selectOption("#theme", "#10b981");
  const accent = await page.evaluate(() =>
    getComputedStyle(document.documentElement).getPropertyValue("--accent").trim()
  );
  assert.equal(accent, "#10b981");

  await page.screenshot({ path: path.join(shots, "03-after.png"), fullPage: true });

  await browser.close();
  console.log("OK · screenshots in test/screenshots/");
})().catch((err) => {
  console.error(err);
  process.exit(1);
});
