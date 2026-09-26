package teams

// capturejs.go is the in-page half of the capture daemon.
//
// Session exposes serializable operations across the browser process boundary.
// Event listeners therefore run inside the page and report through polling:
//
//   - jsInstallInterceptor buffers fetch/XHR/WebSocket message payloads.
//   - jsInstallScroller loads conversation history on an in-page schedule.
//   - jsDumpIDB reads the cached messages and conversation metadata.
//
// The shims catch their own errors so capture does not break the Teams tab.

// captureShimVersion is stamped into both shims. A sessh build with new shim
// behaviour bumps it, and install replaces an older shim still running in a page
// held by another process (the routed case).
const captureShimVersion = "3"

// captureEndpointRe matches Teams' public message and API service hosts.
const captureEndpointRe = `teams\.microsoft\.com|teams\.cloud\.microsoft|skype\.com|skypedata\.com|substrate\.office\.com|graph\.microsoft\.com`

// captureHintRe is a cheap pre-filter applied to the raw response text before
// paying for JSON.parse. Teams chatters constantly (presence, telemetry,
// config) and only a fraction of that carries messages; these are the field
// names looksLikeMessage keys off, so anything without one cannot survive
// extraction anyway.
const captureHintRe = `messagetype|composetime|originalarrivaltime|clientarrivaltime|createddatetime|imdisplayname`

// jsInstallInterceptor installs window.__sesshCapture: wrappers around fetch,
// XMLHttpRequest and WebSocket that buffer Teams JSON payloads for Go to drain.
//
// The global wrappers are installed once per document and resolve
// window.__sesshCapture at call time, so re-installing (a newer sessh, or after
// stop()) swaps the buffer without ever double-wrapping a global. Sockets and
// requests already in flight when we install are not seen — the IndexedDB
// harvest is the backstop for those, and is the authoritative source anyway.
const jsInstallInterceptor = `(cfg) => {
  const W = window;
  if (W.__sesshCapture && W.__sesshCapture.version === cfg.version && W.__sesshCapture.enabled) {
    return { installed: true, already: true };
  }
  if (W.__sesshCapture) { try { W.__sesshCapture.stop(); } catch (e) {} }

  const endpointRe = new RegExp(cfg.endpointRe, 'i');
  const hintRe = new RegExp(cfg.hintRe, 'i');

  const state = {
    version: cfg.version,
    enabled: true,
    events: [],
    dropped: 0,
    seen: 0,
  };

  state.push = (source, url, text) => {
    try {
      if (!state.enabled) return;
      const u = String(url || '');
      if (u && !endpointRe.test(u)) return;
      if (typeof text !== 'string' || text.length === 0) return;
      state.seen++;
      if (text.length > cfg.maxBytes) return;
      if (!hintRe.test(text)) return;
      let payload;
      try { payload = JSON.parse(text); } catch (e) { return; }
      // Bounded ring: a drain that never comes (Go wedged, browser busy) must
      // cost the page a fixed amount of memory, not an unbounded one.
      while (state.events.length >= cfg.maxEvents) { state.events.shift(); state.dropped++; }
      state.events.push({ source: source, url: u.slice(0, 200), payload: payload });
    } catch (e) {}
  };

  state.drain = () => {
    const out = { installed: true, events: state.events, dropped: state.dropped, seen: state.seen };
    state.events = [];
    state.dropped = 0;
    state.seen = 0;
    return out;
  };

  state.stop = () => { state.enabled = false; state.events = []; };

  W.__sesshCapture = state;

  const sink = (source, url, text) => {
    const c = W.__sesshCapture;
    if (c && c.push) c.push(source, url, text);
  };

  const origFetch = W.fetch;
  if (typeof origFetch === 'function' && !origFetch.__sesshWrapped) {
    const wrappedFetch = function (input) {
      const p = origFetch.apply(this, arguments);
      try {
        return p.then((res) => {
          try {
            const url = (res && res.url) ||
              (typeof input === 'string' ? input : (input && input.url)) || '';
            if (endpointRe.test(String(url))) {
              let ct = '';
              try { ct = (res.headers && res.headers.get('content-type')) || ''; } catch (e) {}
              // clone() so reading the body here cannot starve the app of it.
              if (/json/i.test(ct)) {
                res.clone().text().then((t) => sink('rest', url, t)).catch(() => {});
              }
            }
          } catch (e) {}
          return res;
        });
      } catch (e) { return p; }
    };
    wrappedFetch.__sesshWrapped = true;
    W.fetch = wrappedFetch;
  }

  const XHR = W.XMLHttpRequest;
  if (XHR && XHR.prototype && XHR.prototype.open && !XHR.prototype.open.__sesshWrapped) {
    const origOpen = XHR.prototype.open;
    const origSend = XHR.prototype.send;
    const wrappedOpen = function (method, url) {
      try { this.__sesshUrl = url; } catch (e) {}
      return origOpen.apply(this, arguments);
    };
    wrappedOpen.__sesshWrapped = true;
    XHR.prototype.open = wrappedOpen;
    XHR.prototype.send = function () {
      try {
        const xhr = this;
        xhr.addEventListener('loadend', () => {
          try {
            const url = xhr.__sesshUrl || '';
            if (!endpointRe.test(String(url))) return;
            const rt = xhr.responseType;
            if (rt === '' || rt === 'text') sink('rest', url, xhr.responseText);
            else if (rt === 'json') sink('rest', url, JSON.stringify(xhr.response));
          } catch (e) {}
        });
      } catch (e) {}
      return origSend.apply(this, arguments);
    };
  }

  const OrigWS = W.WebSocket;
  if (typeof OrigWS === 'function' && !OrigWS.__sesshWrapped) {
    const WrappedWS = function (url, protocols) {
      const ws = arguments.length > 1 ? new OrigWS(url, protocols) : new OrigWS(url);
      try {
        ws.addEventListener('message', (ev) => {
          if (ev && typeof ev.data === 'string') sink('ws', url, ev.data);
        });
      } catch (e) {}
      return ws;
    };
    WrappedWS.prototype = OrigWS.prototype;
    for (const k of ['CONNECTING', 'OPEN', 'CLOSING', 'CLOSED']) {
      try { WrappedWS[k] = OrigWS[k]; } catch (e) {}
    }
    WrappedWS.__sesshWrapped = true;
    W.WebSocket = WrappedWS;
  }

  return { installed: true, already: false };
}`

// jsPoll is the single round trip per tick: it drains the interceptor buffer and
// reads the scroller's counters together. installed:false is how Go learns the
// page was reloaded out from under the shims (Teams v2 does a real document
// navigation on its own) and re-installs them.
const jsPoll = `() => {
  const cap = window.__sesshCapture && window.__sesshCapture.enabled
    ? window.__sesshCapture.drain()
    : { installed: false, events: [], dropped: 0, seen: 0 };
  const scroll = window.__sesshScroll && window.__sesshScroll.running()
    ? window.__sesshScroll.stats()
    : { installed: false };
  return { capture: cap, scroll: scroll };
}`

// jsStopShims disables both shims. It matters only in the routed case: when the
// page belongs to another process, capture exiting must not leave an
// interceptor buffering and a scroller clicking through someone's chats forever.
const jsStopShims = `() => {
  let stopped = 0;
  try { if (window.__sesshCapture) { window.__sesshCapture.stop(); stopped++; } } catch (e) {}
  try { if (window.__sesshScroll) { window.__sesshScroll.stop(); stopped++; } } catch (e) {}
  return stopped;
}`

// jsInstallScroller installs an in-page conversation history loader.
//
// Why it opens chats at all: the IndexedDB harvest can only read what Teams has
// already cached, and Teams lazy-loads history — a thread gets a deep
// replychains.messageMap only once it has been opened and scrolled up. This
// widens the harvest window from "threads you happened to look at" to "every
// thread in the rail".
//
// Opening a conversation marks it
// read and may send a read receipt, exactly as a human click would. That is
// inherent to forcing history to load. maxConversationsPerSweep caps the blast
// radius and dryRun validates the selectors without clicking anything.
//
// The page shim uses el.click(), falling back to a synthetic MouseEvent, to
// activate a conversation. Teams must accept untrusted events for this to work.
const jsInstallScroller = `(cfg) => {
  const W = window;
  if (W.__sesshScroll) { try { W.__sesshScroll.stop(); } catch (e) {} }

  const S = {
    version: cfg.version,
    running: true,
    busy: false,
    timer: null,
    stats: {
      sweeps: 0, opened: 0, errors: 0, skipped: 0, crashes: 0,
      startedAt: Date.now(), last: null,
    },
  };

  const sleep = (ms) => new Promise((r) => setTimeout(r, ms > 0 ? ms : 0));
  const withTimeout = (p, ms, label) => {
    if (!ms || ms <= 0) return Promise.resolve(p);
    return new Promise((resolve, reject) => {
      const t = setTimeout(() => reject(new Error('timeout after ' + ms + 'ms: ' + label)), ms);
      Promise.resolve(p).then(
        (v) => { clearTimeout(t); resolve(v); },
        (e) => { clearTimeout(t); reject(e); }
      );
    });
  };
  const clampDelay = () => {
    const jitter = cfg.jitterMs > 0 ? Math.round((Math.random() * 2 - 1) * cfg.jitterMs) : 0;
    return Math.max(1000, cfg.intervalMs + jitter);
  };
  const shuffled = (arr) => {
    const a = arr.slice();
    for (let i = a.length - 1; i > 0; i--) {
      const j = Math.floor(Math.random() * (i + 1));
      const t = a[i]; a[i] = a[j]; a[j] = t;
    }
    return a;
  };

  const collect = (sels) => {
    for (const s of sels) {
      let els;
      try { els = Array.from(document.querySelectorAll(s)); } catch (e) { els = []; }
      if (els.length) return els;
    }
    return [];
  };
  // A tree item is a conversation if it is a non-expandable leaf carrying an
  // avatar: that excludes the nav rows (Mentions/Discover/Drafts) and the group
  // headers, which are level-2 too but clicking them collapses the list.
  const isConv = (el) => {
    if (!cfg.filterConversations) return true;
    if (el.getAttribute && el.getAttribute('aria-expanded') !== null) return false;
    if (cfg.selectors.avatar) {
      try { if (!el.querySelector(cfg.selectors.avatar)) return false; } catch (e) {}
    }
    return true;
  };
  // The rail is virtualized, so we never hold element handles: a key is a
  // descriptor re-resolved against the DOM at click time.
  const keyOf = (el) => {
    const cand = [
      el.getAttribute && el.getAttribute('data-tid'),
      el.id,
      el.getAttribute && el.getAttribute('data-item-id'),
      el.getAttribute && el.getAttribute('aria-label'),
      (el.textContent || '').trim().slice(0, 80),
    ];
    for (const c of cand) if (c && String(c).trim()) return String(c).trim();
    return null;
  };
  const items = () => collect(cfg.selectors.list).filter(isConv);
  const keys = () => {
    const seen = new Set();
    const out = [];
    for (const el of items()) {
      const k = keyOf(el);
      if (k && !seen.has(k)) { seen.add(k); out.push(k); }
    }
    return out;
  };

  const findPane = () => {
    for (const s of cfg.selectors.messagePane) {
      let el;
      try { el = document.querySelector(s); } catch (e) { el = null; }
      if (el && el.scrollHeight - el.clientHeight > 40) return el;
    }
    // Heuristic fallback: the tallest scrollable div. >400px of overflow skips
    // the ~280px left rail.
    let best = null, bestDelta = 0;
    for (const el of document.querySelectorAll('div')) {
      const delta = el.scrollHeight - el.clientHeight;
      if (delta > 400 && el.clientHeight > 200 && delta > bestDelta) {
        let st;
        try { st = getComputedStyle(el); } catch (e) { continue; }
        if (st && /(auto|scroll)/.test(st.overflowY)) { best = el; bestDelta = delta; }
      }
    }
    return best;
  };

  const listScroller = () => {
    let first = null;
    for (const s of cfg.selectors.list) {
      let el;
      try { el = document.querySelector(s); } catch (e) { el = null; }
      if (el) { first = el; break; }
    }
    let node = first;
    while (node && node !== document.body) {
      const delta = node.scrollHeight - node.clientHeight;
      let st;
      try { st = getComputedStyle(node); } catch (e) { st = null; }
      if (delta > 20 && st && /(auto|scroll)/.test(st.overflowY)) return node;
      node = node.parentElement;
    }
    return null;
  };
  const listScrollTop = (byPx) => {
    try {
      const node = listScroller();
      if (!node) return 0;
      if (typeof byPx === 'number') node.scrollTop = node.scrollTop + byPx;
      return node.scrollTop;
    } catch (e) { return 0; }
  };

  const getSelection = () => {
    try {
      for (const s of cfg.selectors.list) {
        let el = null;
        try {
          el = document.querySelector(s + '[aria-selected="true"]') ||
               document.querySelector(s + '.is-selected');
        } catch (e) { el = null; }
        if (el) return keyOf(el);
      }
      const el = document.querySelector('[role="treeitem"][aria-selected="true"]');
      return el ? keyOf(el) : null;
    } catch (e) { return null; }
  };

  const openKey = async (key) => {
    const el = items().find((e) => keyOf(e) === key);
    if (!el) throw new Error('conversation not in DOM: ' + String(key).slice(0, 40));
    try { el.scrollIntoView({ block: 'center', inline: 'nearest' }); } catch (e) {}
    try {
      el.click();
    } catch (e) {
      try {
        el.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true, view: W }));
      } catch (e2) {}
    }
  };

  // Jump the message pane to the top repeatedly: Teams prepends the next older
  // chunk of history on each jump. Stop when scrollHeight stops growing (the
  // start of the thread) or the step budget runs out.
  const scrollMessagesUp = async () => {
    const heights = [];
    let noGrowth = 0;
    for (let i = 0; i < cfg.scrollSteps; i++) {
      let h = -1;
      try {
        const el = findPane();
        if (el) { h = el.scrollHeight; el.scrollTop = 0; }
      } catch (e) { h = -1; }
      if (h < 0) break;
      heights.push(h);
      if (heights.length >= 2) {
        const grew = heights[heights.length - 1] - heights[heights.length - 2];
        if (grew <= 0) { if (++noGrowth >= 2) break; } else noGrowth = 0;
      }
      if (cfg.scrollDelayMs) await sleep(cfg.scrollDelayMs);
    }
    return heights.length ? heights[heights.length - 1] - heights[0] : 0;
  };

  const visitOne = async (key) => {
    await openKey(key);
    if (cfg.settleAfterOpenMs) await sleep(cfg.settleAfterOpenMs);
    await scrollMessagesUp();
  };

  const diagnose = () => {
    try {
      const list = cfg.selectors.list.map((s) => {
        let n = -1;
        try { n = document.querySelectorAll(s).length; } catch (e) { n = -1; }
        return { selector: s, count: n };
      });
      const pane = findPane();
      return {
        url: location.href,
        listResolved: list.find((x) => x.count > 0) || null,
        conversationCount: items().length,
        messagePane: pane ? {
          scrollHeight: pane.scrollHeight,
          clientHeight: pane.clientHeight,
          tag: pane.tagName,
          tid: pane.getAttribute('data-tid') || null,
        } : null,
      };
    } catch (e) { return { error: String((e && e.message) || e) }; }
  };

  const sweepOnce = async () => {
    const startedAt = Date.now();
    if (cfg.dryRun) {
      S.stats.sweeps++;
      S.stats.last = { dryRun: true, report: diagnose(), durationMs: Date.now() - startedAt };
      return;
    }
    const original = cfg.restoreSelection ? getSelection() : null;
    const visited = new Set();
    let opened = 0, errors = 0, rounds = 0, staleRounds = 0, reachedCap = false;

    while (rounds < cfg.maxRounds && S.running) {
      rounds++;
      let fresh = keys().filter((k) => !visited.has(k));
      if (fresh.length === 0) staleRounds++; else staleRounds = 0;
      if (cfg.shuffle) fresh = shuffled(fresh);

      for (const key of fresh) {
        if (!S.running) break;
        if (cfg.maxConversationsPerSweep && opened >= cfg.maxConversationsPerSweep) {
          reachedCap = true;
          break;
        }
        visited.add(key);
        try {
          await withTimeout(visitOne(key), cfg.perConversationTimeoutMs, String(key).slice(0, 30));
          opened++;
          S.stats.opened++;
        } catch (e) {
          errors++;
          S.stats.errors++;
        }
      }

      if (!S.running || reachedCap) break;
      if (staleRounds >= cfg.maxStaleRounds) break;
      // Advance the virtualized rail to reveal more items; if it will not move,
      // we are at the bottom of the list and the sweep is done.
      const before = listScrollTop(undefined);
      listScrollTop(cfg.listScrollStepPx);
      if (listScrollTop(undefined) === before) break;
    }

    if (original != null) { try { await openKey(original); } catch (e) {} }

    S.stats.sweeps++;
    S.stats.last = {
      opened: opened, errors: errors, rounds: rounds, visited: visited.size,
      reachedCap: reachedCap, hitRoundCap: rounds >= cfg.maxRounds,
      durationMs: Date.now() - startedAt,
    };
  };

  const loop = async () => {
    if (!S.running) return;
    if (S.busy) {
      S.stats.skipped++;
    } else {
      S.busy = true;
      try { await sweepOnce(); } catch (e) { S.stats.crashes++; } finally { S.busy = false; }
    }
    if (!S.running) return;
    S.timer = setTimeout(loop, clampDelay());
  };

  W.__sesshScroll = {
    version: S.version,
    running: () => S.running,
    stats: () => ({
      installed: true,
      sweeps: S.stats.sweeps,
      opened: S.stats.opened,
      errors: S.stats.errors,
      skipped: S.stats.skipped,
      crashes: S.stats.crashes,
      busy: S.busy,
      last: S.stats.last,
    }),
    stop: () => {
      S.running = false;
      if (S.timer) { try { clearTimeout(S.timer); } catch (e) {} S.timer = null; }
    },
  };

  S.timer = setTimeout(loop, Math.max(0, cfg.startDelayMs));
  return { installed: true, dryRun: !!cfg.dryRun };
}`

// jsDumpIDB reads Teams' own IndexedDB caches.
//
// This is the authoritative source, not a backstop: the live WS traffic carries
// Trouter notification envelopes rather than full message bodies, so IDB is
// where a complete message actually lands. Teams keeps roughly a three-week
// window here, which is the real horizon of everything downstream.
const jsDumpIDB = `async () => {
  const out = { messages: [], conversations: [], teams: [], errors: [] };

  let dbs;
  try {
    dbs = await indexedDB.databases();
  } catch (e) {
    out.errors.push('indexedDB.databases() unavailable: ' + String(e));
    return out;
  }
  const findDbName = (re) => {
    const hit = dbs.find((d) => d && typeof d.name === 'string' && re.test(d.name));
    return hit ? hit.name : null;
  };

  const openDB = (name) => new Promise((res, rej) => {
    const req = indexedDB.open(name);
    req.onsuccess = () => res(req.result);
    req.onerror = () => rej(req.error);
    req.onblocked = () => rej(new Error('blocked'));
  });

  const readAll = (db, storeName) => new Promise((res, rej) => {
    const tx = db.transaction(storeName, 'readonly');
    const store = tx.objectStore(storeName);
    const req = store.openCursor();
    const items = [];
    req.onsuccess = () => {
      const cur = req.result;
      if (!cur) { res(items); return; }
      try { items.push(cur.value); } catch (e) {}
      cur.continue();
    };
    req.onerror = () => rej(req.error);
  });

  const member = (u) => u && typeof u === 'object' ? {
    mri: u.mri || u.id || u.userId || u.memberId,
    displayName: u.displayName || u.name,
    email: u.email || u.mail || u.userPrincipalName,
  } : null;

  // Team rows have changed wrapper names over time. Only records from an
  // explicitly team/group-named object store enter this list; a Space
  // conversation called "General" is never treated as a team name.
  const team = (row) => {
    let t = row && typeof row === 'object' ? row : null;
    for (let i = 0; t && i < 3; i++) {
      const nested = t.team || t.teamDetails || t.teamData || t.group ||
        t.groupDetails || t.value || t.data;
      if (!nested || typeof nested !== 'object' || nested === t) break;
      t = nested;
    }
    if (!t || typeof t !== 'object') return null;
    return {
      id: t.id || t.teamId || t.groupId,
      teamId: t.teamId || t.id,
      threadId: t.threadId || t.spaceId || t.conversationId,
      generalChannelId: t.generalChannelId || t.defaultChannelId,
      groupId: t.groupId || t.aadGroupId,
      teamName: t.teamName || t.groupName,
      displayName: t.displayName,
      name: t.name,
      title: t.title,
    };
  };

  // 1. Replychains = the actual message history. Each record's messageMap holds
  //    one or more message-shaped objects.
  try {
    const rcName = findDbName(/:replychain-manager:/);
    if (rcName) {
      const db = await openDB(rcName);
      const rows = await readAll(db, 'replychains');
      db.close();
      for (const row of rows) {
        const mm = row && row.messageMap;
        if (mm && typeof mm === 'object') {
          for (const v of Object.values(mm)) {
            if (v && typeof v === 'object') out.messages.push(v);
          }
        }
      }
    } else {
      out.errors.push('replychain-manager DB not found');
    }
  } catch (e) {
    out.errors.push('replychains: ' + String((e && e.message) || e));
  }

  // 2. Conversations = chat list metadata (titles, members, lastMessage). The
  //    lastMessage object is itself message-shaped and rounds out history for
  //    threads we have not fully opened.
  try {
    const cName = findDbName(/:conversation-manager:/);
    if (cName) {
      const db = await openDB(cName);
      const rows = await readAll(db, 'conversations');
      for (const c of rows) {
        if (c && c.lastMessage && typeof c.lastMessage === 'object') {
          out.messages.push(c.lastMessage);
        }
        out.conversations.push({
          id: c && c.id,
          type: c && c.type,
          teamId: c && c.teamId,
          groupId: c && (c.groupId || c.aadGroupId),
          teamName: c && c.teamName,
          lastMessageTimeUtc: c && c.lastMessageTimeUtc,
          threadProperties: c && c.threadProperties && {
            topic: c.threadProperties.topic,
            topicThreadTopic: c.threadProperties.topicThreadTopic,
            threadType: c.threadProperties.threadType,
            spaceId: c.threadProperties.spaceId,
          },
          chatTitle: c && c.chatTitle && {
            short: c.chatTitle.shortTitle,
            long: c.chatTitle.longTitle,
            members: (c.chatTitle.avatarUsersInfo || []).map((u) => ({
              mri: u.mri, displayName: u.displayName, email: u.email,
            })),
          },
          members: (c && Array.isArray(c.members) ? c.members : []).map(member).filter(Boolean),
          membersComplete: !!(c && Array.isArray(c.members) && !c.isConversationMembersTruncated),
        });
        // Some builds colocate an explicit team definition on the
        // conversation row. Read only a team/group-named child object; never
        // feed the Space conversation itself to team(), because its title is
        // the default channel name (usually "General").
        for (const explicit of [c && c.team, c && c.teamDetails, c && c.teamData,
          c && c.group, c && c.groupDetails]) {
          const t = team(explicit);
          if (t) out.teams.push(t);
        }
      }
      for (const storeName of Array.from(db.objectStoreNames)) {
        if (storeName === 'conversations' || !/(?:team|group)/i.test(storeName)) continue;
        try {
          for (const row of await readAll(db, storeName)) {
            const t = team(row);
            if (t) out.teams.push(t);
          }
        } catch (e) {
          out.errors.push('teams/' + storeName + ': ' + String((e && e.message) || e));
        }
      }
      db.close();
    }
  } catch (e) {
    out.errors.push('conversations: ' + String((e && e.message) || e));
  }

  return out;
}`
