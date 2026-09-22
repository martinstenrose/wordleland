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

// Progressive enhancements for native <details> controls: dismiss topbar
// menus on outside clicks, close any popup on Esc, and nudge informational
// popups back on screen. Opening, summary-click closing and exclusivity
// still work without JS. Topbar menus keep their fixed CSS positioning.
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

  // The <details> that open over the page: the topbar menus and drawer,
  // the Help panel, and the popups on board cells. Other disclosures open
  // in the flow of the page and stay as the reader left them.
  var POPUPS = 'details[name="menu-group"][open], details[name="about"][open], details[name="popup"][open]';

  // Esc closes one popup per press, the innermost first: Help opened from
  // inside the drawer goes, and the drawer stays for the next press. Later
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

// The ⌘K command palette. It exists only because opening an overlay on a
// keystroke, and moving a selection through it with arrow keys, cannot be
// done from HTML and CSS alone — everything else about search does not
// need this file. The topbar's search link (see topbar.html) already goes
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

  function open() {
    button.setAttribute("aria-expanded", "true");
    overlay.hidden = false;
    input.value = "";
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
    button = document.querySelector(".search-btn");
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

// Raising an outcome to the middle of the screen.
//
// A change that lands somewhere else — a confirmation mailed to an address
// you are not reading, a password that just signed you out everywhere — is
// worth more than a line at the top of a page you were not looking at. The
// design asks for that as a centred panel, and this is it.
//
// The whole of the feature is already on the page without this file: the
// action posts, the server redirects, and the outcome renders as a note where
// notes go. Absent, disabled, or failing to load, that note is what a reader
// gets, and it says the same thing. Only the outcome of something done is
// raised — a rejected form's message stays beside the field it is about,
// which is where it can actually be acted on.
//
// Esc, the backdrop and the button all close it. It is marked up as a dialog
// here rather than in the template because only here is it one: focus moves
// into it, and comes back to where it was when it closes.
(function () {
  "use strict";

  // A page switched in may carry an outcome of its own to raise.
  onPageChange(function () {
    var note = document.querySelector("[data-raise]");
    if (!note) return;

    var returnTo = document.activeElement;

    var backdrop = document.createElement("div");
    backdrop.className = "raised-backdrop";

    var panel = document.createElement("div");
    panel.className = "raised-panel";
    panel.setAttribute("role", "dialog");
    panel.setAttribute("aria-modal", "true");

    var body = document.createElement("p");
    body.className = "raised-body";
    body.textContent = note.textContent.trim();

    var close = document.createElement("button");
    close.type = "button";
    // The dialog's one control, and it does the thing: the filled accent,
    // like any other. See the controls block in app.css.
    close.className = "btn raised-close";
    // The template carries the word, so this file holds no copy of its own and
    // needs no knowledge of which language the page is in.
    close.textContent = note.dataset.raise;

    panel.appendChild(body);
    panel.appendChild(close);
    backdrop.appendChild(panel);

    // The note goes, rather than staying behind the panel saying the same thing
    // twice to a screen reader.
    note.remove();
    document.body.appendChild(backdrop);
    close.focus();

    function dismiss() {
      backdrop.remove();
      document.removeEventListener("keydown", onKey);
      if (returnTo && document.contains(returnTo) && returnTo.focus) returnTo.focus();
    }

    function onKey(event) {
      if (event.key === "Escape") dismiss();
    }

    close.addEventListener("click", dismiss);
    backdrop.addEventListener("click", function (event) {
      if (event.target === backdrop) dismiss();
    });
    document.addEventListener("keydown", onKey);
  });
})();

// The share slug's copy button.
//
// Copying cannot exist without a script at all: a clipboard cannot be written
// to from markup. That is why the button is built here rather than rendered
// and then wired up — it exists exactly when it works, and a page served over
// plain http to anything but localhost, which has no navigator.clipboard,
// gets no button instead of a dead one.
//
// Rotating the slug is not here. It is three links and a form that work
// with no script, and the attributes on the share section in
// admin_settings.html have htmx swap the card in place at each step. What
// that leaves for this file is that a rotation replaces the slug, and the
// button carries the address of the slug it was built for — so the button
// is built again after any swap that is not the whole body, the registry
// covering the rest.
(function () {
  "use strict";

  // Rebuilt after every swap: the row is new markup carrying a new address.
  function mountCopy() {
    var row = document.querySelector("[data-copy]");
    if (!row) return; // No link yet, or no APP_URL to make it absolute with.
    if (row.querySelector("button")) return; // Already mounted on this row.
    if (!navigator.clipboard || !navigator.clipboard.writeText) return;

    var button = document.createElement("button");
    button.type = "button";
    // Filled accent: copying the link is safe and is what somebody is here
    // for. The control beside it rotates the slug and is toned for that.
    button.className = "btn";
    button.textContent = row.dataset.copyLabel;
    // First in the row: copying is what somebody is usually here to do, and
    // the control beside it rotates the slug for everybody in the group.
    row.insertBefore(button, row.firstChild);

    var revert;
    button.addEventListener("click", function () {
      navigator.clipboard.writeText(row.dataset.copy).then(function () {
        button.textContent = row.dataset.copiedLabel;
        // Said, then taken back: a button stuck reading "Copied" is a button
        // that looks pressed rather than one that can be pressed again.
        clearTimeout(revert);
        revert = setTimeout(function () {
          button.textContent = row.dataset.copyLabel;
        }, 2000);
      }).catch(function () {
        // Refused — a permissions policy, or a window that is not focused.
        // The slug is still on the page to read, so say nothing.
      });
    });
  }

  onPageChange(mountCopy);
  document.addEventListener("htmx:afterSettle", function (event) {
    if (event.detail.target !== document.body) mountCopy();
  });
})();

// A page that is really a step, shown over the page that asked for it.
//
// Setting up a two-factor key is one screen with a form on it, and the
// settings screen sends people to it. As a page of its own it takes the whole
// window for a job that belongs to the screen behind it, and coming back
// means a second navigation — so with a script it opens over that screen
// instead, and the whole exchange happens there: the code, the password, a
// rejected code, and finally the recovery codes, which are shown once and are
// worth showing where the reader is already looking.
//
// The link is a link the whole time. With this file absent, disabled, or
// failing at any step, following it goes to the page, which is the page this
// is fetching anyway — the server renders the same card either way, and asks
// only to be spared the frame around it.
//
// It is a dialog and is built as one here rather than marked up as one in the
// template, for the reason the raised outcome above is: only here is it true.
// Focus moves in, is held inside while it is open, and goes back to the
// control that opened it when it closes.
//
// This is the one enhancement that keeps its own requests rather than
// handing them to htmx, and htmx never sees inside it. htmx wants a
// dialog's target on a container the card lands in, and the card is a
// template shared with the standalone page, whose links must go on
// navigating the page — so each of them would have needed attributes undoing
// the container's, and the form's Cancel sits inside the form. Instead the
// card is inserted here and never handed to htmx.process, so nothing in it
// is boosted: every press inside is this file's, and the whole exchange
// stays where it was written. The link that opens it says hx-boost="false"
// in settings.html so htmx leaves that press to this file too.
(function () {
  "use strict";

  if (!window.fetch) return; // Without it the link is still the whole feature.

  var FOCUSABLE = 'a[href], button:not([disabled]), input:not([disabled]), [tabindex]:not([tabindex="-1"])';

  var backdrop = null;
  var panel = null;
  var openedBy = null;
  // The word for the close button, taken from the link that opened the
  // dialog so this file holds no copy of it in any language.
  var closeLabel = "";
  // Whether anything inside the dialog changed the account, and so whether
  // the page behind it is now out of date.
  var changed = false;

  function focusables() {
    return Array.prototype.filter.call(panel.querySelectorAll(FOCUSABLE), function (el) {
      return el.getClientRects().length;
    });
  }

  function onKey(event) {
    if (!backdrop) return;
    if (event.key === "Escape") {
      close();
      return;
    }
    if (event.key !== "Tab") return;

    // Held inside: without this, Tab walks out of a dialog into the page it
    // is drawn over, which for a password field is worse than untidy.
    var items = focusables();
    if (!items.length) return;
    var first = items[0];
    var last = items[items.length - 1];
    if (event.shiftKey && document.activeElement === first) {
      event.preventDefault();
      last.focus();
    } else if (!event.shiftKey && document.activeElement === last) {
      event.preventDefault();
      first.focus();
    }
  }

  function close() {
    if (!backdrop) return;
    backdrop.remove();
    backdrop = null;
    panel = null;
    document.removeEventListener("keydown", onKey);
    document.documentElement.style.overflow = "";
    if (openedBy && document.contains(openedBy) && openedBy.focus) openedBy.focus();

    // The account is not what it was: the badge, the code count and the
    // control's own wording all belong to the state that just changed.
    if (changed) {
      changed = false;
      if (refreshPage) refreshPage();
      else window.location.reload();
    }
  }

  // draw puts a fetched card in the panel and aims focus at its first field.
  // The card is the dialog itself — it is already a bordered, padded box —
  // rather than a card drawn inside a second one.
  function draw(html) {
    var wrapper = document.createElement("div");
    wrapper.innerHTML = html;
    var card = wrapper.querySelector("section");
    if (!card) return false;

    card.classList.add("modal-card");
    card.setAttribute("role", "dialog");
    card.setAttribute("aria-modal", "true");

    var shut = document.createElement("button");
    shut.type = "button";
    shut.className = "modal-close";
    // The template carries the word, so this file holds no copy of its own
    // and needs no knowledge of which language the page is in.
    shut.setAttribute("aria-label", closeLabel);
    shut.textContent = "\u00d7";
    shut.addEventListener("click", close);
    card.insertBefore(shut, card.firstChild);

    if (panel) panel.replaceWith(card);
    else backdrop.appendChild(card);
    panel = card;

    var items = focusables();
    // The close button is items[0] and is not what somebody came here to
    // use; the field after it is.
    (items[1] || items[0] || card).focus({ preventScroll: true });
    return true;
  }

  function open(url) {
    fetch(url + (url.indexOf("?") === -1 ? "?" : "&") + "partial=1", {
      credentials: "same-origin",
      headers: { Accept: "text/html" },
    })
      .then(function (response) { return response.ok ? response.text() : null; })
      .then(function (html) {
        if (html === null) {
          window.location.href = url;
          return;
        }
        backdrop = document.createElement("div");
        backdrop.className = "raised-backdrop";
        backdrop.addEventListener("click", function (event) {
          if (event.target === backdrop) close();
        });
        document.body.appendChild(backdrop);
        if (!draw(html)) {
          backdrop.remove();
          backdrop = null;
          window.location.href = url;
          return;
        }
        document.addEventListener("keydown", onKey);
        // The page behind must not scroll under a dialog drawn over it.
        document.documentElement.style.overflow = "hidden";
      })
      .catch(function () { window.location.href = url; });
  }

  // Should the body be swapped under an open dialog — a live update, say —
  // the dialog goes with it, and only the state kept here needs forgetting.
  document.addEventListener("htmx:afterSettle", function (event) {
    if (event.detail.target !== document.body || !backdrop) return;
    backdrop = null;
    panel = null;
    changed = false;
    document.removeEventListener("keydown", onKey);
    document.documentElement.style.overflow = "";
  });

  document.addEventListener("click", function (event) {
    if (event.defaultPrevented) return;
    if (event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) {
      return;
    }

    // A link inside the dialog is a way out of it — "back to settings", or
    // the button under the recovery codes. Both mean "done here", and the
    // page behind is the page they name.
    if (panel && panel.contains(event.target)) {
      var inside = event.target.closest("a[href]");
      if (inside) {
        event.preventDefault();
        close();
      }
      return;
    }

    var link = event.target.closest("a[data-modal]");
    if (!link) return;
    event.preventDefault();
    openedBy = link;
    closeLabel = link.dataset.modalClose || "";
    changed = false;
    open(link.href);
  });

  // Every form in the dialog stays in it: what comes back is either the same
  // card carrying its own error, or the next step of the same exchange.
  document.addEventListener("submit", function (event) {
    if (!panel || !panel.contains(event.target)) return;
    var form = event.target.closest("form");
    if (!form) return;
    event.preventDefault();

    var action = form.getAttribute("action") || window.location.pathname;
    // URLSearchParams, not the FormData itself: that posts multipart, which
    // Go's ParseForm does not read, and every field arrives empty.
    fetch(action + (action.indexOf("?") === -1 ? "?" : "&") + "partial=1", {
      method: "POST",
      body: new URLSearchParams(new FormData(form)),
      credentials: "same-origin",
      headers: { Accept: "text/html" },
    })
      .then(function (response) {
        return response.text().then(function (html) {
          return { html: html, ok: response.ok };
        });
      })
      .then(function (result) {
        // A rejected form comes back as the same card with its message on
        // it; a 200 is the step after this one, and the only step after this
        // one is the account having changed.
        if (result.ok) changed = true;
        if (!draw(result.html)) {
          window.location.href = action;
        }
      })
      .catch(function () { form.submit(); });
  });
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
// page arrives without the rail, a theme link arrives with the theme, and a
// page reached this way is the server's own rendering of that URL. htmx
// does the fetching, the swapping, the history and the scroll. Six things
// are outside its reach and live here.
(function () {
  "use strict";

  if (!window.htmx || !window.DOMParser) return; // Without them every link is still a link.

  // 1. The attributes the server decides for the whole document — the
  // reader's language, their theme, the rail's width — sit on <html>, which
  // a body swap never touches. Following a theme link is an ordinary
  // navigation, so this is how the theme actually changes.
  document.addEventListener("htmx:beforeSwap", function (event) {
    var detail = event.detail;
    if (detail.target !== document.body || !detail.xhr) return;
    var incoming = new DOMParser().parseFromString(detail.xhr.responseText, "text/html").documentElement;
    var root = document.documentElement;
    ["lang", "data-theme", "data-sidebar"].forEach(function (name) {
      var value = incoming.getAttribute(name);
      if (value === null) root.removeAttribute(name);
      else root.setAttribute(name, value);
    });
    // The page is the authority, so anything item 5 set ahead of this
    // reply is confirmed or corrected by the lines above, and there is
    // nothing left to put back.
    if (detail.shouldSwap !== false) settleAhead(detail.xhr);
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

  // Where focus goes instead, for the three controls that are a place in the
  // page rather than a step out of it. Each returns false if the page that
  // arrived does not have what it was looking for, and the main region takes
  // over. link is the anchor that was pressed, detached now but intact.
  function focusRule(link) {
    var ranking = link.closest(".ranking-panel");
    if (ranking) {
      // A board reached by following this link arrives with its menu shut,
      // which is right for a link. Here the menu was open and the reader may
      // well have a second rule to set.
      var index = Array.prototype.indexOf.call(ranking.querySelectorAll("a"), link);
      return function () {
        var menu = document.querySelector("details.ranking");
        if (!menu) return false;
        menu.open = true;
        var row = menu.querySelectorAll(".ranking-panel a")[index];
        if (row) row.focus({ preventScroll: true });
        return true;
      };
    }
    if (link.closest(".switcher-panel")) {
      // The roster, and the admin area's section bar. The reader asked for a
      // page, not for the list again, so the menu stays shut and the bar —
      // which is the control they just used — keeps the focus.
      return function () {
        var bar = document.querySelector("details.switcher > summary");
        if (!bar) return false;
        bar.focus({ preventScroll: true });
        return true;
      };
    }
    if (link.closest(".sidebar, .drawer")) {
      // A rail row: the same row in the new rail, so the next Tab is the next
      // view rather than the top of the page.
      var href = link.getAttribute("href");
      return function () {
        // The rail and the drawer render the same rows, and whichever of the
        // two this width does not use is hidden — focusing a hidden element
        // silently does nothing, which would leave focus on the body.
        var rows = document.querySelectorAll(".sidebar a[href], .drawer a[href]");
        for (var i = 0; i < rows.length; i++) {
          if (rows[i].getAttribute("href") === href && rows[i].getClientRects().length) {
            rows[i].focus({ preventScroll: true });
            return true;
          }
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

  // 5. The instant half of a control whose whole effect is an attribute on
  // <html>: the rail's width, and the theme. Each is a link back to this
  // URL with ?sidebar= or ?theme= set — urlWith in chrome.go is the one
  // place that builds them — and the page that comes back carries the new
  // value, which item 1 copies across. That is correct and, on a local
  // network, quick. But the rail's width transition runs on the node the
  // swap is about to throw away, and the node that replaces it arrives
  // already at its new width, so the motion went with the round trip even
  // where the latency did not. So the attribute is set here, at the press,
  // off the parameter the link already carries, and the reply confirms it:
  // a page that arrives overwrites it (item 1), and a request that ends
  // with no page — the network gone, a 204, a press superseded by the next
  // — puts back what was there. Only these two: their values are the
  // stylesheet's to act on. The language stays with the page, because an
  // <html lang> claiming a language its words are not yet in misleads
  // exactly the reader that consults it. Without script, each link is
  // still the link, and the server still decides the width.
  var ahead = {
    sidebar: { attr: "data-sidebar", values: ["wide", "narrow"] },
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
      // A link carries the whole query, so a theme link on a page already
      // at ?sidebar=narrow says both; only what actually changes is set.
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
  document.addEventListener("htmx:beforeTransition", function (event) {
    if (window.matchMedia && window.matchMedia("(prefers-reduced-motion: reduce)").matches) {
      event.preventDefault();
    }
  });
})();
