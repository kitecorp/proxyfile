// Draws the Mermaid blocks of a page, as GitHub does. Mermaid is large, so it is asked for only when a diagram is
// about to be seen, from the folder this file is served from, which holds the copy the build checked. Until a block is
// drawn the theme hides its source and keeps its place, and a reader without scripts sees the source as code. Each
// diagram keeps its natural size and a wide one scrolls inside its own box, as tables and code do. The diagrams take
// the page's light or dark scheme and are drawn again when it changes.
(function () {
  var blocks = Array.prototype.slice.call(document.querySelectorAll("pre.mermaid"));
  if (blocks.length === 0) return;
  var script = document.currentScript.src.replace(/[^/]*$/, "mermaid.min.js");
  var dark = window.matchMedia("(prefers-color-scheme: dark)");
  var sources = blocks.map(function (block) { return block.textContent; });
  var natural = { useMaxWidth: false };

  function draw() {
    blocks.forEach(function (block, i) {
      block.removeAttribute("data-processed");
      block.textContent = sources[i];
    });
    window.mermaid.initialize({
      startOnLoad: false,
      theme: dark.matches ? "dark" : "default",
      flowchart: natural, sequence: natural, class: natural, state: natural, er: natural, journey: natural, gantt: natural
    });
    window.mermaid.run({ nodes: blocks });
  }

  var asked = false;
  function load() {
    if (asked) return;
    asked = true;
    var tag = document.createElement("script");
    tag.src = script;
    tag.onload = function () {
      draw();
      dark.addEventListener("change", draw);
    };
    document.head.appendChild(tag);
  }

  if (!("IntersectionObserver" in window)) {
    load();
    return;
  }
  // A screen's height ahead, so a diagram is usually drawn before it scrolls into view.
  var watcher = new IntersectionObserver(function (entries) {
    if (entries.some(function (entry) { return entry.isIntersecting; })) {
      watcher.disconnect();
      load();
    }
  }, { rootMargin: "100% 0px" });
  blocks.forEach(function (block) { watcher.observe(block); });
})();
