"use strict";
const $ = (s) => document.querySelector(s);
const esc = (s) =>
  String(s ?? "").replace(
    /[&<>"']/g,
    (c) =>
      ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[
        c
      ],
  );
const enc = encodeURIComponent;
const values = (o) => Object.values(o || {});
const short = (s) => (s ? s.slice(0, 7) : "—");
const link = (kind, id) => `/${kind}/${enc(id)}`;
const compareLink = (repo, old, next) => `/compare/${repo.split("/").map(enc).join("/")}/${enc(old)}/${enc(next)}`;
const commitURL = (c) => `https://github.com/${c.repository.split("/").map(enc).join("/")}/commit/${enc(c.revision)}`;
const timelinePages = new Map();
const configurationMetadata = new Map();
const commitComparisons = new Map();
const reviewSelections = new Map();
let drawGeneration = 0;
const comparisonSelections = new Map();
let state,
  repository = "",
  renderID = 0;
const age = (t) => {
  if (!t || t.startsWith("0001")) return "Not seen yet";
  const seconds = Math.max(0, (Date.now() - new Date(t)) / 1000);
  if (seconds < 60) return "Just now";
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m ago`;
  if (seconds < 86400) return `${Math.floor(seconds / 3600)}h ago`;
  return `${Math.floor(seconds / 86400)}d ago`;
};
const date = (t) =>
  new Date(t).toLocaleString(undefined, {
    year: "numeric",
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  });
const bytes = (n) => `${(n / 1048576).toFixed(0)} MiB`;
const icon = (h) =>
  `<img src="/${h.platform === "darwin" ? "apple" : "tux"}.svg" alt="${h.platform === "darwin" ? "macOS" : "Linux"}" width="22" height="24">`;
const tone = (s) =>
  s === "Up to date" || s === "success" || s === "staged"
    ? "green"
    : /Reboot/.test(s)
      ? "purple"
      : /attention|failed|interrupted/.test(s)
        ? "red"
        : /Processing|Outdated/.test(s)
          ? "amber"
          : /Unknown/.test(s)
            ? "blue"
            : "";
const badge = (s, t = tone(s)) =>
  `<span class="badge ${esc(t)}">${esc(s)}</span>`;
const button = (text, action, host = "", extra = "") =>
  `<button data-action="${esc(action)}" data-host="${esc(host)}" ${extra}>${esc(text)}</button>`;
const heading = (title, sub = "", actions = "") =>
  `<div class="page-heading"><div><h1>${esc(title)}</h1>${sub ? `<p class="subtitle">${esc(sub)}</p>` : ""}</div>${actions ? `<div class="actions">${actions}</div>` : ""}</div>`;
function firstConfiguration(h, p) {
  if (!p) return null;
  return configurationMetadata.get(h.id)?.get(p) || h.configurations?.[p] || null;
}
function configuration(h, p) {
  const c = firstConfiguration(h, p);
  return c ? `<a class="configuration-title" href="${commitURL(c)}">${esc(c.title)}</a><time class="configuration-date">${date(c.created)}</time>` : '<span class="muted">Unknown</span>';
}
function configurations(h) {
  const current = `<div>${configuration(h, h.observation.active)}</div>`;
  const target = h.desired && h.desired !== h.observation.active && h.status !== "Processing"
    ? `<span class="configuration-arrow" aria-label="to">→</span><div>${configuration(h, h.desired)}</div>`
    : "";
  return `<div class="configuration">${current}${target}</div>`;
}
async function request(url, body) {
  const response = await fetch(
    url,
    body === undefined
      ? {}
      : {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(body),
        },
  );
  const data = await response.json();
  if (!response.ok) {
    const error = new Error(data.error || `Request failed (${response.status})`);
    error.status = response.status;
    throw error;
  }
  return data;
}
function toast(message) {
  $("#toast").textContent = message;
  $("#toast").hidden = false;
  clearTimeout(toast.timer);
  toast.timer = setTimeout(() => ($("#toast").hidden = true), 6000);
}
const signature = (data) =>
  JSON.stringify(data, (key, value) =>
    ["now", "last_seen", "checked"].includes(key) ? undefined : value,
  );
async function refresh(render = true) {
  if (location.pathname.startsWith("/compare/")) { if (render) await draw(); return; }
  const startedOn = location.pathname;
  try {
    const incoming = await request("/api/ui/state");
    const changed = !state || signature(state) !== signature(incoming);
    state = incoming;
    if (location.pathname !== startedOn) return;
    if (render || changed || location.pathname.startsWith("/host/")) await draw();
  } catch (e) {
    if (!state)
      $("#app").innerHTML =
        `<div class="empty">${esc(e.message)}<br>Check that infra-hub is running.</div>`;
    else toast(e.message);
  }
}
function fleet() {
  const repos = values(state.repositories).sort((a, b) =>
    a.id.localeCompare(b.id),
  );
  if (!repos.some(r => r.id === repository)) repository = repos[0]?.id || "";
  return `<div class="section-tabs" aria-label="Repositories">${repos.map(r => `<button data-repository="${esc(r.id)}" class="${repository === r.id ? "selected" : ""}">${esc(r.id)}</button>`).join("")}</div>${pullRequestCards(repository)}<div id="host-tables">${hostTables()}</div>`;
}
function hostTables() {
  let output = "";
  for (const repo of values(state.repositories).sort((a, b) =>
    a.id.localeCompare(b.id),
  )) {
    if (repository !== repo.id) continue;
    const hosts = values(state.hosts).filter(h => !h.removed && h.repository === repo.id)
      .sort((a, b) => a.name.localeCompare(b.name));
    if (!hosts.length) continue;
    output += `<section class="repo-block"><div class="table-scroll"><table><thead><tr><th>HOST</th><th>STATUS</th><th>CONFIGURATION</th></tr></thead><tbody>${hosts.map((h) => `<tr class="host-row" data-href="${link("host", h.id)}"><td><a class="host-cell" href="${link("host", h.id)}"><span class="host-logo">${icon(h)}</span><div><span class="host-name">${esc(h.name)}</span></div></a></td><td>${badge(h.status)}</td><td>${configurations(h)}</td></tr>`).join("")}</tbody></table></div></section>`;
  }
  return output || '<div class="empty">No hosts match.</div>';
}
function timelineConfigurations(h) {
  const page = timelinePages.get(h.id);
  return page ? { ...page, more: !!page.next_cursor } : { entries: [], staged: "", unknown: true, more: false };
}
function defaultComparison(h) {
  return timelinePages.get(h.id)?.comparison || { before: "", after: "" };
}
function rememberConfigurations(id, entries) {
  const metadata = configurationMetadata.get(id) || new Map();
  for (const entry of entries) metadata.set(entry.path, entry.commit);
  configurationMetadata.set(id, metadata);
}
function acceptTimeline(id, page, append = false) {
  const previous = timelinePages.get(id);
  rememberConfigurations(id, page.entries);
  if (previous?.version === page.version) {
    const entries = append ? [...previous.entries, ...page.entries] : [...page.entries, ...previous.entries];
    page.entries = [...new Map(entries.map(e => [e.path, e])).values()]
      .sort((a, b) => new Date(b.commit.created) - new Date(a.commit.created) || b.commit.revision.localeCompare(a.commit.revision));
    if (!append) page.next_cursor = previous.next_cursor;
  }
  timelinePages.set(id, page);
}
async function loadMoreTimeline(id) {
  const previous = timelinePages.get(id);
  if (!previous?.next_cursor) return;
  try {
    const page = await request(`/api/ui/hosts/${enc(id)}/timeline?cursor=${enc(previous.next_cursor)}`);
    if (timelinePages.get(id)?.version === previous.version) acceptTimeline(id, page, true);
  } catch (error) {
    if (error.status !== 409) throw error;
    const page = await request(`/api/ui/hosts/${enc(id)}/timeline`);
    acceptTimeline(id, page);
  }
}
function selectedComparison(h) {
  return comparisonSelections.get(h.id) || defaultComparison(h);
}
function comparisonControls(h, path) {
  const pair = selectedComparison(h);
  return `<div class="comparison-controls" aria-label="Use configuration in comparison">${["before", "after"].map(side => `<button type="button" class="compare-pick ${side}" data-action="compare-${side}" data-host="${esc(h.id)}" data-path="${esc(path)}" aria-pressed="${pair[side] === path}" aria-label="Use as ${side}">${side === "before" ? "Before" : "After"}</button>`).join("")}</div>`;
}
function comparisonPanel(h) {
  const pair = selectedComparison(h);
  const selected = comparisonSelections.has(h.id);
  return `<section class="panel comparison-panel"><div class="panel-title"><h2>Comparison</h2>${selected ? button("Reset", "compare-reset", h.id, 'class="small" title="Restore automatic comparison"') : ""}</div><div class="comparison-pair">${["before", "after"].map(side => `<div class="comparison-endpoint ${side}"><span class="comparison-side">${side === "before" ? "Before" : "After"}</span>${pair[side] ? configuration(h, pair[side]) : '<span class="muted">No configuration available</span>'}</div>`).join('<span class="comparison-direction" aria-hidden="true">→</span>')}</div>${pair.before && pair.after ? `<div class="diff" aria-live="polite" data-old="${esc(pair.before)}" data-new="${esc(pair.after)}">Loading comparison…</div>` : '<p class="muted">Choose a Before and After configuration from the timeline.</p>'}</section>`;
}
function configurationTimeline(h) {
  const { entries, staged, unknown, more } = timelineConfigurations(h);
  const unknownEntry = unknown
    ? `<li class="is-live"><div class="timeline-entry"><div><strong>unknown configuration</strong></div>${badge("Live", "green")}</div>${comparisonControls(h, h.observation.active)}</li>` : "";
  return `<section class="panel timeline-panel"><div class="panel-title"><h2>Configurations</h2></div><div class="timeline-scroll"><ol class="configuration-timeline">${entries.map(entry => {
    const c = entry.commit;
    const live = entry.path === h.observation.active;
    const isStaged = !live && entry.path === staged;
    const queued = !live && h.automatic && entry.path === h.desired && h.status === "Deployment queued";
    let marker = live ? badge("Live", "green") : isStaged ? badge("Reboot to apply") : h.automatic && entry.path === h.desired ? badge(h.status) : "";
    const title = `<a href="${commitURL(c)}">${esc(c.title)}</a>`;
    const branch = entry.offBranch ? `<span class="branch-warning">! ${esc(c.branch || "branch unavailable")}</span>` : "";
    return `<li class="${live ? "is-live" : ""}"><div class="timeline-entry"><div>${live ? `<strong>${title}</strong>` : isStaged || queued ? `<em>${title}</em>` : title}<div class="timeline-date"><time>${date(c.created)}</time>${branch}</div></div>${marker}</div>${comparisonControls(h, entry.path)}</li>`;
  }).join("")}${unknownEntry}</ol>${more ? button("Load more", "timeline-more", h.id, 'class="timeline-more"') : ""}</div></section>`;
}
function deploymentAttempt(h) {
  if (h.status !== "Deploy failed" && h.status !== "Deployment queued") return "";
  const result = timelinePages.get(h.id)?.attempt;
  if (!result) return "";
  const unreachable = result.outcome === "unreachable";
  const explanation = result.detail || (unreachable ? "The host could not be reached. Deployment will be retried." : "The deployment attempt failed without an error message.");
  return `<section class="panel deployment-attempt"><div class="panel-title"><h2>${unreachable ? "Host unreachable" : "Deployment failed"}</h2></div><p class="attempt-time">Last attempt · <time datetime="${esc(result.finished)}">${date(result.finished)}</time></p><pre class="attempt-explanation">${esc(explanation)}</pre></section>`;
}
function hostView(id) {
  const h = timelinePages.get(id)?.host || state.hosts[id];
  if (!h) return '<div class="empty">Host not found.</div>';
  return `<a class="back" href="/">← Back to fleet</a>` +
    heading(h.name, `${h.repository} · Last seen ${age(h.last_seen).toLowerCase()}`) +
    `<div class="host-columns">${configurationTimeline(h)}<div class="host-right">${deploymentAttempt(h)}${comparisonPanel(h)}</div></div>`;
}
function prChecks(p) {
 return p.draft ? "Draft" : p.checks === "success" ? "Checks passed" : p.checks === "failure" ? "Checks failed" : p.checks === "pending" ? "Checks pending" : "Checks unknown";
}
function prStatusDot(p) {
 return `<span class="pr-status-dot ${p.draft ? "draft" : p.checks === "failure" ? "failed" : p.checks === "success" ? "passed" : "pending"}" role="img" aria-label="${esc(prChecks(p))}" title="${esc(prChecks(p))}"></span>`;
}
function pullRequestCards(repo) {
  const prs = values(state.pull_requests)
    .filter(p => p.repository === repo && p.state === "open")
    .sort((a, b) => b.number - a.number);
  const error = state.repositories[repo]?.pr_error;
  return (error ? `<div class="notice">${esc(error)}</div>` : "") + (prs.length ? `<div class="pull-request-cards">${prs.map(p =>
    `<a class="panel pr-card" href="${compareLink(p.repository, p.base || p.base_head || state.repositories[p.repository].main, p.head)}"><div class="pr-heading">${prStatusDot(p)}<h2>${esc(p.title)}</h2></div><p>#${p.number} &nbsp; · &nbsp; ${p.inputs.length} input changes${p.checks === "success" && p.evaluated ? ` &nbsp; · &nbsp; ${p.hosts.length} hosts affected` : ""}</p></a>`
  ).join("")}</div>` : "");
}
function flakeInputs(p) {
  if (!p.inputs?.length) return "";
  const inputDate = (stamp, revision) => stamp ? new Date(stamp).toLocaleDateString(undefined, {year:"numeric", month:"short", day:"numeric"}) : revision ? "Date unavailable" : "—";
  return `<section class="panel"><div class="panel-title"><h2>flake.lock inputs</h2></div><table class="diff-table"><thead><tr><th>INPUT</th><th>BEFORE → AFTER</th></tr></thead><tbody>${p.inputs.map(i => {
    const dates = `${esc(inputDate(i.before_date, i.before))} → ${esc(inputDate(i.after_date, i.after))}`;
    const url = i.compare_url?.startsWith("https://github.com/") ? i.compare_url : "";
    return `<tr><td>${esc(i.name)}</td><td>${url ? `<a class="input-compare" href="${esc(url)}" target="_blank" rel="noreferrer">${dates}</a>` : dates}</td></tr>`;
  }).join("")}</tbody></table></section>`;
}
function commitComparisonView(id) {
  const data = commitComparisons.get(id);
  if (!data) return '<div class="empty">Comparison not found.</div>';
  const commit = c => c ? `<a href="${commitURL(c)}">${esc(c.title || short(c.revision))}</a>${c.created && !c.created.startsWith("0001") ? `<time class="configuration-date">${date(c.created)}</time>` : ""}` : '<span class="muted">Commit unavailable</span>';
  const failures = data.failed_checks || [];
  return '<a class="back" href="/">← Back to fleet</a>' +
    `<div class="page-heading"><div><h1 class="pr-title">${prStatusDot(data)}${esc(data.head?.title || "Comparison")}</h1><p class="subtitle">${esc(data.repository)}</p></div></div>` +
    `${data.git_error ? `<div class="notice">${esc(data.git_error)}</div>` : ""}<div class="pr-metadata">${flakeInputs(data)}<section class="panel"><div class="panel-title"><h2>Summary</h2></div><dl class="facts"><dt>Head</dt><dd>${commit(data.head)}</dd><dt>Base</dt><dd>${commit(data.base)}</dd></dl>${failures.length ? `<div class="pr-failed-checks"><h3>Failed checks</h3><ul>${failures.map(c => `<li>${/^https?:\/\//.test(c.url || "") ? `<a href="${esc(c.url)}" target="_blank" rel="noreferrer">${esc(c.name)}</a>` : esc(c.name)}${c.detail ? `<p>${esc(c.detail)}</p>` : ""}</li>`).join("")}</ul></div>` : ""}<div class="actions pr-summary-actions"><a class="button" href="${esc(data.github_url)}" target="_blank" rel="noreferrer">GitHub</a></div></section></div>${comparisonReview(data, id)}`;
}

function comparisonReview(data, id) {
  const selection = reviewSelections.get(id) || "";
  const all = { ...data.all, id: "", name: "All hosts" };
  const selected = data.hosts.find(h => h.id === selection) || all;
  const count = h => {
    const d = data.comparisons[h.pair];
    return d?.status === "different" ? d.packages.length : d?.status === "unchanged" ? 0 : -1;
  };
  const hosts = [all, ...[...data.hosts].sort((a,b) => count(b) - count(a) || a.name.localeCompare(b.name))];
  const comparison = data.comparisons[selected.pair];
  const missing = comparison?.missing_hosts || [];
  return `<div class="pr-review"><nav class="pr-host-list" aria-label="Hosts">${hosts.map(h =>
    `<button type="button" data-action="comparison-host" data-host="${esc(id)}" data-selection="${esc(h.id)}" aria-pressed="${h.id === selected.id}">${h.id ? icon(h) : ""}<span>${esc(h.name)}</span><span class="pr-change-count" title="Package changes">${count(h) < 0 ? "—" : count(h)}</span></button>`
  ).join("")}</nav><section class="panel pr-comparison"><div class="panel-title"><h2>${esc(selected.name)}</h2></div>${missing.length ? `<div class="notice">Snapshots unavailable for: ${missing.map(esc).join(", ")}.</div>` : ""}${diffHTML(comparison)}</section></div>`;
}
function selectComparisonHost(id, value) {
  reviewSelections.set(id, value);
}

function diffHTML(d) {
  if (!d || d.status === "unavailable") return '<div class="notice">Snapshot unavailable for this comparison.</div>';
  if (d.status === "error") return `<div class="notice">${esc(d.error)}</div>`;
  if (d.status === "unchanged") return '<div class="notice">Configuration unchanged.</div>';
  return `<p class="subtitle">${d.packages.length} package changes · ${bytes(d.size_old)} → ${bytes(d.size_new)}</p>${d.packages.length ? `<div class="table-scroll"><table class="diff-table"><thead><tr><th>PACKAGE</th><th>BEFORE</th><th>AFTER</th></tr></thead><tbody>${d.packages.map(p => `<tr><td>${esc(p.name)}</td><td class="before">${esc(p.before || "—")}</td><td class="after">${esc(p.after || "—")}</td></tr>`).join("")}</tbody></table></div>` : '<div class="notice">Different configuration paths, no package changes.</div>'}`;
}
async function fillDiffs(generation) {
  await Promise.all(
    [...document.querySelectorAll(".diff")].map(async (el) => {
      try {
        const d = await request(
          `/api/ui/diff?old=${enc(el.dataset.old)}&new=${enc(el.dataset.new)}`,
        );
        if (generation !== renderID) return;
        el.innerHTML = diffHTML(d);
      } catch (e) {
        if (generation === renderID)
          el.innerHTML = `<div class="notice">${esc(e.message)}</div>`;
      }
    }),
  );
}
async function draw() {
  if (!state) state = { hosts: {}, repositories: {}, pull_requests: {} };
  const [part, ...ids] = location.pathname.slice(1).split("/");
  const route = part || "fleet";
  let id;
  try {
    id = decodeURIComponent(ids.join("/"));
  } catch {
    id = "";
  }
  if (route === "host" && state.hosts[id]) repository = state.hosts[id].repository;
  const generation = ++drawGeneration;
  ++renderID;
  const currentPath = location.pathname;
  const position = document.querySelector(".timeline-scroll")?.scrollTop || 0;
  try {
    if (route === "host" && state.hosts[id]) {
      if (!timelinePages.has(id)) $("#app").innerHTML = '<div class="loading">Loading…</div>';
      const page = await request(`/api/ui/hosts/${enc(id)}/timeline`);
      if (generation !== drawGeneration || location.pathname !== currentPath) return;
      acceptTimeline(id, page);
    } else if (route === "compare") {
      if (!commitComparisons.has(id)) $("#app").innerHTML = '<div class="loading">Loading…</div>';
      const data = await request(`/api/ui/compare/${ids.map(part => enc(decodeURIComponent(part))).join("/")}`);
      if (generation !== drawGeneration || location.pathname !== currentPath) return;
      commitComparisons.set(id, data);
      reviewSelections.delete(id);
      repository = data.repository;
    }
  } catch (error) {
    if (generation === drawGeneration && location.pathname === currentPath) $("#app").innerHTML = `<div class="empty">${esc(error.message)}</div>`;
    return;
  }
  document.title = `infra · ${route === "compare" ? "Comparison" : "Fleet"}`;
  const renderers = {
    fleet,
    host: () => hostView(id),
    compare: () => commitComparisonView(id),
  };
  $("#app").innerHTML = (renderers[route] || fleet)();
  const scroll = document.querySelector(".timeline-scroll");
  if (scroll) scroll.scrollTop = position;
  if (route !== "compare") fillDiffs(++renderID);
}
document.addEventListener("click", async (e) => {
  const tab = e.target.closest("[data-repository]");
  if (tab) {
    repository = tab.dataset.repository;
    draw();
    return;
  }
  const target = e.target.closest("[data-action]");
  if (target) {
    const { action, host } = target.dataset;
    if (action === "comparison-host") {
      selectComparisonHost(host, target.dataset.selection);
      const panel = document.querySelector(".pr-review");
      const position = panel.querySelector(".pr-host-list").scrollTop;
      panel.outerHTML = comparisonReview(commitComparisons.get(host), host);
      document.querySelector(".pr-host-list").scrollTop = position;
      const control = [...document.querySelectorAll(`[data-action="${action}"]`)].find(el => el.dataset.selection === target.dataset.selection);
      control?.focus({ preventScroll: true });
      return;
    }
    if (["compare-before", "compare-after", "compare-reset"].includes(action)) {
      const h = state.hosts[host];
      if (action === "compare-reset") comparisonSelections.delete(host);
      else comparisonSelections.set(host, { ...selectedComparison(h), [action.slice(8)]: target.dataset.path });
      const position = document.querySelector(".timeline-scroll").scrollTop;
      document.querySelector(".timeline-panel").outerHTML = configurationTimeline(h);
      document.querySelector(".timeline-scroll").scrollTop = position;
      document.querySelector(".comparison-panel").outerHTML = comparisonPanel(h);
      fillDiffs(++renderID);
      const control = [...document.querySelectorAll("[data-action]")].find(el => el.dataset.action === action && el.dataset.path === target.dataset.path);
      control?.focus({ preventScroll: true });
      return;
    }
    if (action === "timeline-more") {
      target.disabled = true;
      const currentPath = location.pathname;
      const position = document.querySelector(".timeline-scroll")?.scrollTop || 0;
      try {
        await loadMoreTimeline(host);
        if (location.pathname !== currentPath) return;
        const panel = document.querySelector(".timeline-panel");
        if (panel) {
          panel.outerHTML = configurationTimeline(timelinePages.get(host).host);
          document.querySelector(".timeline-scroll").scrollTop = position;
        }
      } catch (error) { toast(error.message); target.disabled = false; }
      return;
    }
    target.disabled = true;
    try {
      if (action !== "retry") return;
      await request(`/api/ui/hosts/${enc(host)}/retry`, {});
      toast("Retry queued.");
      await refresh();
    } catch (error) {
      toast(error.message);
      target.disabled = false;
    }
    return;
  }
  const anchor = e.target.closest("a");
  const row = e.target.closest("[data-href]");
  const href = anchor?.getAttribute("href") || row?.dataset.href;
  if (href?.startsWith("/") && !href.startsWith("//") && !e.ctrlKey && !e.metaKey && !e.shiftKey && !e.altKey && e.button === 0) {
    e.preventDefault();
    history.pushState(null, "", href);
    refresh();
    window.scrollTo(0, 0);
  }
});
window.addEventListener("popstate", () => {
  refresh();
  window.scrollTo(0, 0);
});
refresh();
setInterval(() => {
  if (location.pathname.startsWith("/compare/")) return;
  if (
    document.activeElement?.tagName === "INPUT" ||
    document.activeElement?.tagName === "SELECT"
  )
    return;
  refresh(false);
}, 10000);
