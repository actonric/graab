// Graab · customizable <select> playground
//
// Nothing here is required for the selects to work. This file only:
//   1. reports whether the browser supports `appearance: base-select`
//   2. mirrors a value into an <output> for the "rich" demo
//   3. re-themes the page from the colour picker demo
//   4. polyfills <selectedcontent> mirroring in a browser that supports
//      base-select but shipped without the element (some early builds).

(function () {
  "use strict";

  var supportsBaseSelect =
    typeof CSS !== "undefined" &&
    CSS.supports &&
    CSS.supports("appearance", "base-select");

  // 1. Support banner ------------------------------------------------------
  var banner = document.getElementById("support");
  if (banner) banner.dataset.state = supportsBaseSelect ? "yes" : "no";
  document.documentElement.dataset.baseSelect = supportsBaseSelect ? "yes" : "no";

  // 2. Readout for the fruit demo ------------------------------------------
  var fruit = document.getElementById("fruit");
  var fruitOut = document.getElementById("fruit-out");
  if (fruit && fruitOut) {
    var sync = function () { fruitOut.value = fruit.value; };
    fruit.addEventListener("change", sync);
    sync();
  }

  // 3. Live accent colour --------------------------------------------------
  var theme = document.getElementById("theme");
  if (theme) {
    var applyTheme = function () {
      document.documentElement.style.setProperty("--accent", theme.value);
    };
    theme.addEventListener("change", applyTheme);
    applyTheme();
  }

  // 4. <selectedcontent> fallback -----------------------------------------
  // Chrome 135+ implements <selectedcontent> natively and keeps it in sync.
  // If base-select is supported but the element is not (an edge case in a
  // few early builds), clone the selected option's children into it by hand.
  var hasSelectedContent = typeof window.HTMLSelectedContentElement !== "undefined";
  if (supportsBaseSelect && !hasSelectedContent) {
    var selects = document.querySelectorAll("select.graab");
    Array.prototype.forEach.call(selects, function (select) {
      var target = select.querySelector("selectedcontent");
      if (!target) return;
      var mirror = function () {
        var opt = select.selectedOptions[0];
        target.replaceChildren();
        if (!opt) return;
        var clone = opt.cloneNode(true);
        while (clone.firstChild) target.appendChild(clone.firstChild);
      };
      select.addEventListener("change", mirror);
      mirror();
    });
  }
})();
