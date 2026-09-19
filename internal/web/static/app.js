// Independent, self-contained enhancements, each an IIFE with its own
// header comment. Every rule from AGENTS.md's JavaScript section applies
// to all of them: vanilla, no build step, no dependency, and each is
// strictly additive — the feature it touches has a working, server-
// rendered form already, and this file only adds a shortcut or a nicety on
// top of it.

// The one thing in this file outside an IIFE: a registry the page switcher
// at the bottom runs after it has replaced the document's body.
//
// An enhancement that delegates from the document needs nothing here — its
// listener outlives every element it will ever fire for. These are the ones
// that hold on to a particular element, which after a switch is a node that
// is no longer in the page. A function registered here runs now and again
// after every switch, so it has to be safe to run more than once: look the
// elements up each time, and register document-level listeners outside it.
// Set by the page switcher at the bottom: re-renders the page at the current
// URL in place, for anything that changed the account underneath it. Null
// until then, and null for good where the switcher is not running, so every
// caller has to have a way of coping without it.
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
// menus on outside clicks and nudge informational popups back on screen.
// Opening, summary-click closing and exclusivity still work without JS.
// Topbar menus keep their fixed CSS positioning.
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
// /share/<slug>/search on the read-only view; all this does is fetch that
// same route's results — "?partial=1" asks the server for just the list,
// not a second page — into an overlay instead of navigating to it, and let
// the arrow keys and Enter move through what comes back. Absent, disabled,
// or failing to load, the link still works exactly as before.
(function () {
  "use strict";

  // Looked up again after every page switch: the overlay on the page now is
  // not the node this closed over when the file first ran. Null on a page
  // with no search — signed out only.
  var overlay = null;
  var button, input, results, searchPath;

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
    // Set from chrome's SearchPath — "/search" signed in,
    // "/share/<slug>/search" on the read-only view — so this file never
    // hardcodes which one applies.
    searchPath = overlay.dataset.searchPath;
    input.addEventListener("input", function () {
      var query = input.value;
      clearTimeout(debounce);
      debounce = setTimeout(function () { fetchResults(query); }, 120);
    });

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

// Collapsing the rail without the round trip.
//
// The control is a link and stays one: following it re-renders the page at the
// other width and the server remembers the choice in a cookie. That is the
// whole feature, and it works with this file absent, disabled, or failing to
// load. What this adds is doing the visible half here — flipping the width
// attribute on <html>, which is what the stylesheet keys the rail off — and
// asking the server for the same URL in the background so the cookie is right
// for the next page load. Nothing is rendered from script: the wording, the
// arrow and the accessible name all follow that one attribute through CSS.
//
// If the background request fails, this page keeps the width just chosen and
// the next one goes back to the stored width. A modified click (new tab, new
// window) is left alone to do what was asked of it.
(function () {
  "use strict";

  if (!window.fetch) return; // Without it the link is still the whole feature.

  // Bound again after every page switch: the rail on the page now is not the
  // one this was bound to.
  onPageChange(function () {
    var toggle = document.querySelector(".nav-collapse");
    if (!toggle) return; // Signed out, or a page with no rail.

    toggle.addEventListener("click", function (event) {
      if (event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) {
        return;
      }
      event.preventDefault();

      var root = document.documentElement;
      var next = root.getAttribute("data-sidebar") === "narrow" ? "wide" : "narrow";

      // Read both before anything moves: one is the width being applied, which
      // is what the server has to be told, and the other is where the link
      // points afterwards. Taking them in the wrong order stores the width the
      // reader just left.
      var applied = next === "narrow" ? toggle.dataset.hrefNarrow : toggle.dataset.hrefWide;
      var other = next === "narrow" ? toggle.dataset.hrefWide : toggle.dataset.hrefNarrow;

      root.setAttribute("data-sidebar", next);

      // Point the link at the other width, for the next press and for anyone
      // who opens it in a tab of its own.
      if (other) toggle.setAttribute("href", other);

      // The label now showing is the one describing the next press, so the
      // tooltip comes from the DOM rather than from a copy kept here.
      var label = toggle.querySelector(".nav-label .to-" + (next === "narrow" ? "wide" : "narrow"));
      if (label) toggle.setAttribute("title", label.textContent.trim());

      // HEAD rather than GET: the handler still runs and still sets the cookie,
      // and a page's worth of HTML is not worth transferring to discard.
      if (applied) fetch(applied, { method: "HEAD", credentials: "same-origin" }).catch(function () {});
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
    close.className = "raised-close";
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

// The share slug: copying it, and rotating it without leaving the page.
//
// Two enhancements in one place because they are one thing. A rotation
// replaces the slug, and the copy button carries the address of the slug it
// was built for — so whatever rebuilds one has to rebuild the other.
//
// Copying cannot exist without a script at all: a clipboard cannot be written
// to from markup. That is why the button is built here rather than rendered
// and then wired up — it exists exactly when it works, and a page served over
// plain http to anything but localhost, which has no navigator.clipboard,
// gets no button instead of a dead one.
//
// Rotating works entirely without this. The control is a link to the same
// page with the question showing, the answer is a form that posts, and the
// server redirects to the outcome — three page loads for one decision. This
// swaps the card in place at each step instead. Every request it makes is the
// one the markup already pointed at, so nothing is decided here that the
// server did not decide, and any failure falls back to the navigation that
// was asked for.
//
// The whole card is fetched rather than a "?partial=1" fragment, unlike the
// board's ranking menu: here the card is nearly the whole page, so a partial
// route would save almost nothing and add a branch to a handler.
//
// One deliberate difference from a full page load: the outcome arrives as the
// note inside the swapped card rather than being raised into a panel. The
// slug visibly changes under the reader's eyes, and a dialog to dismiss on
// top of something they just watched happen is feedback for nothing.
(function () {
  "use strict";

  // Rebuilt after every swap: the row is new markup carrying a new address.
  function mountCopy() {
    var row = document.querySelector("[data-copy]");
    if (!row) return; // No link yet, or no APP_URL to make it absolute with.
    if (!navigator.clipboard || !navigator.clipboard.writeText) return;

    var button = document.createElement("button");
    button.type = "button";
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
  if (!window.fetch) return; // Without it every control is still a real one.

  // Swaps in the card from a response, and puts the copy button back on it.
  // Returns false when the markup was not what we expected, so the caller can
  // fall back to a real navigation rather than leaving a half-changed page.
  function swap(html, url) {
    var card = document.querySelector("section.card");
    var wrapper = document.createElement("div");
    wrapper.innerHTML = html;
    var replacement = wrapper.querySelector("section.card");
    if (!card || !replacement) return false;

    card.replaceWith(replacement);
    if (url) history.replaceState(null, "", url);
    mountCopy();
    return true;
  }

  function load(url) {
    fetch(url, { credentials: "same-origin" })
      .then(function (response) { return response.ok ? response.text() : null; })
      .then(function (html) {
        if (html === null || !swap(html, url)) window.location.href = url;
      })
      .catch(function () { window.location.href = url; });
  }

  // Asking the question, and taking it back: both are links to this same page
  // with the question showing or not.
  document.addEventListener("click", function (event) {
    var link = event.target.closest(".share-section a[href]");
    if (!link) return;
    if (event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) {
      return;
    }
    event.preventDefault();
    load(link.href);
  });

  // Answering it. The form carries its own CSRF token, so posting it as it
  // stands is the same request the browser would have made.
  document.addEventListener("submit", function (event) {
    var form = event.target;
    if (!form.matches(".share-section form")) return;

    event.preventDefault();
    // URLSearchParams, not the FormData it is built from: FormData posts as
    // multipart, and this form declares no enctype, so a browser submitting
    // it sends url-encoded. The difference is not cosmetic — Go's ParseForm
    // does not read a multipart body, leaves PostForm empty, and the CSRF
    // token goes missing, which the server correctly answers with "the form
    // expired". Every request this makes has to be the one the markup
    // already described.
    fetch(form.action, {
      method: "POST",
      body: new URLSearchParams(new FormData(form)),
      credentials: "same-origin",
    })
      .then(function (response) {
        // The redirect has been followed, so this is the outcome page and
        // response.url is where it landed.
        return response.ok ? response.text().then(function (html) {
          return { html: html, url: response.url };
        }) : null;
      })
      .then(function (result) {
        if (result === null || !swap(result.html, result.url)) form.submit();
      })
      .catch(function () { form.submit(); });
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
    (items[1] || items[0] || card).focus();
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

// Switching pages without the flash.
//
// Every link in the application is a real link to a real URL, and following
// one works with this file absent, disabled, or failing to load. That is the
// whole feature and none of it is built here. What a full page load costs is
// the flash: the document is torn down and drawn again, and the bar, the rail
// and the wordmark — identical on every page — go white and come back. On a
// phone that reads as the application blinking each time it is touched.
//
// This fetches the page the link points at and puts it in place instead.
//
// The whole body is replaced, not just the content. The rail's highlight, the
// theme and language links (each of which is the current URL with one
// parameter changed) and the title all belong to the page being moved to, and
// patching the handful known to differ is a list that goes stale the first
// time somebody adds a control to the bar. What arrives here is the server's
// own rendering of that URL, so a page reached this way and a page reached by
// following the link are the same page — which is also why the pages that
// used to answer "?partial=1" for this no longer need to.
//
// It replaces two enhancements that each did this for one route: the board's
// ranking menu and the player roster. Both survive as the focus rules below,
// which are the only part that was ever specific to them.
//
// Anything it cannot do it hands back to the browser: a modified click, a
// link out of the application, a reply that is not HTML, markup that is not a
// document, a request that fails. The fallback is always the navigation that
// was asked for, which is what would have happened anyway.
(function () {
  "use strict";

  if (!window.fetch || !window.history || !history.pushState) return;
  if (!window.DOMParser || !window.AbortController) return;
  if (!document.body || !document.body.replaceChildren) return;

  var parser = new DOMParser();
  var inFlight = null; // The request being waited on, if any.
  var marked = false; // Whether the entry this page loaded on is one of ours.
  var slow; // The timer that admits a page is taking a while.

  // Where each of our history entries was scrolled to. Keyed by a number kept
  // in the entry's own state, because a URL is not unique in a history: the
  // same board can be three entries back and two entries forward.
  var scrolls = {};
  var entry = 0;
  var nextEntry = 1;

  function waiting(on) {
    clearTimeout(slow);
    if (!on) {
      document.documentElement.removeAttribute("data-loading");
      return;
    }
    // Only once it has taken long enough that silence would read as the
    // press having done nothing. A page off a local network never gets here.
    slow = setTimeout(function () {
      document.documentElement.setAttribute("data-loading", "");
    }, 120);
  }

  // apply puts a fetched document in place of this one, and reports whether
  // it was a document at all — a reply that parses to an empty body is a
  // sign-in page served as a fragment, or an error page from something in
  // front of the application, and either is better navigated to for real.
  function apply(html) {
    var doc = parser.parseFromString(html, "text/html");
    if (!doc || !doc.body || !doc.body.firstChild) return false;

    // The attributes the server decides for the whole document: the reader's
    // language, their theme, and how wide the rail is. Following a theme link
    // is an ordinary navigation, so this is how the theme actually changes.
    var root = document.documentElement;
    var incoming = doc.documentElement;
    ["lang", "data-theme", "data-sidebar"].forEach(function (name) {
      var value = incoming.getAttribute(name);
      if (value === null) root.removeAttribute(name);
      else root.setAttribute(name, value);
    });
    document.title = doc.title;

    // The body's own attributes as well as its children. There are none
    // today; a page that grows one would otherwise keep the last page's.
    Array.prototype.slice.call(document.body.attributes).forEach(function (attr) {
      if (!doc.body.hasAttribute(attr.name)) document.body.removeAttribute(attr.name);
    });
    Array.prototype.forEach.call(doc.body.attributes, function (attr) {
      document.body.setAttribute(attr.name, attr.value);
    });

    document.body.replaceChildren.apply(
      document.body,
      Array.prototype.slice.call(doc.body.childNodes)
    );
    onPageChange.rerun();
    return true;
  }

  // Focus has to be put somewhere: the element that was clicked is about to
  // stop existing, and focus left on a departing node lands on the body,
  // which puts a keyboard back at the start of the page with nothing to say
  // it moved. A real navigation resets focus too — this only aims it better.
  function focusMain() {
    var main = document.getElementById("main");
    if (!main) return;
    // tabindex only for as long as the focus lasts, so the region never
    // becomes a tab stop of its own.
    main.setAttribute("tabindex", "-1");
    main.focus();
    main.addEventListener("blur", function () { main.removeAttribute("tabindex"); }, { once: true });
  }

  // Where focus goes instead, for the three controls that are a place in the
  // page rather than a step out of it. Each returns false if the page that
  // arrived does not have what it was looking for, and the main region takes
  // over.
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
        if (row) row.focus();
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
        bar.focus();
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
            rows[i].focus();
            return true;
          }
        }
        return false;
      };
    }
    return null;
  }

  // go fetches url and, if what comes back is a page, puts it in place.
  // commit is what to do with the history and the scroll once it is there:
  // pushing an entry for a press, restoring one for a Back.
  function go(url, commit, focus) {
    if (inFlight) inFlight.abort();
    var controller = new AbortController();
    inFlight = controller;
    waiting(true);

    fetch(url, {
      credentials: "same-origin",
      headers: { Accept: "text/html" },
      signal: controller.signal,
    })
      .then(function (response) {
        var type = response.headers.get("content-type") || "";
        if (type.indexOf("text/html") === -1) return null;
        return response.text().then(function (html) {
          // A redirect is where the reader actually ended up — a sign-in
          // page, most often — so that is the address to record.
          return { html: html, url: response.redirected ? response.url : url };
        });
      })
      .then(function (result) {
        if (inFlight !== controller) return; // A later press won.
        inFlight = null;
        waiting(false);
        if (result === null || !apply(result.html)) {
          window.location.href = url;
          return;
        }
        commit(result.url);
        if (!focus || !focus()) focusMain();
      })
      .catch(function (err) {
        if (err && err.name === "AbortError") return;
        waiting(false);
        window.location.href = url;
      });
  }

  // Which links this can take over. Everything else is left alone, and left
  // alone means the browser does exactly what the markup asked for.
  function swappable(link) {
    if (link.target && link.target !== "_self") return false;
    if (link.hasAttribute("download")) return false;
    if (link.dataset.reload !== undefined) return false; // The opt-out.
    if (link.origin !== window.location.origin) return false; // Also catches mailto:.
    if (link.pathname.indexOf("/static/") === 0) return false;

    var href = link.getAttribute("href");
    if (!href || href.charAt(0) === "#") return false; // An anchor in this page.
    // This page with a fragment on it: the browser's own scroll, not a fetch.
    if (link.pathname === window.location.pathname &&
        link.search === window.location.search && link.hash) {
      return false;
    }
    return true;
  }

  document.addEventListener("click", function (event) {
    // Registered last in this file, so an enhancement that has already taken
    // this press — the search button, the rail's collapse, the share slug —
    // has said so by now.
    if (event.defaultPrevented) return;
    if (event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) {
      return;
    }

    var link = event.target.closest("a[href]");
    if (!link || !swappable(link)) return;

    event.preventDefault();
    var url = link.href;
    var focus = focusRule(link);

    // The entry this page loaded on is marked on the first switch, so coming
    // back to it later is a switch like any other.
    if (!marked) {
      history.replaceState({ wl: entry }, "", window.location.href);
      marked = true;
    }
    scrolls[entry] = window.scrollY;

    var to = nextEntry++;
    go(url, function (finalURL) {
      history.pushState({ wl: to }, "", finalURL);
      entry = to;
      // A press is a new page, and a new page starts at the top.
      window.scrollTo(0, 0);
    }, focus);
  });

  // For anything that changed the page underneath it — the enrolment dialog
  // finishing, say — without itself being a navigation.
  refreshPage = function () {
    go(window.location.href, function () {}, null);
  };

  window.addEventListener("popstate", function (event) {
    if (!marked) return; // Nothing here was switched, so nothing here is stale.
    if (!event.state || typeof event.state.wl !== "number") {
      // Older than anything this ever drew. The address bar already says
      // where we are; a reload is the honest way to agree with it.
      window.location.reload();
      return;
    }

    scrolls[entry] = window.scrollY; // The page being left, before it goes.
    var to = event.state.wl;
    go(window.location.href, function () {
      entry = to;
      window.scrollTo(0, scrolls[to] || 0);
    }, null);
  });
})();
