// Independent, self-contained enhancements, each an IIFE with its own
// header comment. Every rule from AGENTS.md's JavaScript section applies
// to all of them: no build step, and each is strictly additive — the
// feature it touches has a working, server-rendered form already, and this
// file only adds a shortcut or a nicety on top of it.
//
// The fetching and swapping is htmx's, declared where the markup is: every
// link and form is boosted (base.html), and the few places that swap
// something other than the body say so in their own template. What is here
// is what htmx cannot express — a keystroke, a dialog, the clipboard, the
// geometry of a popup — and, in the last block, the handful of things a
// body swap leaves undone.

// The one thing in this file outside an IIFE: a registry the last block
// runs after htmx has replaced the document's body.
//
// An enhancement that delegates from the document needs nothing here — its
// listener outlives every element it will ever fire for. These are the ones
// that hold on to a particular element, which after a swap is a node that
// is no longer in the page. A function registered here runs now and again
// after every body swap, so it has to be safe to run more than once: look
// the elements up each time, and register document-level listeners outside
// it.
// Set by the last block: re-renders the page at the current URL in place,
// for anything that changed the account underneath it. Null until then, and
// null for good where htmx did not load, so every caller has to have a way
// of coping without it.
var refreshPage = null;

var onPageChange = (function () {
  "use strict";
  var fns = [];
  function register(fn) {
    fns.push(fn);
    fn();
  }
  register.rerun = function () {
    for (var i = 0; i < fns.length; i++) {
      try {
        fns[i]();
      } catch (err) {
        // One enhancement failing must not cost the reader the others.
      }
    }
  };
  return register;
})();

// Progressive enhancements for native <details> controls: dismiss the bar's
// menus on outside clicks, close any popup on Esc, and nudge informational
// popups back on screen. Opening, summary-click closing and exclusivity
// still work without JS. The bar's menus keep their fixed CSS positioning.
(function () {
  "use strict";

  // Keep clicks on menu links and summaries native. Other disclosures,
  // such as result details and admin diagnostics, are not dismissible menus.
  document.addEventListener("click", function (event) {
    document.querySelectorAll('details[name="menu-group"][open]').forEach(function (menu) {
      if (!menu.contains(event.target)) {
        menu.open = false;
      }
    });
  });

  // The <details> that open over the page: the bar's menus, the Help
  // panel, and the popups on board cells. Other disclosures open
  // in the flow of the page and stay as the reader left them.
  var POPUPS = 'details[name="menu-group"][open], details[name="about"][open], details[name="popup"][open]';

  // Esc closes one popup per press, the innermost first (About, opened from
  // the account menu, takes the menu with it — see below). Later
  // in document order is deeper, since a nested one follows its parent.
  // Focus that was inside goes back to the summary, so it is not lost to
  // the top of the page with the panel it was in.
  document.addEventListener("keydown", function (event) {
    if (event.key !== "Escape" || event.defaultPrevented) return;
    var open = document.querySelectorAll(POPUPS);
    if (!open.length) return;
    var details = open[open.length - 1];
    var hadFocus = details.contains(document.activeElement);
    details.open = false;
    if (hadFocus) details.querySelector(":scope > summary").focus();
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

// About's close button.
//
// The About card is a <details>, and what closes it with no script is the
// dim behind it, which is its own summary stretched over the page. A card
// with no visible way out reads as stuck, so this shows the button the card
// carries hidden and has it close the card. Absent, disabled or failing to
// load, the button stays hidden and the dim still closes the card.
(function () {
  "use strict";

  document.addEventListener("click", function (event) {
    var button = event.target.closest && event.target.closest(".about-close");
    if (!button) return;
    var about = button.closest("details.about");
    if (!about) return;
    about.open = false;
    about.querySelector(":scope > summary").focus({ preventScroll: true });
  });

  // About opened from a menu closes the menu when it closes, by whatever
  // means — the button, the dim or Esc. app.css hid the menu while the card
  // was up, so the reader is back on the page, not in a menu they left.
  // Focus that would be lost with the menu goes to the menu's own summary.
  // Absent, the menu reappears when the card closes and closes as any
  // menu does.
  document.addEventListener(
    "toggle",
    function (event) {
      var about = event.target;
      if (about.tagName !== "DETAILS" || !about.classList.contains("about") || about.open) return;
      var menu = about.parentElement && about.parentElement.closest("details.menu[open]");
      if (!menu) return;
      var hadFocus = menu.contains(document.activeElement);
      menu.open = false;
      if (hadFocus) menu.querySelector(":scope > summary").focus({ preventScroll: true });
    },
    true
  );
  onPageChange(function () {
    document.querySelectorAll(".about-close").forEach(function (button) { button.hidden = false; });
  });
})();

// The pill row keeps the pill for this page in view.
//
// A roster of fourteen, or the admin area's five tabs on a phone, is a row wider
// than the window, and it arrives scrolled to its left end — so the player
// you asked for can be off the right edge of the row that is meant to show
// you where you are. This scrolls the row, and only the row, to put the
// current pill in the middle of it, once per page. scrollTo on the row
// rather than scrollIntoView on the pill, which would scroll the page as
// well. Absent, disabled or failing to load, the row is the same row,
// scrolled to its start, and every pill in it is still a link.
(function () {
  "use strict";

  onPageChange(function () {
    document.querySelectorAll(".pills, .admin-tabs").forEach(function (row) {
      var pill = row.querySelector(".pill.on, .admin-tab.on");
      if (!pill || row.scrollWidth <= row.clientWidth) return;
      var left = pill.offsetLeft - (row.clientWidth - pill.offsetWidth) / 2;
      row.scrollTo({ left: Math.max(0, left), behavior: "instant" });
    });
  });
})();

// The ⌘K command palette. It exists only because opening an overlay on a
// keystroke, and moving a selection through it with arrow keys, cannot be
// done from HTML and CSS alone — everything else about search does not
// need this file. The bar's search link (see topbar.html) already goes
// to a working search page with no script at all — /search signed in,
// /share/<slug>/search on the read-only view. The fetching is htmx's, as
// attributes on the overlay's input: each pause in the typing asks that
// same route for just its results and puts them in the box. What is here
// is the rest: showing the overlay on the keystroke, telling htmx it has
// been shown so the empty query's results appear, and letting the arrow
// keys and Enter move through what comes back. Absent, disabled, or
// failing to load, the link still works exactly as before.
(function () {
  "use strict";

  // Looked up again after every page switch: the overlay on the page now is
  // not the node this closed over when the file first ran. Null on a page
  // with no search — signed out only.
  var overlay = null;
  var button, input, results;

  // The design's long placeholder clips on a phone, where it is just
  // "Search"; see topbar.html.
  var narrow = window.matchMedia("(max-width: 860px)");

  function open() {
    button.setAttribute("aria-expanded", "true");
    overlay.hidden = false;
    input.value = "";
    if (!input.dataset.placeholderWide) input.dataset.placeholderWide = input.placeholder;
    input.placeholder = narrow.matches ? input.dataset.placeholderNarrow : input.dataset.placeholderWide;
    // The input's hx-trigger names this event; htmx fetches the results for
    // an empty query, as it would for a keystroke.
    if (window.htmx) htmx.trigger(input, "search-open");
    input.focus();
  }

  function close() {
    overlay.hidden = true;
    button.setAttribute("aria-expanded", "false");
    button.focus();
  }

  // The shortcut belongs to the document, not to the overlay, so it is
  // registered once and reads whichever overlay is on the page when it fires.
  document.addEventListener("keydown", function (event) {
    if (!overlay) return;
    if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "k") {
      event.preventDefault();
      overlay.hidden ? open() : close();
      return;
    }
    if (!overlay.hidden && event.key === "Escape") close();
  });

  onPageChange(function () {
    overlay = document.getElementById("search-overlay");
    button = document.querySelector(".bar-search");
    if (!overlay || !button) {
      overlay = null;
      return;
    }
    input = overlay.querySelector(".search-overlay-input");
    results = overlay.querySelector(".search-overlay-results");

    button.addEventListener("click", function (event) {
      event.preventDefault(); // The link still has a real href; only override it once script has run.
      open();
    });

    overlay.addEventListener("click", function (event) {
      if (event.target === overlay) close(); // The backdrop, not the box inside it.
    });
    overlay.querySelector(".search-overlay-close").addEventListener("click", close);

    // The clear button is a reset, which empties the field but raises no
    // input event; ask for the empty query's results as typing would.
    var form = overlay.querySelector("form");
    form.addEventListener("reset", function () {
      setTimeout(function () {
        if (window.htmx) htmx.trigger(input, "search-open");
        input.focus();
      }, 0);
    });
    form.addEventListener("submit", function (event) { event.preventDefault(); });

    // A lone result is the one Enter takes, so it shows as chosen.
    results.addEventListener("htmx:afterSwap", function () {
      var links = results.querySelectorAll("a");
      if (links.length === 1) links[0].classList.add("current");
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
  });
})();

// Copy buttons: the share link on the admin area's Settings, a fresh set of
// recovery codes on a reader's own.
//
// Copying cannot exist without a script at all: a clipboard cannot be written
// to from markup. So the button is in the markup hidden, carrying what it
// copies and the word it says once it has, and this shows it only where a
// clipboard can be written — a page served over plain http to anything but
// localhost has no navigator.clipboard, and gets no button instead of a dead
// one.
(function () {
  "use strict";

  function mount() {
    if (!navigator.clipboard || !navigator.clipboard.writeText) return;
    document.querySelectorAll("button[data-copy-text][hidden]").forEach(function (button) {
      button.hidden = false;
    });
  }

  document.addEventListener("click", function (event) {
    var button = event.target.closest("button[data-copy-text]");
    if (!button) return;
    var label = button.querySelector("span") || button;
    var was = label.textContent;
    navigator.clipboard.writeText(button.dataset.copyText).then(function () {
      label.textContent = button.dataset.copiedLabel;
      // Said, then taken back: a button stuck reading "Copied" looks
      // pressed rather than like one that can be pressed again.
      clearTimeout(button._revert);
      button._revert = setTimeout(function () { label.textContent = was; }, 2000);
    }).catch(function () {
      // Refused — a permissions policy, or a window that is not focused.
      // What it copies is still on the page to read, so say nothing.
    });
  });

  onPageChange(mount);
  document.addEventListener("htmx:afterSettle", mount);
})();

// The live stream and the browser's own page cache.
//
// Today and the board hold a stream open (sse-connect on <main>, see
// base.html), and htmx's SSE extension opens and closes it as the region
// comes and goes. What the extension cannot see is the browser keeping a
// page that was navigated away from — a share link typed in, a sign-in
// redirect — alive in its back-forward cache with the stream still open.
// Six such pages and the browser has no connection left for the next
// request to this host; the seventh visit to Today never loads. So every
// stream is closed the moment its page is hidden, and a page brought back
// from that cache, stale and now streamless, is loaded again: the old
// switcher reloaded for an entry older than anything it drew, for the same
// reason.
(function () {
  "use strict";

  if (!window.htmx || !window.EventSource) return;

  var open = [];
  // The extension makes every source through this hook, which is what it
  // is for.
  var create = htmx.createEventSource;
  htmx.createEventSource = function (url) {
    var source = create ? create(url) : new EventSource(url, { withCredentials: true });
    open.push(source);
    return source;
  };

  window.addEventListener("pagehide", function () {
    open.forEach(function (source) { source.close(); });
    open = [];
  });
  window.addEventListener("pageshow", function (event) {
    if (event.persisted && document.querySelector("[sse-connect]")) window.location.reload();
  });
})();

// What htmx leaves to this file.
//
// Every link and form in the application is boosted — <body hx-boost="true">
// in base.html — so following one fetches the page and puts its body in
// place of this one. The whole body, as docs/decisions.md asks: an error
// page arrives without the bar, a theme link arrives with the theme, and a
// page reached this way is the server's own rendering of that URL. htmx
// does the fetching, the swapping, the history and the scroll. Seven things
// are outside its reach and live here.
(function () {
  "use strict";

  if (!window.htmx || !window.DOMParser) return; // Without them every link is still a link.

  // 1. The attributes the server decides for the whole document — the
  // reader's language and their theme — sit on <html>, which
  // a body swap never touches. Following a theme link is an ordinary
  // navigation, so this is how the theme actually changes.
  document.addEventListener("htmx:beforeSwap", function (event) {
    var detail = event.detail;
    if (detail.target !== document.body || !detail.xhr) return;
    var incoming = new DOMParser().parseFromString(detail.xhr.responseText, "text/html").documentElement;
    var root = document.documentElement;
    ["lang", "data-theme"].forEach(function (name) {
      var value = incoming.getAttribute(name);
      if (value === null) root.removeAttribute(name);
      else root.setAttribute(name, value);
    });
    // The page is the authority, so anything item 5 set ahead of this
    // reply is confirmed or corrected by the lines above, and there is
    // nothing left to put back.
    if (detail.shouldSwap !== false) settleAhead(detail.xhr);
    // A page can arrive with a menu open — the board after a rule pressed
    // in its ranking menu (board.go's ruleLink). htmx puts the new body in
    // before it takes the old one out, and a <details name> opening while
    // another of its name is open shuts itself, as an accordion does. So
    // the departing one closes first. That press swaps with no cross-fade
    // (item 6), so nothing is drawn in between; and the copy htmx keeps for
    // Back, taken after this, has the menu shut, as a page come back to
    // should.
    if (detail.shouldSwap !== false) {
      incoming.querySelectorAll("details[name][open]").forEach(function (arriving) {
        document.querySelectorAll("details[name][open]").forEach(function (d) {
          if (d.getAttribute("name") === arriving.getAttribute("name")) d.open = false;
        });
      });
    }
  });

  // And the address. htmx (2.0.10) decides whether a boosted swap pushes
  // the URL by a flag on the element that was pressed, read once the reply
  // is in — and forgets that flag when the element leaves the page, which it
  // does if the list it was in is swapped while its request is in flight: a
  // search hit pressed as the results refresh, a player pressed as a live
  // update lands. The page then changes and the address does not. So the
  // decision is made here, while the element is still in the page: a
  // boosted request that says nothing of its own about the address pushes
  // wherever the server ends up sending it, which is what htmx would have
  // decided.
  document.addEventListener("htmx:beforeRequest", function (event) {
    var detail = event.detail;
    var config = detail.requestConfig;
    var etc = detail.etc;
    if (!config || !config.boosted || !etc || etc.push || etc.replace) return;
    if (config.elt.closest("[hx-push-url], [hx-replace-url]")) return;
    etc.push = "true";
  });

  // 2. Focus. The element that was pressed is gone with the body it was in,
  // and focus left on a departing node lands on the body, which puts a
  // keyboard back at the start of the page with nothing to say it moved. A
  // real navigation resets focus too — this only aims it better.
  function focusMain() {
    var main = document.getElementById("main");
    if (!main) return;
    // tabindex only for as long as the focus lasts, so the region never
    // becomes a tab stop of its own.
    main.setAttribute("tabindex", "-1");
    // preventScroll, or focusing the region scrolls it to the top of the
    // window — and the bar above it is sticky, so every page switched in
    // arrived with the bar already scrolled away and the reader 56px down a
    // page they had not touched.
    main.focus({ preventScroll: true });
    main.addEventListener("blur", function () { main.removeAttribute("tabindex"); }, { once: true });
  }

  // Focus placed after a swap is for the keyboard: the next Tab or arrow
  // moves on from where the reader is. A press with a mouse or a finger
  // needs the same place but not the ring — Safari draws one for any focus
  // a script sets, and a pill left circled after a click reads as a fault.
  // So the last kind of input decides, and focus set after a pointer press
  // is marked quiet until it leaves (see .focus-quiet in app.css).
  var pointerLast = false;
  document.addEventListener("pointerdown", function () { pointerLast = true; }, true);
  document.addEventListener("keydown", function () { pointerLast = false; }, true);
  function placeFocus(el) {
    if (pointerLast) {
      el.classList.add("focus-quiet");
      el.addEventListener("blur", function () { el.classList.remove("focus-quiet"); }, { once: true });
    }
    el.focus({ preventScroll: true, focusVisible: !pointerLast });
  }

  // Where focus goes instead, for the three controls that are a place in the
  // page rather than a step out of it. Each returns false if the page that
  // arrived does not have what it was looking for, and the main region takes
  // over. link is the anchor that was pressed, detached now but intact.
  function focusRule(link) {
    var ranking = link.closest(".ranking-panel");
    if (ranking) {
      // The board arrives with the menu open (board.go's ruleLink), and the
      // reader may well have a second rule to set, so the row they chose
      // keeps the focus.
      var index = Array.prototype.indexOf.call(ranking.querySelectorAll("a"), link);
      return function () {
        var menu = document.querySelector("details.ranking");
        if (!menu || !menu.open) return false;
        var row = menu.querySelectorAll(".ranking-panel a")[index];
        if (row) placeFocus(row);
        return true;
      };
    }
    if (link.closest(".pill-row, .admin-tabs")) {
      // A pill or a step arrow beside the roster, or one of the admin
      // area's tabs. The one for the page that arrived keeps the focus, so
      // the next arrow key or Tab moves on from where the reader is rather
      // than from the top of the page.
      return function () {
        var pill = document.querySelector(".pills .pill.on, .admin-tabs .admin-tab.on");
        if (!pill) return false;
        placeFocus(pill);
        return true;
      };
    }
    if (link.closest(".bar-pages, .bar-menu")) {
      // A page in the bar: the same page in the new bar, so the next Tab is
      // the next view rather than the top of the page. The phone's menu
      // arrives shut, which is right — the reader asked for a page — so
      // there the capsule that opens it keeps the focus instead.
      var href = link.getAttribute("href");
      return function () {
        var pages = document.querySelectorAll(".bar-pages a[href]");
        for (var i = 0; i < pages.length; i++) {
          if (pages[i].getAttribute("href") === href && pages[i].getClientRects().length) {
            placeFocus(pages[i]);
            return true;
          }
        }
        var capsule = document.querySelector(".bar-menu > summary");
        if (capsule && capsule.getClientRects().length) {
          placeFocus(capsule);
          return true;
        }
        return false;
      };
    }
    return null;
  }

  // 3. The registry, and focus, after every body swap — a link followed, a
  // form posted, Back or Forward, or a refresh asked for below.
  function settled(link) {
    onPageChange.rerun();
    var rule = link ? focusRule(link) : null;
    if (!rule || !rule()) focusMain();
  }
  document.addEventListener("htmx:afterSettle", function (event) {
    if (event.detail.target !== document.body) return;
    // requestConfig.elt is the element that made the request; detail.elt is
    // only the one the event was raised on, which for a body swap is the body.
    var config = event.detail.requestConfig;
    var pressed = config && config.elt;
    settled(pressed && pressed.tagName === "A" ? pressed : null);
  });
  // Back and Forward: htmx puts the page back from its cache, or fetches it
  // again, and either way the enhancements on it need running and focus needs
  // a home. The scroll position is htmx's to restore, and it does.
  document.addEventListener("htmx:historyRestore", function () { settled(null); });

  // For anything that changed the page underneath it — the enrolment dialog
  // finishing, say — without itself being a navigation.
  refreshPage = function () {
    htmx.ajax("GET", window.location.href, { target: "body", swap: "innerHTML" });
  };

  // 4. A request that could not be sent at all — the network is gone — is
  // handed back to the browser as the navigation that was asked for, which
  // is what would have happened anyway. A page that comes back is swapped
  // whatever its status: the config in base.html has htmx treat a 404 as a
  // page, which is what the error frame is.
  document.addEventListener("htmx:sendError", function (event) {
    var config = event.detail.requestConfig;
    var pressed = config && config.elt;
    if (pressed && pressed.tagName === "FORM") {
      pressed.submit();
      return;
    }
    var path = event.detail.pathInfo && event.detail.pathInfo.requestPath;
    if (path) window.location.href = path;
  });

  // 5. The instant half of the theme, whose whole effect is an attribute on
  // <html>. A theme link is a link back to this URL with ?theme= set —
  // urlWith in chrome.go builds it — and the page that comes back carries
  // the new value, which item 1 copies across. That is correct and, on a
  // local network, quick; but the reader pressed a segment and should see
  // the page turn over at the press, not when the reply lands. So the
  // attribute is set here, at the press, off the parameter the link already
  // carries, and the reply confirms it: a page that arrives overwrites it
  // (item 1), and a request that ends with no page — the network gone, a
  // 204, a press superseded by the next — puts back what was there. The
  // language stays with the page, because an <html lang> claiming a
  // language its words are not yet in misleads exactly the reader that
  // consults it. Without script, each link is still the link.
  var ahead = {
    theme: { attr: "data-theme", values: ["system", "light", "dark"] }
  };
  // What was set ahead of a reply still in flight: { attr, was, xhr }.
  var setAhead = [];
  function settleAhead(xhr) {
    setAhead = setAhead.filter(function (a) { return a.xhr !== xhr; });
  }
  document.addEventListener("htmx:beforeRequest", function (event) {
    var config = event.detail.requestConfig;
    var link = config && config.elt;
    if (!config.boosted || !link || link.tagName !== "A") return;
    var params;
    try { params = new URL(link.href, window.location.href).searchParams; } catch (e) { return; }
    var root = document.documentElement;
    Object.keys(ahead).forEach(function (name) {
      var rule = ahead[name];
      var value = params.get(name);
      if (value === null || rule.values.indexOf(value) < 0) return;
      // A link carries the whole query, so a link on a page already at
      // ?theme=dark says so too; only what actually changes is set.
      var was = root.getAttribute(rule.attr);
      if (value === was) return;
      root.setAttribute(rule.attr, value);
      setAhead.push({ attr: rule.attr, was: was, xhr: event.detail.xhr });
    });
  });
  // Fires for every request however it ended, after any swap it caused
  // — so an entry still here is one no page confirmed.
  document.addEventListener("htmx:afterRequest", function (event) {
    var xhr = event.detail.xhr;
    var root = document.documentElement;
    setAhead = setAhead.filter(function (a) {
      if (a.xhr !== xhr) return true;
      if (a.was === null) root.removeAttribute(a.attr);
      else root.setAttribute(a.attr, a.was);
      return false;
    });
  });

  // 6. Reduced motion. Every swap is a view transition (the config in
  // base.html), and htmx has no attribute for a reader who has asked their
  // system for less of that. The stylesheet zeroes the animation, but the
  // browser still pauses to take its pictures; cancelling here, before it
  // starts, is the version that costs nothing. Read at each swap rather
  // than once, so a setting changed mid-session is honoured. Without
  // script there is no transition to cancel.
  //
  // Nor is there one for a press in the board's ranking menu. The menu is
  // open on both sides of that swap, and the cross-fade is drawn from
  // pictures of the page, which Safari takes without the frosted glass: the
  // panel went clear for the length of the fade and frosted again after,
  // which read as the page reloading. The swap is instant instead.
  document.addEventListener("htmx:beforeTransition", function (event) {
    if (window.matchMedia && window.matchMedia("(prefers-reduced-motion: reduce)").matches) {
      event.preventDefault();
      return;
    }
    var config = event.detail && event.detail.requestConfig;
    if (config && config.elt && config.elt.closest && config.elt.closest(".ranking-panel")) {
      event.preventDefault();
    }
  });

  // 7. A link that stays on the page keeps the reader where they are.
  // htmx scrolls a boosted swap to the top, which is right for a step to
  // another page and wrong for a change to this one: another player in the
  // roster, the range or a rule on the board, a pair to compare, a month
  // from the season, the grid's window. Those swap the page and leave the
  // scroll alone. "The same page" is the same view — the same path, or
  // the same kind of page under it (/players/…, /puzzle/…, an admin
  // section's players) — read off the addresses either side of the swap,
  // so no link has to say so itself.
  function pageOf(href) {
    var path;
    try { path = new URL(href, window.location.href).pathname; } catch (e) { return null; }
    path = path.replace(/^\/share\/[^/]+/, "").replace(/\/+$/, "") || "/today";
    var parts = path.split("/");
    if (parts[1] === "players" || parts[1] === "puzzle") return parts[1];
    if (parts[1] === "admin" && parts[2] === "players") return "admin/players";
    return path;
  }
  document.addEventListener("htmx:beforeSwap", function (event) {
    var detail = event.detail;
    if (detail.target !== document.body || !detail.xhr || !detail.requestConfig || !detail.requestConfig.boosted) return;
    var landed = detail.xhr.responseURL || (detail.pathInfo && detail.pathInfo.finalRequestPath);
    if (!landed || pageOf(landed) !== pageOf(window.location.href)) return;
    detail.swapOverride = "innerHTML show:none";
  });
})();
