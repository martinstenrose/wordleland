// Independent, self-contained enhancements, each an IIFE with its own
// header comment. Every rule from AGENTS.md's JavaScript section applies
// to all of them: vanilla, no build step, no dependency, and each is
// strictly additive — the feature it touches has a working, server-
// rendered form already, and this file only adds a shortcut or a nicety on
// top of it.

// Progressive enhancements for native <details> controls: dismiss topbar
// menus on outside clicks and nudge informational popups back on screen.
// Opening, summary-click closing and exclusivity still work without JS.
// Topbar menus keep their fixed CSS positioning.
(function () {
  "use strict";

  // Keep clicks on menu links and summaries native. Other disclosures,
  // such as result details and admin diagnostics, are not dismissible menus.
  document.addEventListener("click", function (event) {
    document.querySelectorAll('details[name="topbar-menu"][open]').forEach(function (menu) {
      if (!menu.contains(event.target)) {
        menu.open = false;
      }
    });
  });

  // Matches the gap app.css opens a popup with (top: calc(100% + 6px)), so
  // flipping above lands the same distance from the cell.
  var GAP = "6px";
  var EDGE_MARGIN = 8;

  function panelOf(details) {
    return details.querySelector(":scope > .popup-panel");
  }

  function clips(overflow) {
    return overflow === "hidden" || overflow === "auto" || overflow === "scroll" || overflow === "clip";
  }

  // The nearest ancestor that actually cuts the panel off, if any. A popup
  // can be well within the browser window and still invisible — .card
  // clips at its own rounded-corner edge, long before the viewport edge is
  // ever reached, and a table wrapped for sideways scrolling clips the
  // same way.
  function clippingAncestor(el) {
    for (var node = el.parentElement; node; node = node.parentElement) {
      var style = getComputedStyle(node);
      if (clips(style.overflowX) || clips(style.overflowY)) {
        return node;
      }
    }
    return null;
  }

  // Where the panel can actually be seen: the viewport, narrowed to
  // whatever a clipping ancestor allows.
  function bounds(panel) {
    var box = { top: 0, left: 0, right: window.innerWidth, bottom: window.innerHeight };
    var clip = clippingAncestor(panel);
    if (clip) {
      var clipBox = clip.getBoundingClientRect();
      box.top = Math.max(box.top, clipBox.top);
      box.left = Math.max(box.left, clipBox.left);
      box.right = Math.min(box.right, clipBox.right);
      box.bottom = Math.min(box.bottom, clipBox.bottom);
    }
    return box;
  }

  function reposition(details) {
    var panel = panelOf(details);
    if (!panel) return;

    // Start from the CSS default every time: a popup reopened after a
    // resize or scroll must not keep a stale adjustment from last time.
    panel.style.top = "";
    panel.style.bottom = "";
    panel.style.transform = "";

    var box = bounds(panel);
    var rect = panel.getBoundingClientRect();
    if (rect.bottom > box.bottom - EDGE_MARGIN) {
      panel.style.top = "auto";
      panel.style.bottom = "calc(100% + " + GAP + ")";
      rect = panel.getBoundingClientRect();
    }

    var shift = 0;
    if (rect.right > box.right - EDGE_MARGIN) {
      shift = box.right - EDGE_MARGIN - rect.right;
    } else if (rect.left < box.left + EDGE_MARGIN) {
      shift = box.left + EDGE_MARGIN - rect.left;
    }
    if (shift !== 0) {
      panel.style.transform = "translateX(calc(-50% + " + shift + "px))";
    }
  }

  // <details>'s "toggle" event does not bubble, but it does fire during the
  // capturing phase, so one listener on the document reaches every popup —
  // including ones the board or player pages render inside a loop.
  document.addEventListener(
    "toggle",
    function (event) {
      var details = event.target;
      if (
        details.tagName === "DETAILS" &&
        details.getAttribute("name") === "popup" &&
        details.open
      ) {
        reposition(details);
      }
    },
    true
  );
})();

// Today's "N players not ranked" toggle. The link's href already flips
// ?benched=0/1 and works with no script at all — see today.go and
// today.html's "bench-section" block. This only swaps that section in
// place instead of reloading the page, the same "?partial=1" trick app.js
// already uses for the ⌘K search overlay.
(function () {
  "use strict";

  document.addEventListener("click", function (event) {
    var link = event.target.closest(".bench-toggle a.toggle");
    if (!link) return;

    var section = link.closest(".bench-section");
    if (!section) return; // Markup changed underneath us; fall back to a real navigation.

    event.preventDefault();
    var url = link.href;

    fetch(url + (url.indexOf("?") === -1 ? "?" : "&") + "partial=1")
      .then(function (response) { return response.ok ? response.text() : null; })
      .then(function (html) {
        if (html === null) {
          window.location.href = url;
          return;
        }
        var wrapper = document.createElement("div");
        wrapper.innerHTML = html;
        var replacement = wrapper.querySelector(".bench-section");
        if (!replacement) {
          window.location.href = url;
          return;
        }
        section.replaceWith(replacement);
        history.replaceState(null, "", url);
      })
      .catch(function () {
        // Pure enhancement: fall back to the link's real navigation.
        window.location.href = url;
      });
  });
})();

// The ⌘K command palette. It exists only because opening an overlay on a
// keystroke, and moving a selection through it with arrow keys, cannot be
// done from HTML and CSS alone — everything else about search does not
// need this file. The topbar's search link (see topbar.html) already goes
// to a working search page with no script at all — /search signed in,
// /share/<slug>/search on the read-only view; all this does is fetch that
// same route's results — "?partial=1" asks the server for just the list,
// not a second page — into an overlay instead of navigating to it, and let
// the arrow keys and Enter move through what comes back. Absent, disabled,
// or failing to load, the link still works exactly as before.
(function () {
  "use strict";

  var overlay = document.getElementById("search-overlay");
  if (!overlay) return; // No search on this page — signed out only.

  var button = document.querySelector(".search-btn");
  var input = overlay.querySelector(".search-overlay-input");
  var results = overlay.querySelector(".search-overlay-results");
  // Set from chrome's SearchPath — "/search" signed in, "/share/<slug>/search"
  // on the read-only view — so this file never hardcodes which one applies.
  var searchPath = overlay.dataset.searchPath;

  // Guards against a slow request for an earlier keystroke landing after a
  // faster one for a later keystroke — without this, typing quickly can
  // show results for a query that is no longer in the box.
  var requestID = 0;

  function fetchResults(query) {
    var thisRequest = ++requestID;
    fetch(searchPath + "?partial=1&q=" + encodeURIComponent(query))
      .then(function (response) { return response.ok ? response.text() : ""; })
      .then(function (html) {
        if (thisRequest === requestID) results.innerHTML = html;
      })
      .catch(function () {
        // Pure enhancement: leave whatever results are already showing
        // rather than replacing them with an error.
      });
  }

  function open() {
    button.setAttribute("aria-expanded", "true");
    overlay.hidden = false;
    input.value = "";
    fetchResults("");
    input.focus();
  }

  function close() {
    overlay.hidden = true;
    button.setAttribute("aria-expanded", "false");
    button.focus();
  }

  var debounce;
  input.addEventListener("input", function () {
    var query = input.value;
    clearTimeout(debounce);
    debounce = setTimeout(function () { fetchResults(query); }, 120);
  });

  button.addEventListener("click", function (event) {
    event.preventDefault(); // The link still has a real href; only override it once script has run.
    open();
  });

  document.addEventListener("keydown", function (event) {
    if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "k") {
      event.preventDefault();
      overlay.hidden ? open() : close();
      return;
    }
    if (!overlay.hidden && event.key === "Escape") close();
  });

  overlay.addEventListener("click", function (event) {
    if (event.target === overlay) close(); // The backdrop, not the box inside it.
  });

  // Arrow keys move a .current marker among the rendered result links;
  // Enter follows whichever one currently carries it, or the first result
  // when nothing has been highlighted yet.
  input.addEventListener("keydown", function (event) {
    var links = results.querySelectorAll("a");
    if (!links.length) return;

    if (event.key !== "ArrowDown" && event.key !== "ArrowUp" && event.key !== "Enter") return;
    event.preventDefault();

    var current = results.querySelector("a.current");
    var index = current ? Array.prototype.indexOf.call(links, current) : -1;

    if (event.key === "Enter") {
      (current || links[0]).click();
      return;
    }

    index = event.key === "ArrowDown" ? (index + 1) % links.length : (index - 1 + links.length) % links.length;
    if (current) current.classList.remove("current");
    links[index].classList.add("current");
    links[index].scrollIntoView({ block: "nearest" });
  });
})();
