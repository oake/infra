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
const hostPath = id => id.split("/").map(enc).join("/");
const values = (o) => Object.values(o || {});
const short = (s) => (s ? s.slice(0, 7) : "—");
const link = (kind, id) => `/${kind}/${id.split("/").map(enc).join("/")}`;
const compareLink = (repo, old, next) => `/compare/${repo.split("/").map(enc).join("/")}/${enc(old)}/${enc(next)}`;
const commitURL = (c) => `https://github.com/${c.repository.split("/").map(enc).join("/")}/commit/${enc(c.revision)}`;
const timelinePages = new Map();
const configurationMetadata = new Map();
const commitComparisons = new Map();
const reviewSelections = new Map();
let drawGeneration = 0;
let packageBlocks = {};
const diffCache = new Map();
const diffKey = (old, next) => JSON.stringify([old, next]);
let state,
  repository = "",
  renderID = 0;
try { repository = localStorage.getItem("infra.repository") || ""; } catch {}
function rememberRepository() {
  if (!repository) return;
  try { localStorage.setItem("infra.repository", repository); } catch {}
}
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
const bytes = value => {
  const n = Math.abs(value), units = ["B", "KiB", "MiB", "GiB", "TiB"];
  let unit = 0;
  while (unit < 4 && n / 1024 ** unit >= 1024) unit++;
  return (n / 1024 ** unit).toLocaleString(undefined, {maximumFractionDigits: unit ? 1 : 0}) + " " + units[unit];
};
function sizeBadge(d) {
  if (!d || !["different", "unchanged"].includes(d.status)) return "";
  const delta = d.size_new - d.size_old;
  return `<span class="badge ${delta > 0 ? "red" : delta < 0 ? "green" : ""}" title="Closure size difference">${delta > 0 ? "+" : delta < 0 ? "−" : ""}${bytes(delta)}</span>`;
}
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
const statusLabel = s => ({"Deployment queued": "Deploying", "Reboot queued": "Rebooting"}[s] || s);
const busyStatus = s => s === "Deployment queued" || s === "Reboot queued";
const badge = (s, t = tone(s)) =>
  `<span class="badge ${esc(t)} ${busyStatus(s) ? "badge-busy" : ""}">${esc(statusLabel(s))}</span>`;
const hostStatusBadge = h => {
  const reboot = h.status === "Reboot to apply" && h.platform !== "darwin";
  if (h.status !== "Paused" && !reboot) return badge(h.status);
  return `<button class="badge unpause-action ${reboot ? "purple" : ""}" data-action="${reboot ? "reboot" : "deploy"}" data-host="${esc(h.id)}" aria-label="${reboot ? "Reboot now" : "Unpause deployment"}"><span class="paused-label">${esc(h.status)}</span><span class="unpause-label" aria-hidden="true">${reboot ? "Reboot now" : "Unpause"}</span></button>`;
};
const stagedBadge = h => ["Reboot to apply", "Reboot queued"].includes(h.status) ? hostStatusBadge(h) : badge("Reboot to apply");

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
  return c ? `<a class="configuration-title" href="${commitURL(c)}">${esc(c.title)}</a><time class="configuration-date">${date(c.created)}${c.offBranch ? ` <span class="branch-warning">! ${esc(c.branch || "branch unavailable")}</span>` : ""}</time>` : '<span class="muted">Unknown</span>';
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
  if (location.pathname.startsWith("/host/")) { if (render) await draw(); return; }
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
function orderedRepositories() {
  const username = (state.username || "").toLowerCase();
  const matches = repo => !!username && repo.id.toLowerCase().includes(username);
  return values(state.repositories).sort((a, b) =>
    Number(matches(b)) - Number(matches(a)) || a.id.localeCompare(b.id),
  );
}
function fleet() {
  const repos = orderedRepositories();
  if (!repos.some(r => r.id === repository)) repository = repos[0]?.id || "";
  return `${pullRequestCards(repository)}<div id="host-tables">${hostTables()}</div>`;
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
    output += `<section class="repo-block"><div class="table-scroll"><table class="fleet-table"><thead><tr><th>Host</th><th>Status</th><th>Configuration</th></tr></thead><tbody>${hosts.map((h) => `<tr class="host-row" data-href="${link("host", h.id)}"><td><a class="host-cell" href="${link("host", h.id)}"><span class="host-logo">${icon(h)}</span><div><span class="host-name">${esc(h.name)}</span></div></a></td><td>${hostStatusBadge(h)}</td><td>${configurations(h)}</td></tr>`).join("")}</tbody></table></div></section>`;
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
  for (const entry of entries) metadata.set(entry.path, { ...entry.commit, offBranch: entry.offBranch });
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
    const page = await request(`/api/ui/hosts/${hostPath(id)}/timeline?cursor=${enc(previous.next_cursor)}`);
    if (timelinePages.get(id)?.version === previous.version) acceptTimeline(id, page, true);
  } catch (error) {
    if (error.status !== 409) throw error;
    const page = await request(`/api/ui/hosts/${hostPath(id)}/timeline${location.search}`);
    acceptTimeline(id, page);
  }
}
function selectedComparison(h) {
  const pair = { ...defaultComparison(h) };
  const params = new URLSearchParams(location.search);
  for (const side of ["before", "after"]) {
    const path = params.get(side);
    if (path && /^\/nix\/store\/[a-z0-9]{32}-[^/]+$/.test(path)) pair[side] = path;
  }
  return pair;
}
function comparisonControls(h, path) {
  const pair = selectedComparison(h);
  return `<div class="comparison-controls" aria-label="Use configuration in comparison">${["before", "after"].map(side => `<button type="button" class="compare-pick ${side}" data-action="compare-${side}" data-host="${esc(h.id)}" data-path="${esc(path)}" aria-pressed="${pair[side] === path}" aria-label="Use as ${side}">${side === "before" ? "Before" : "After"}</button>`).join("")}</div>`;
}
function comparisonPanel(h) {
  const pair = selectedComparison(h);
  const before = firstConfiguration(h, pair.before);
  const after = firstConfiguration(h, pair.after);
  const allHosts = before?.revision && after?.revision
    ? `<a class="compare-all-hosts" href="${compareLink(h.repository, before.revision, after.revision)}">All hosts →</a>` : "";
  return `<section class="panel comparison-panel"><div class="panel-title"><div class="comparison-heading"><h2>Comparison</h2>${allHosts}</div><div class="actions"><span class="comparison-size">${sizeBadge(diffCache.get(diffKey(pair.before, pair.after)))}</span></div></div><div class="comparison-pair">${["before", "after"].map(side => `<div class="comparison-endpoint ${side}"><span class="comparison-side">${side === "before" ? "Before" : "After"}</span>${pair[side] ? configuration(h, pair[side]) : '<span class="muted">No configuration available</span>'}</div>`).join('<span class="comparison-direction" aria-hidden="true">→</span>')}</div>${pair.before && pair.after ? `<div class="diff" aria-live="polite" data-old="${esc(pair.before)}" data-new="${esc(pair.after)}">${diffCache.has(diffKey(pair.before, pair.after)) ? diffHTML(diffCache.get(diffKey(pair.before, pair.after))) : tableSkeleton()}</div>` : '<p class="muted">Choose a Before and After configuration from the timeline.</p>'}</section>`;
}
function configurationTimeline(h) {
  const { entries, staged, unknown, unknown_staged, more } = timelineConfigurations(h);
  const target = h.desired && h.desired !== h.observation.active && entries.some(e => e.path === h.desired)
    ? h.desired : staged;
  const unknownEntries = (unknown_staged
    ? `<li class="is-unknown is-staged ${target === staged ? "is-destination" : ""}"><div class="timeline-entry"><span>unknown configuration</span>${stagedBadge(h)}</div></li>` : "") + (unknown
    ? `<li class="is-unknown is-live"><div class="timeline-entry"><strong>unknown configuration</strong>${badge("Live", "green")}</div></li>` : "");
  return `<section class="panel timeline-panel"><div class="panel-title"><h2>Configurations</h2></div><div class="timeline-scroll"><ol class="configuration-timeline">${unknownEntries}${entries.map(entry => {
    const c = entry.commit;
    const live = entry.path === h.observation.active;
    const isStaged = !live && entry.path === staged;
    let marker = live ? badge("Live", "green") : isStaged ? stagedBadge(h) : h.automatic && entry.path === h.desired ? hostStatusBadge(h) : "";
    const title = `<a href="${commitURL(c)}">${esc(c.title)}</a>`;
    const branch = entry.offBranch ? `<span class="branch-warning">! ${esc(c.branch || "branch unavailable")}</span>` : "";
    return `<li class="${live ? "is-live" : isStaged ? "is-staged" : ""} ${!live && entry.path === target ? "is-destination" : ""}"><div class="timeline-entry"><div>${live ? `<strong>${title}</strong>` : title}<div class="timeline-date"><time>${date(c.created)}</time>${branch}</div></div>${marker}</div>${comparisonControls(h, entry.path)}</li>`;
  }).join("")}${more ? `<li class="timeline-load-more">${button("Load more", "timeline-more", h.id, 'class="timeline-more"')}</li>` : ""}</ol></div></section>`;
}
let cardAlignmentObserver;
function alignHostCards() {
  cardAlignmentObserver?.disconnect();
  const columns = document.querySelector(".host-columns");
  const timeline = columns?.querySelector(".timeline-panel");
  const right = columns?.querySelector(".host-right");
  if (!timeline || !right) return;
  const align = () => {
    columns.classList.remove("align-card-bottoms");
    const comparison = right.querySelector(".comparison-panel");
    if (comparison && window.innerWidth > 900) {
      const shell = document.querySelector(".shell");
      const top = comparison.getBoundingClientRect().top - shell.getBoundingClientRect().top + shell.scrollTop;
      const bottomPadding = parseFloat(getComputedStyle(document.querySelector("#app")).paddingBottom);
      const preceding = comparison.getBoundingClientRect().top - right.getBoundingClientRect().top;
      const height = Math.max(240, Math.min(timeline.getBoundingClientRect().height - preceding, shell.clientHeight - top - bottomPadding));
      comparison.style.setProperty("--comparison-height", `${height}px`);
    } else comparison?.style.removeProperty("--comparison-height");
    const difference = timeline.getBoundingClientRect().height - right.getBoundingClientRect().height;
    columns.classList.toggle("align-card-bottoms", difference > 0 && difference <= 48);
  };
  cardAlignmentObserver = new ResizeObserver(align);
  cardAlignmentObserver.observe(timeline);
  for (const child of right.querySelectorAll(".panel > *")) cardAlignmentObserver.observe(child);
  align();
}
function animateTimeline() {
  alignHostCards();
  const list = document.querySelector(".configuration-timeline");
  if (!list) return;
  const entries = [...list.children];
  const start = entries.findIndex(e => e.classList.contains("is-live"));
  const end = entries.findIndex(e => e.classList.contains("is-destination"));
  if (start < 0 || end < 0 || start === end) return;
  for (let i = Math.min(start, end); i < Math.max(start, end); i++) {
    entries[i].classList.add("is-flowing", end < start ? "flow-up" : "flow-down");
  }
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
  return `<div class="page-heading"><div><h1 class="host-title"><span class="heading-icon">${icon(h)}</span>${esc(h.name)}</h1></div></div>` +
    `<div class="host-columns">${configurationTimeline(h)}<div class="host-right">${deploymentAttempt(h)}${comparisonPanel(h)}</div></div>`;
}
function prChecks(p) {
 return p.draft ? "Draft" : p.checks === "success" ? "Checks passed" : p.checks === "failure" ? "Checks failed" : p.checks === "pending" ? "Checks pending" : "Checks unknown";
}
const prStatusClass = p => p.draft ? "draft" : p.checks === "failure" ? "failed" : p.checks === "success" ? "passed" : "pending";
function prStatusDot(p) {
 return `<span class="pr-status-dot ${prStatusClass(p)}" role="img" aria-label="${esc(prChecks(p))}" title="${esc(prChecks(p))}"></span>`;
}
function pullRequestCards(repo) {
  const prs = values(state.pull_requests)
    .filter(p => p.repository === repo && p.state === "open")
    .sort((a, b) => b.number - a.number);
  const error = state.repositories[repo]?.pr_error;
  return (error ? `<div class="notice">${esc(error)}</div>` : "") + (prs.length ? `<div class="pull-request-cards">${prs.map(p =>
    `<a class="panel pr-card ${prStatusClass(p)}" title="${esc(prChecks(p))}" href="${compareLink(p.repository, p.base || p.base_head || state.repositories[p.repository].main, p.head)}"><div class="pr-heading"><h2>${esc(p.title)}</h2></div><p>#${p.number} &nbsp; · &nbsp; ${p.inputs.length} inputs${p.checks === "success" && p.evaluated ? ` &nbsp; · &nbsp; ${p.hosts.length} hosts` : ""}</p></a>`
  ).join("")}</div>` : "");
}
const inputTableHead = `<colgroup><col class="input-name-column"><col><col></colgroup><thead><tr><th>Input</th><th>Before</th><th>After</th></tr></thead>`;
function flakeInputs(p) {
  if (!p.inputs?.length) return "";
  const inputDate = (stamp, revision) => stamp ? new Date(stamp).toLocaleDateString(undefined, {year:"numeric", month:"short", day:"numeric"}) : revision ? "Date unavailable" : "—";
  return `<section class="panel"><div class="panel-title"><h2>flake.lock inputs</h2></div><div class="table-scroll"><table class="diff-table input-table">${inputTableHead}<tbody>${p.inputs.map(i => {
    const before = !i.before && i.after ? '<span class="badge green">+ Added</span>' : esc(inputDate(i.before_date, i.before));
    const after = i.before && !i.after ? '<span class="badge red">− Removed</span>' : esc(inputDate(i.after_date, i.after));
    const dates = `<span>${before}</span><span>${after}</span>`;
    const url = i.compare_url?.startsWith("https://github.com/") ? i.compare_url : "";
    return `<tr><td>${esc(i.name)}</td><td colspan="2" class="input-values">${url ? `<a class="input-compare input-dates" href="${esc(url)}" target="_blank" rel="noreferrer">${dates}</a>` : `<span class="input-dates">${dates}</span>`}</td></tr>`;
  }).join("")}</tbody></table></div></section>`;
}
const comparisonTitle = data => data?.pr_title || data?.head?.title || "Comparison";
function commitComparisonView(id) {
  const data = commitComparisons.get(id);
  if (!data) return '<div class="empty">Comparison not found.</div>';
  const commit = c => c ? `<a title="${esc(c.title || c.revision)}" href="${commitURL(c)}">${esc(c.title || short(c.revision))}</a>${c.created && !c.created.startsWith("0001") ? `<time class="configuration-date">${date(c.created)}${c.offBranch ? ` <span class="branch-warning">! ${esc(c.branch || "branch unavailable")}</span>` : ""}</time>` : ""}` : '<span class="muted">Commit unavailable</span>';
  const failures = data.failed_checks || [];
  const prNumber = /^https:\/\/github\.com\/[^/]+\/[^/]+\/pull\/(\d+)\/?$/.exec(data.github_url || "")?.[1];
  const githubIcon = `<svg width="20" height="20" viewBox="0 0 24 24" fill="currentColor" aria-hidden="true"><path d="M12 .3a12 12 0 0 0-3.8 23.4c.6.1.8-.3.8-.6v-2.3c-3.3.7-4-1.4-4-1.4-.5-1.4-1.3-1.8-1.3-1.8-1.1-.7.1-.7.1-.7 1.2.1 1.8 1.2 1.8 1.2 1.1 1.8 2.8 1.3 3.5 1 .1-.8.4-1.3.8-1.6-2.7-.3-5.5-1.3-5.5-5.9 0-1.3.5-2.4 1.2-3.2-.1-.3-.5-1.5.1-3.2 0 0 1-.3 3.3 1.2a11.5 11.5 0 0 1 6 0c2.3-1.5 3.3-1.2 3.3-1.2.6 1.7.2 2.9.1 3.2.8.8 1.2 1.9 1.2 3.2 0 4.6-2.8 5.6-5.5 5.9.4.4.8 1.1.8 2.2v3.4c0 .3.2.7.8.6A12 12 0 0 0 12 .3Z"/></svg>`;
  const failedCard = failures.length ? `<section class="panel pr-failed-checks"><div class="panel-title"><h2>Failed checks</h2></div><ul>${failures.map(c => `<li>${/^https?:\/\//.test(c.url || "") ? `<a href="${esc(c.url)}" target="_blank" rel="noreferrer">${esc(c.name)}</a>` : esc(c.name)}${c.summary ? `<p>${esc(c.summary)}</p>` : ""}</li>`).join("")}</ul></section>` : "";
  const metadata = flakeInputs(data) + failedCard;
  return `<div class="compare-heading"><div class="compare-title"><h1 class="pr-title"><span class="heading-icon">${prStatusDot(data)}</span>${esc(comparisonTitle(data))}</h1>${prNumber ? `<p class="pr-number">PR #${esc(prNumber)}</p>` : ""}</div><div class="compare-commit"><span class="comparison-side">Base</span>${commit(data.base)}</div><div class="compare-commit"><span class="comparison-side">Head</span>${commit(data.head)}</div><a class="github-button" href="${esc(data.github_url)}" target="_blank" rel="noreferrer">${githubIcon}GitHub</a></div>${data.git_error ? `<div class="notice">${esc(data.git_error)}</div>` : ""}${metadata ? `<div class="pr-metadata">${metadata}</div>` : ""}${comparisonReview(data, id)}`;
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
  ).join("")}</nav><section class="panel pr-comparison"><div class="panel-title"><h2>${esc(selected.name)}</h2>${sizeBadge(comparison)}</div>${missing.length ? `<div class="notice">Snapshots unavailable for: ${missing.map(esc).join(", ")}.</div>` : ""}${diffHTML(comparison)}</section></div>`;
}
function selectComparisonHost(id, value) {
  reviewSelections.set(id, value);
}

const skeletonLine = '<span class="skeleton-line"></span>';
function tableSkeleton(fleet = false) {
  const headers = fleet ? ["Host", "Status", "Configuration"] : ["Package", "Before", "After"];
  const configuration = `<div class="configuration"><div>${skeletonLine}<span class="configuration-date">${skeletonLine}</span></div></div>`;
  const cells = fleet
    ? `<td><div class="host-cell"><span class="host-logo skeleton-line"></span><span class="host-name">${skeletonLine}</span></div></td><td><span class="skeleton-badge">${skeletonLine}</span></td><td>${configuration}</td>`
    : `<td>${skeletonLine}</td><td>${skeletonLine}</td><td>${skeletonLine}</td>`;
  return `<div class="skeleton-table table-scroll" role="status" aria-label="Loading ${fleet ? "hosts" : "comparison"}" aria-busy="true"><table class="${fleet ? "fleet-table" : "diff-table"}"><thead><tr>${headers.map(label => `<th scope="col">${label}</th>`).join("")}</tr></thead><tbody>${Array.from({length: 6}, () => `<tr>${cells}</tr>`).join("")}</tbody></table></div>`;
}
function pageSkeleton(route) {
  const title = `<div class="skeleton-heading">${skeletonLine}</div>`;
  const panel = `<section class="panel">${title}${tableSkeleton()}</section>`;
  if (route === "host") return `<div class="page-skeleton" role="status" aria-label="Loading host" aria-busy="true"><div class="page-heading"><div><h1 class="skeleton-host-title">${skeletonLine}</h1></div></div><div class="host-columns"><section class="panel">${title}<div class="skeleton-timeline">${Array.from({length: 4}, () => `<div>${skeletonLine}${skeletonLine}<div class="skeleton-controls">${skeletonLine}${skeletonLine}</div></div>`).join("")}</div></section>${panel}</div></div>`;
  if (route === "compare") {
    const header = `<div class="compare-heading"><h1 class="pr-title skeleton-host-title"><span class="heading-icon"><span class="skeleton-status"></span></span>${skeletonLine}</h1>${["Base", "Head"].map(label => `<div class="compare-commit"><span class="comparison-side">${label}</span><div class="skeleton-commit-title">${skeletonLine}</div><time class="configuration-date skeleton-commit-date">${skeletonLine}</time></div>`).join("")}<span class="github-button skeleton-github" aria-hidden="true"><span class="skeleton-line"></span></span></div>`;
    const inputs = `<section class="panel"><div class="panel-title"><h2>flake.lock inputs</h2></div><table class="diff-table input-table">${inputTableHead}<tbody>${Array.from({length: 3}, () => `<tr><td>${skeletonLine}</td><td>${skeletonLine}</td><td>${skeletonLine}</td></tr>`).join("")}</tbody></table></section>`;
    return `<div class="page-skeleton" role="status" aria-label="Loading comparison" aria-busy="true">${header}<div class="pr-metadata">${inputs}</div><div class="pr-review"><div class="skeleton-hosts">${skeletonLine.repeat(5)}</div><section class="panel"><div class="panel-title"><h2>All hosts</h2></div>${tableSkeleton()}</section></div></div>`;
  }
  return `<div class="page-skeleton" role="status" aria-label="Loading hosts" aria-busy="true"><div class="pull-request-cards">${Array.from({length: 1}, () => `<div class="panel pr-card pr-card-skeleton"><div class="pr-heading"><h2>${skeletonLine}</h2></div><p>${skeletonLine}</p></div>`).join("")}</div>${tableSkeleton(true)}</div>`;
}
const packageVersion = value => value ? value.replaceAll("<none>", "no version").trim() : "—";
const packageChangeOrder = p => !p.before && p.after ? 0 : p.before && !p.after ? 1 : 2;
function diffHTML(d) {
  if (!d || d.status === "unavailable") return '<div class="notice">Snapshot unavailable for this comparison.</div>';
  if (d.status === "error") return `<div class="notice">${esc(d.error)}</div>`;
  if (d.status === "unchanged") return '<div class="notice">Configuration unchanged.</div>';
  return `${d.packages.length ? `<div class="table-scroll"><table class="diff-table"><thead><tr><th>Package</th><th>Before</th><th>After</th></tr></thead><tbody>${[...d.packages].sort((a, b) => Number(!!b.explicit) - Number(!!a.explicit) || packageChangeOrder(a) - packageChangeOrder(b) || a.name.localeCompare(b.name)).map(p => {
    const added = !p.before && !!p.after;
    const removed = !!p.before && !p.after;
    return `<tr><td>${p.explicit ? `<strong>${esc(p.name)}</strong>` : esc(p.name)}</td><td class="before">${added ? '<span class="badge green">+ Added</span>' : esc(packageVersion(p.before))}</td><td class="after">${removed ? '<span class="badge red">− Removed</span>' : esc(packageVersion(p.after))}</td></tr>`;
  }).join("")}</tbody></table></div>` : '<div class="notice">Different configuration paths, no package changes.</div>'}`;
}
async function fillDiffs(generation) {
  await Promise.all(
    [...document.querySelectorAll(".diff")].map(async (el) => {
      try {
        const key = diffKey(el.dataset.old, el.dataset.new);
        let d = diffCache.get(key);
        if (!d) {
          d = await request(`/api/ui/diff?old=${enc(el.dataset.old)}&new=${enc(el.dataset.new)}`);
          if (d.status !== "error") diffCache.set(key, d);
        }
        if (generation !== renderID) return;
        el.innerHTML = diffHTML(d);
        const size = el.closest(".comparison-panel")?.querySelector(".comparison-size");
        if (size) size.innerHTML = sizeBadge(d);
        alignHostCards();
        updateComparisonScroll();
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
  const currentPath = location.pathname;
  const position = document.querySelector(".timeline-scroll")?.scrollTop || 0;
  if (route === "host" && !timelinePages.has(id) || route === "compare" && !commitComparisons.has(id)) $("#app").innerHTML = pageSkeleton(route);
  try {
    if (!Object.keys(state.repositories).length && (route === "host" || route === "compare")) {
      const repos = await request("/api/ui/repositories");
      if (generation !== drawGeneration || location.pathname !== currentPath) return;
      state.repositories = Object.fromEntries(repos.map(id => [id, {id}]));
    }
    if (route === "host") {

      const page = await request(`/api/ui/hosts/${hostPath(id)}/timeline${location.search}`);
      if (generation !== drawGeneration || location.pathname !== currentPath) return;
      acceptTimeline(id, page);
      state.hosts[id] = page.host;
      repository = page.host.repository;
    } else if (route === "compare") {

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
  document.title = `infra · ${route === "compare" ? comparisonTitle(commitComparisons.get(id)) : "Hosts"}`;
  const renderers = {
    fleet,
    host: () => hostView(id),
    compare: () => commitComparisonView(id),
  };
  $("#app").innerHTML = (renderers[route] || fleet)();
  rememberRepository();
  animateTimeline();
  updateComparisonScroll();
  const switches = $("#repository-switches");
  if (switches) switches.innerHTML = orderedRepositories().filter(r => r.id !== repository).map(r => `<button data-repository="${esc(r.id)}" title="Switch to ${esc(r.id)}"><svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true"><path d="M4 7h16m-5-5 5 5-5 5M20 17H4m5-5-5 5 5 5"/></svg><span>${esc(repository)}</span></button>`).join("");
  const scroll = document.querySelector(".timeline-scroll");
  if (scroll) scroll.scrollTop = position;
  if (route !== "compare") fillDiffs(++renderID);
}
document.addEventListener("click", async (e) => {
  const tab = e.target.closest("[data-repository]");
  if (tab) {
    repository = tab.dataset.repository;
    rememberRepository();
    if (location.pathname !== "/") history.pushState({}, "", "/");
    refresh();
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
      updateComparisonScroll();
      document.querySelector(".pr-host-list").scrollTop = position;
      const control = [...document.querySelectorAll(`[data-action="${action}"]`)].find(el => el.dataset.selection === target.dataset.selection);
      control?.focus({ preventScroll: true });
      return;
    }
    if (["compare-before", "compare-after"].includes(action)) {
      const h = state.hosts[host];
      const pair = { ...selectedComparison(h), [action.slice(8)]: target.dataset.path };
      const defaults = defaultComparison(h);
      const url = new URL(location.href);
      const isDefault = pair.before === defaults.before && pair.after === defaults.after;
      for (const side of ["before", "after"]) {
        if (isDefault || !pair[side]) url.searchParams.delete(side);
        else url.searchParams.set(side, pair[side]);
      }
      history.replaceState(null, "", url.pathname + url.search + url.hash);
      const position = document.querySelector(".timeline-scroll").scrollTop;
      document.querySelector(".timeline-panel").outerHTML = configurationTimeline(h);
      animateTimeline();
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
          animateTimeline();
          document.querySelector(".timeline-scroll").scrollTop = position;
        }
      } catch (error) { toast(error.message); target.disabled = false; }
      return;
    }
    target.disabled = true;
    const previousHTML = target.innerHTML;
    const previousClass = target.className;
    if (action === "deploy" || action === "reboot") {
      target.className = `badge badge-busy ${action === "reboot" ? "purple" : ""}`;
      target.textContent = action === "reboot" ? "Rebooting" : "Deploying";
      target.setAttribute("aria-label", target.textContent);
    }
    try {
      if (!["retry", "deploy", "reboot"].includes(action)) return;
      await request(`/api/ui/hosts/${hostPath(host)}/${action}`, {});
      toast(action === "reboot" ? "Reboot queued." : action === "deploy" ? "Deployment approved." : "Retry queued.");
      await refresh();
    } catch (error) {
      toast(error.message);
      target.innerHTML = previousHTML;
      target.className = previousClass;
      if (action === "reboot") target.setAttribute("aria-label", "Reboot now");
      if (action === "deploy") target.setAttribute("aria-label", "Unpause deployment");
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
    document.querySelector(".shell").scrollTo(0, 0);
  }
});
function updateComparisonScroll() {
  const hosts = document.querySelector(".pr-host-list");
  if (hosts) {
    hosts.style.removeProperty("--host-list-height");
    if (window.innerWidth > 750 && hosts.scrollHeight > hosts.clientHeight) {
      const limit = hosts.clientHeight;
      const origin = hosts.getBoundingClientRect().top;
      const rows = [...hosts.children].map(row => ({top: row.getBoundingClientRect().top - origin + hosts.scrollTop, height: row.getBoundingClientRect().height}));
      const last = rows.findLast(row => row.top + row.height / 2 <= limit);
      if (last) hosts.style.setProperty("--host-list-height", `${last.top + last.height / 2}px`);
    }
  }
  const shell = document.querySelector(".shell");
  shell.classList.toggle("comparison-scroll-ready", !!document.querySelector(".pr-review, .comparison-panel") && shell.scrollHeight - shell.clientHeight - shell.scrollTop <= 2);
}
document.querySelector(".shell").addEventListener("scroll", updateComparisonScroll, {passive: true});
const comparisonScrollObserver = new ResizeObserver(updateComparisonScroll);
comparisonScrollObserver.observe(document.querySelector("#app"));
comparisonScrollObserver.observe(document.querySelector(".shell"));
window.addEventListener("popstate", () => {
  refresh();
  document.querySelector(".shell").scrollTo(0, 0);
});
const blockDialog = document.createElement("dialog");
blockDialog.className = "package-block-dialog";
document.body.append(blockDialog);
function renderBlockList() {
 blockDialog.querySelector(".block-list").innerHTML = Object.entries(packageBlocks).map(([id, r]) => `<li><span><strong>${esc(r.text)}</strong> <span class="muted">${esc({starts: "starts with", contains: "contains", exact: "exact"}[r.match])}</span></span><button type="button" data-remove-block="${esc(id)}">Delete</button></li>`).join("") || '<li class="muted">No blocked packages.</li>';
}
async function openPackageBlocks(text) {
 if (blockDialog.open) return;
 blockDialog.innerHTML = `<div class="panel-title"><h2 id="block-title">Blocked packages</h2><button type="button" data-close-blocks aria-label="Close">Close</button></div><p>Excluded from all package comparisons.</p><ul class="block-list"></ul><form><label for="block-text">Package name</label><input id="block-text" name="text" required maxlength="512" autocomplete="off" /><fieldset><legend>Match</legend>${[["starts", "starts with"], ["contains", "contains"], ["exact", "exact"]].map(([value,label]) => `<label><input type="radio" name="match" value="${value}" ${value === "exact" ? "checked" : ""} /> ${label}</label>`).join("")}</fieldset><p class="block-error" role="alert"></p><button type="submit">Add</button></form>`;
 blockDialog.setAttribute("aria-labelledby", "block-title");
 blockDialog.querySelector("#block-text").value = text;
 blockDialog.querySelector(".block-list").innerHTML = `<li role="status" aria-label="Loading blocked packages">${skeletonLine}</li>`;
 blockDialog.showModal();
 blockDialog.querySelector("#block-text").focus();
 try { packageBlocks = await request("/api/ui/package-blocks"); renderBlockList(); }
 catch (e) { blockDialog.querySelector(".block-error").textContent = e.message; }
}
async function updatePackageBlocks(path, body) {
 const controls = blockDialog.querySelectorAll("button");
 controls.forEach(b => b.disabled = true);
 try {
  await request(path, body);
  packageBlocks = await request("/api/ui/package-blocks");
  renderBlockList();
  diffCache.clear();
  const [route, ...ids] = location.pathname.slice(1).split("/");
  if (route === "compare") {
   const id = decodeURIComponent(ids.join("/"));
   const data = await request(`/api/ui/compare/${ids.map(part => enc(decodeURIComponent(part))).join("/")}`);
   commitComparisons.set(id, data);
   const review = document.querySelector(".pr-review");
   if (review) review.outerHTML = comparisonReview(data, id);
  } else { await fillDiffs(++renderID); }
  blockDialog.querySelector(".block-error").textContent = "";
  return true;
 } catch (e) { blockDialog.querySelector(".block-error").textContent = e.message; return false; }
 finally { controls.forEach(b => b.disabled = false); }
}
blockDialog.addEventListener("submit", async e => {
 e.preventDefault();
 const form = new FormData(e.target);
 if (await updatePackageBlocks("/api/ui/package-blocks", {text: form.get("text"), match: form.get("match")})) {
  blockDialog.querySelector("#block-text").value = "";
  blockDialog.querySelector("#block-text").focus();
 }
});
blockDialog.addEventListener("click", e => {
 if (e.target.closest("[data-close-blocks]")) blockDialog.close();
 const remove = e.target.closest("[data-remove-block]");
 if (remove) updatePackageBlocks(`/api/ui/package-blocks/${enc(remove.dataset.removeBlock)}/delete`, {});
});
document.addEventListener("keydown", e => {
 if (e.key.toLowerCase() !== "b" || e.ctrlKey || e.metaKey || e.altKey || e.repeat || blockDialog.open) return;
 if (e.target.closest("input, textarea, select, [contenteditable=true]")) return;
 if (!document.querySelector(".comparison-panel, .pr-comparison")) return;
 e.preventDefault();
 openPackageBlocks(window.getSelection()?.toString().trim() || "");
});
// Anchor newly rendered badges to the same document clock, including after
// fleet refreshes or optimistic button updates.
new MutationObserver(() => {
  for (const animation of document.getAnimations()) {
    if (animation.animationName === "badge-double-blink" && animation.startTime !== 0) animation.startTime = 0;
  }
}).observe($("#app"), {childList: true, subtree: true, attributes: true, attributeFilter: ["class"]});
$("#app").innerHTML = pageSkeleton(location.pathname.startsWith("/host/") ? "host" : location.pathname.startsWith("/compare/") ? "compare" : "fleet");
refresh();
setInterval(() => {
  if (document.hidden || location.pathname.startsWith("/host/") || location.pathname.startsWith("/compare/") || blockDialog.open) return;
  if (
    document.activeElement?.tagName === "INPUT" ||
    document.activeElement?.tagName === "SELECT"
  )
    return;
  refresh(false);
}, 10000);
