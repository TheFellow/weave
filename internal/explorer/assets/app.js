(function () {
  "use strict";

  const basePath = new URL(".", window.location.href).pathname;
  const elements = {
    form: document.getElementById("search-form"),
    target: document.getElementById("target"),
    direction: document.getElementById("direction"),
    depth: document.getElementById("max-depth"),
    limit: document.getElementById("limit"),
    maxEdges: document.getElementById("max-edges"),
    kinds: document.getElementById("kinds"),
    providers: document.getElementById("providers"),
    evidence: document.getElementById("evidence"),
    apply: document.getElementById("apply"),
    clear: document.getElementById("clear-filters"),
    back: document.getElementById("back"),
    forward: document.getElementById("forward"),
    resetZoom: document.getElementById("reset-zoom"),
    focus: document.getElementById("focus-label"),
    meta: document.getElementById("graph-meta"),
    notice: document.getElementById("notice"),
    loading: document.getElementById("loading"),
    error: document.getElementById("error"),
    status: document.getElementById("status"),
    selectionEmpty: document.getElementById("selection-empty"),
    selectionDetail: document.getElementById("selection-detail"),
    refocusSelected: document.getElementById("refocus-selected"),
    linkList: document.getElementById("link-list"),
    linkEditor: document.getElementById("link-editor"),
    linkEditorTitle: document.getElementById("link-editor-title"),
    linkForm: document.getElementById("link-form"),
    linkID: document.getElementById("link-id"),
    linkFrom: document.getElementById("link-from"),
    linkTo: document.getElementById("link-to"),
    linkKind: document.getElementById("link-kind"),
    linkNote: document.getElementById("link-note"),
    newLink: document.getElementById("new-link"),
    cancelLink: document.getElementById("cancel-link"),
    useSelectedFrom: document.getElementById("use-selected-from"),
    useSelectedTo: document.getElementById("use-selected-to"),
    removeDialog: document.getElementById("remove-dialog"),
    removeDescription: document.getElementById("remove-description"),
    cancelRemove: document.getElementById("cancel-remove"),
    confirmRemove: document.getElementById("confirm-remove")
  };

  let renderer;
  let requestSerial = 0;
  let detailSerial = 0;
  let activeRequest;
  let activeDetailRequest;
  let lastResult;
  let lastRequest;
  let resizeTimer;
  let renderedWidth = 0;
  let renderedHeight = 0;
  let entries = [];
  let entryIndex = -1;
  let selectedItem;
  let linkRevision = "";
  let links = [];
  let linkMode = "add";
  let linkEditorRevision = "";
  let pendingRemoval;
  const reducedMotion = window.matchMedia("(prefers-reduced-motion: reduce)");

  function setBusy(busy, message) {
    elements.loading.hidden = !busy;
    if (message) elements.loading.lastElementChild.textContent = message;
    elements.status.textContent = busy ? "Refreshing bounded graph…" : "Current · select a node or edge for evidence";
  }

  function showError(error) {
    elements.error.textContent = error instanceof Error ? error.message : String(error);
    elements.error.hidden = false;
    setBusy(false);
    elements.status.textContent = "View failed";
  }

  function selected(select) {
    return Array.from(select.selectedOptions, option => option.value);
  }

  function integerValue(element) {
    return Number.parseInt(element.value, 10);
  }

  function requestFromControls(target) {
    return {
      target: target.trim(),
      direction: elements.direction.value,
      max_depth: integerValue(elements.depth),
      limit: integerValue(elements.limit),
      max_edges: integerValue(elements.maxEdges),
      kinds: selected(elements.kinds),
      providers: selected(elements.providers),
      evidence: selected(elements.evidence)
    };
  }

  function copyRequest(request) {
    return JSON.parse(JSON.stringify(request));
  }

  function updateHistoryButtons() {
    elements.back.disabled = entryIndex <= 0;
    elements.forward.disabled = entryIndex < 0 || entryIndex >= entries.length - 1;
  }

  function pushHistory(request) {
    entries = entries.slice(0, entryIndex + 1);
    entries.push(copyRequest(request));
    entryIndex = entries.length - 1;
    updateHistoryButtons();
  }

  function fillControls(request) {
    elements.target.value = request.target;
    elements.direction.value = request.direction;
    elements.depth.value = request.max_depth;
    elements.limit.value = request.limit;
    elements.maxEdges.value = request.max_edges;
    selectValues(elements.kinds, request.kinds || []);
    selectValues(elements.providers, request.providers || []);
    selectValues(elements.evidence, request.evidence || []);
  }

  function selectValues(select, values) {
    const wanted = new Set(values);
    for (const option of select.options) option.selected = wanted.has(option.value);
  }

  function updateOptions(select, values, selectedValues) {
    const selectedSet = new Set(selectedValues || []);
    const complete = Array.from(new Set([...(values || []), ...selectedSet])).sort();
    select.replaceChildren(...complete.map(value => {
      const option = document.createElement("option");
      option.value = value;
      option.textContent = value;
      option.selected = selectedSet.has(value);
      return option;
    }));
  }

  async function fetchJSON(path, options) {
    const response = await fetch(basePath + path, {
      cache: "no-store",
      credentials: "same-origin",
      ...options,
      headers: { "Accept": "application/json", ...(options && options.headers) }
    });
    let body;
    try {
      body = await response.json();
    } catch (_) {
      throw new Error("The local explorer returned an unreadable response.");
    }
    if (!response.ok) {
      const error = new Error(body.error || `Local explorer request failed (${response.status}).`);
      error.status = response.status;
      throw error;
    }
    return body;
  }

  async function load(request, historyMode) {
    if (!request.target || !request.target.trim()) {
      showError(new Error("Enter a symbol, file, or stable graph ID."));
      return;
    }
    const serial = ++requestSerial;
    if (activeRequest) activeRequest.abort();
    activeRequest = new AbortController();
    elements.error.hidden = true;
    setBusy(true, "Querying the current index…");
    try {
      const result = await fetchJSON("api/graph", {
        method: "POST",
        signal: activeRequest.signal,
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(request)
      });
      if (serial !== requestSerial) return;
      if (historyMode === "push") pushHistory(request);
      fillControls(request);
      lastResult = result;
      lastRequest = copyRequest(request);
      await render(result, request, serial, true);
    } catch (error) {
      if (error.name !== "AbortError" && serial === requestSerial) showError(error);
    }
  }

  function render(result, request, serial, allowMotion) {
    const graph = document.getElementById("graph");
    graph.classList.toggle("compact", result.nodes.length <= 3);
    elements.focus.textContent = result.focus_label || result.focus;
    elements.meta.textContent = `${result.nodes.length} nodes · ${result.edges.length} visual edges · ${request.direction} · depth ${request.max_depth}`;
    const notices = [];
    if (result.truncated) notices.push("Bounded result: additional nodes or edges were omitted.");
    if (result.diagnostics && result.diagnostics.length) notices.push(result.diagnostics.join("\n"));
    elements.notice.textContent = notices.join("\n");
    elements.notice.hidden = notices.length === 0;
    updateOptions(elements.kinds, result.options.kinds, request.kinds);
    updateOptions(elements.providers, result.options.providers, request.providers);
    updateOptions(elements.evidence, result.options.evidence, request.evidence);

    const animate = allowMotion && !reducedMotion.matches && result.nodes.length <= 250;
    renderer
      .keyMode("id")
      .fade(animate)
      .growEnteringEdges(animate)
      .tweenPaths(animate)
      .tweenShapes(animate)
      .transition(function () {
        return d3.transition("weave-graph").duration(animate ? 440 : 0).ease(d3.easeCubicInOut);
      });

    return new Promise(resolve => {
      const timeout = window.setTimeout(() => {
        if (serial === requestSerial) showError(new Error("Graphviz did not finish rendering this bounded view within 30 seconds."));
        resolve();
      }, 30000);
      renderer.renderDot(result.dot, function () {
        window.clearTimeout(timeout);
        if (serial === requestSerial) {
          bindGraphItems(result.nodes, result.edges);
          markSelection();
          setBusy(false);
        }
        resolve();
      });
    });
  }

  function bindGraphItems(nodes, edgeGroups) {
    for (const node of nodes) {
      const element = document.getElementById(node.svg_id);
      if (!element) continue;
      element.setAttribute("role", "button");
      element.setAttribute("tabindex", "0");
      element.setAttribute("aria-label", `Inspect ${node.label}. Use Refocus graph to navigate.`);
      element.onclick = () => selectNode(node);
      element.ondblclick = () => refocus(node.id);
      element.onkeydown = event => {
        if (event.key === "Enter" || event.key === " ") {
          event.preventDefault();
          selectNode(node);
        }
      };
    }
    for (const edgeGroup of edgeGroups || []) {
      const element = document.getElementById(edgeGroup.svg_id);
      if (!element || !edgeGroup.facts || edgeGroup.facts.length === 0) continue;
      const fact = edgeGroup.facts[0];
      element.setAttribute("role", "button");
      element.setAttribute("tabindex", "0");
      element.setAttribute("aria-label", `Inspect ${fact.kind} relationship from ${fact.from} to ${fact.to}`);
      element.onclick = () => selectEdge(edgeGroup);
      element.onkeydown = event => {
        if (event.key === "Enter" || event.key === " ") {
          event.preventDefault();
          selectEdge(edgeGroup);
        }
      };
    }
  }

  function selectNode(node) {
    selectedItem = { kind: "node", id: node.id, svgID: node.svg_id, label: node.label };
    markSelection();
    elements.refocusSelected.disabled = false;
    elements.useSelectedFrom.disabled = false;
    elements.useSelectedTo.disabled = false;
    loadDetail({ target: node.id, limit: 64, context_lines: 2, max_source_bytes: 65536 });
  }

  function selectEdge(group) {
    const fact = group.facts[0];
    selectedItem = { kind: "edge", id: fact.id, svgID: group.svg_id, label: fact.kind, fact: fact };
    markSelection();
    elements.refocusSelected.disabled = true;
    elements.useSelectedFrom.disabled = true;
    elements.useSelectedTo.disabled = true;
    loadDetail({ target: fact.from, edge_id: fact.id, limit: 512, context_lines: 2, max_source_bytes: 65536 }, fact, group.facts.length);
  }

  function markSelection() {
    document.querySelectorAll("#graph .selected").forEach(element => element.classList.remove("selected"));
    if (!selectedItem) return;
    const element = document.getElementById(selectedItem.svgID);
    if (element) element.classList.add("selected");
  }

  function refocus(target) {
    load(requestFromControls(target), "push");
  }

  async function loadDetail(request, fallbackEdge, factCount) {
    const serial = ++detailSerial;
    if (activeDetailRequest) activeDetailRequest.abort();
    activeDetailRequest = new AbortController();
    elements.selectionEmpty.hidden = true;
    if (fallbackEdge) {
      renderEdgeFallback(fallbackEdge, factCount, "Loading current source evidence…");
    } else {
      elements.selectionDetail.replaceChildren(paragraph("Loading current source evidence…", "hint"));
    }
    try {
      const result = await fetchJSON("api/context", {
        method: "POST",
        signal: activeDetailRequest.signal,
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(request)
      });
      if (serial === detailSerial) renderDetail(result);
    } catch (error) {
      if (error.name !== "AbortError" && serial === detailSerial) {
        if (fallbackEdge) {
          renderEdgeFallback(fallbackEdge, factCount, error.message);
        } else {
          elements.selectionDetail.replaceChildren(paragraph(error.message, "source-status"));
        }
      }
    }
  }

  function renderEdgeFallback(edge, factCount, status) {
    const fragment = document.createDocumentFragment();
    fragment.appendChild(heading(`${edge.kind}: ${edge.from} → ${edge.to}`));
    fragment.appendChild(detailGrid([
      ["edge", edge.id], ["facts", factCount > 1 ? factCount : ""],
      ["provider", edge.provider], ["evidence", edge.evidence],
      ["document", edge.document_id], ["range", edge.document_id ? formatRange(edge.range) : ""]
    ]));
    fragment.appendChild(paragraph(status, "source-status"));
    elements.selectionDetail.replaceChildren(fragment);
  }

  function renderDetail(result) {
    const fragment = document.createDocumentFragment();
    if (result.kind === "edge" && result.relationship) {
      const relationship = result.relationship;
      fragment.appendChild(heading(`${relationship.edge.kind}: ${relationship.edge.from} → ${relationship.edge.to}`));
      fragment.appendChild(detailGrid([
        ["edge", relationship.edge.id], ["provider", relationship.edge.provider],
        ["evidence", relationship.edge.evidence], ["document", relationship.document && relationship.document.path],
        ["range", formatRange(relationship.edge.range)], ["repository", repositoryText(relationship.repositories)]
      ]));
      fragment.appendChild(sourceCard(relationship.source));
    } else if (result.context) {
      const context = result.context;
      const symbol = context.focus.symbol;
      fragment.appendChild(heading(symbol.display_name || symbol.stable_name || symbol.id));
      fragment.appendChild(detailGrid([
        ["id", symbol.id], ["stable", symbol.stable_name], ["kind", symbol.kind],
        ["provider", symbol.provider], ["evidence", symbol.evidence],
        ["repository", repositoryText(context.focus.repositories)]
      ]));
      for (const evidence of context.evidence || []) {
        const card = document.createElement("article");
        card.className = "evidence-card";
        const header = document.createElement("header");
        header.textContent = `${evidence.role} · ${evidence.provider}${evidence.evidence ? ` · ${evidence.evidence}` : ""}`;
        card.appendChild(header);
        card.appendChild(detailGrid([
          ["path", evidence.document && evidence.document.path || evidence.source && evidence.source.path],
          ["range", formatRange(evidence.range)], ["repository", repositoryText(evidence.repositories)]
        ]));
        card.appendChild(sourceCard(evidence.source));
        fragment.appendChild(card);
      }
      if (!context.evidence || context.evidence.length === 0) fragment.appendChild(paragraph("This entity has no source-backed occurrence in the bounded result.", "hint"));
    }
    elements.selectionDetail.replaceChildren(fragment);
  }

  function heading(text) {
    const value = document.createElement("h3");
    value.className = "detail-title";
    value.textContent = text;
    return value;
  }

  function paragraph(text, className) {
    const value = document.createElement("p");
    if (className) value.className = className;
    value.textContent = text;
    return value;
  }

  function detailGrid(rows) {
    const list = document.createElement("dl");
    list.className = "detail-grid";
    for (const [name, value] of rows) {
      if (value === undefined || value === null || value === "") continue;
      const term = document.createElement("dt");
      term.textContent = name;
      const detail = document.createElement("dd");
      detail.textContent = String(value);
      list.append(term, detail);
    }
    return list;
  }

  function sourceCard(source) {
    if (!source) return paragraph("No source range is attached to this fact.", "source-status");
    if (source.status !== "current" || !source.lines || source.lines.length === 0) {
      return paragraph(`${source.status}${source.detail ? ` · ${source.detail}` : ""}`, "source-status");
    }
    const pre = document.createElement("pre");
    pre.className = "source";
    pre.textContent = source.lines.map(line => `${String(line.number).padStart(5, " ")}  ${line.text}`).join("\n");
    return pre;
  }

  function formatRange(range) {
    if (!range || !range.start || !range.end) return "";
    return `${range.start.line + 1}:${range.start.column + 1}–${range.end.line + 1}:${range.end.column + 1}`;
  }

  function repositoryText(repositories) {
    return (repositories || []).map(repository => `${repository.identity} · ${repository.worktree_id} · ${repository.root}`).join("\n");
  }

  async function loadLinks() {
    try {
      const result = await fetchJSON("api/links", { method: "GET" });
      linkRevision = result.revision;
      links = result.links || [];
      renderLinks();
    } catch (error) {
      elements.linkList.replaceChildren(paragraph(error.message, "source-status"));
    }
  }

  function renderLinks() {
    if (links.length === 0) {
      elements.linkList.replaceChildren(paragraph("No authored contextual relationships yet.", "hint"));
      return;
    }
    const rows = links.map(link => {
      const row = document.createElement("article");
      row.className = "link-row";
      const title = document.createElement("strong");
      title.textContent = link.id;
      const relation = document.createElement("code");
      relation.textContent = `${link.from}\n${link.kind} → ${link.to}`;
      const actions = document.createElement("div");
      actions.className = "link-actions";
      const edit = document.createElement("button");
      edit.type = "button";
      edit.textContent = "Edit";
      edit.onclick = () => openLinkEditor(link);
      const remove = document.createElement("button");
      remove.type = "button";
      remove.textContent = "Remove";
      remove.onclick = () => confirmRemoval(link);
      actions.append(edit, remove);
      row.append(title, relation, actions);
      return row;
    });
    elements.linkList.replaceChildren(...rows);
  }

  function openLinkEditor(link) {
    linkMode = link ? "update" : "add";
    linkEditorRevision = linkRevision;
    elements.linkEditorTitle.textContent = link ? `Edit ${link.id}` : "New contextual link";
    elements.linkID.value = link ? link.id : "";
    elements.linkID.disabled = Boolean(link);
    elements.linkFrom.value = link ? link.from : selectedEndpoint();
    elements.linkTo.value = link ? link.to : "";
    elements.linkKind.value = link ? link.kind : "documents";
    elements.linkNote.value = link ? (link.note || "") : "";
    elements.linkEditor.hidden = false;
    (link ? elements.linkFrom : elements.linkID).focus();
  }

  function closeLinkEditor() {
    elements.linkEditor.hidden = true;
    elements.linkID.disabled = false;
    linkEditorRevision = "";
  }

  function selectedEndpoint() {
    return selectedItem && selectedItem.kind === "node" ? `entity:${selectedItem.id}` : "";
  }

  async function saveLink(event) {
    event.preventDefault();
    const payload = {
      id: elements.linkID.value, revision: linkEditorRevision,
      from: elements.linkFrom.value.trim(), to: elements.linkTo.value.trim(),
      kind: elements.linkKind.value, note: elements.linkNote.value
    };
    const method = linkMode === "add" ? "POST" : "PUT";
    try {
      await mutateLink(method, payload);
      closeLinkEditor();
    } catch (_) {}
  }

  async function mutateLink(method, payload) {
    try {
      await fetchJSON("api/links", {
        method: method,
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(payload)
      });
      await loadLinks();
      if (lastRequest) await load(copyRequest(lastRequest), "replace");
    } catch (error) {
      if (error.status === 409) await loadLinks();
      showError(error);
      throw error;
    }
  }

  function confirmRemoval(link) {
    pendingRemoval = { link: link, revision: linkRevision };
    elements.removeDescription.textContent = `Remove exact link “${link.id}” (${link.kind})?`;
    elements.removeDialog.showModal();
    elements.cancelRemove.focus();
  }

  function initializeRenderer() {
    if (!window.d3 || typeof d3.select("#graph").graphviz !== "function") throw new Error("Embedded Graphviz assets did not initialize.");
    const graph = document.getElementById("graph");
    renderedWidth = graph.clientWidth;
    renderedHeight = graph.clientHeight;
    renderer = d3.select("#graph").graphviz({
      useWorker: true,
      useSharedWorker: false,
      keyMode: "id",
      zoom: true,
      fit: true,
      width: renderedWidth,
      height: renderedHeight
    });
    renderer.onerror(message => showError(new Error(`Graphviz could not render this view: ${message}`)));
    if (window.ResizeObserver) {
      new ResizeObserver(() => {
        window.clearTimeout(resizeTimer);
        resizeTimer = window.setTimeout(() => {
          if (!renderer || !lastResult || !lastRequest) return;
          const width = graph.clientWidth;
          const height = graph.clientHeight;
          if (width === renderedWidth && height === renderedHeight) return;
          renderedWidth = width;
          renderedHeight = height;
          renderer.width(width).height(height).fit(true);
          render(lastResult, lastRequest, requestSerial, false);
        }, 160);
      }).observe(graph);
    }
  }

  elements.form.addEventListener("submit", event => {
    event.preventDefault();
    load(requestFromControls(elements.target.value), "push");
  });
  elements.apply.addEventListener("click", () => load(requestFromControls(elements.target.value), "push"));
  elements.clear.addEventListener("click", () => {
    selectValues(elements.kinds, []);
    selectValues(elements.providers, []);
    selectValues(elements.evidence, []);
  });
  elements.back.addEventListener("click", () => {
    if (entryIndex <= 0) return;
    entryIndex -= 1;
    updateHistoryButtons();
    load(copyRequest(entries[entryIndex]), "replace");
  });
  elements.forward.addEventListener("click", () => {
    if (entryIndex >= entries.length - 1) return;
    entryIndex += 1;
    updateHistoryButtons();
    load(copyRequest(entries[entryIndex]), "replace");
  });
  elements.resetZoom.addEventListener("click", () => renderer && renderer.resetZoom());
  elements.refocusSelected.addEventListener("click", () => {
    if (selectedItem && selectedItem.kind === "node") refocus(selectedItem.id);
  });
  elements.newLink.addEventListener("click", () => openLinkEditor());
  elements.cancelLink.addEventListener("click", closeLinkEditor);
  elements.useSelectedFrom.addEventListener("click", () => { elements.linkFrom.value = selectedEndpoint(); });
  elements.useSelectedTo.addEventListener("click", () => { elements.linkTo.value = selectedEndpoint(); });
  elements.linkForm.addEventListener("submit", saveLink);
  elements.cancelRemove.addEventListener("click", () => elements.removeDialog.close());
  elements.confirmRemove.addEventListener("click", async () => {
    if (!pendingRemoval) return;
    const removal = pendingRemoval;
    pendingRemoval = undefined;
    elements.removeDialog.close();
    try { await mutateLink("DELETE", { id: removal.link.id, revision: removal.revision }); } catch (_) {}
  });
  window.addEventListener("beforeunload", () => {
    if (activeRequest) activeRequest.abort();
    if (activeDetailRequest) activeDetailRequest.abort();
    if (renderer && typeof renderer.destroy === "function") renderer.destroy();
  });

  async function start() {
    try {
      initializeRenderer();
      const config = await fetchJSON("api/config", { method: "GET" });
      const initial = config.initial;
      updateOptions(elements.kinds, [], initial.kinds || []);
      updateOptions(elements.providers, [], initial.providers || []);
      updateOptions(elements.evidence, [], initial.evidence || []);
      updateOptions(elements.linkKind, config.edge_kinds || [], ["documents"]);
      fillControls(initial);
      await Promise.all([load(initial, "push"), loadLinks()]);
    } catch (error) {
      showError(error);
    }
  }

  start();
}());
