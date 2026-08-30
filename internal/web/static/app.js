// Keeps a popup on screen. Nothing here opens or closes a popup, or makes
// one exclusive with another — that is all native <details name="popup">
// behaviour (see base.html) and works with this file absent, disabled, or
// failing to load. All this does is notice, once a popup has opened where
// CSS put it, that there was no room there, and nudge it back into view. A
// topbar menu (name="topbar-menu") is not covered: it has one fixed spot
// and anchors itself by hand in app.css.
(function () {
  "use strict";

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
