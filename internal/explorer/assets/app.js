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
    status: document.getElementById("status")
  };

  let renderer;
  let requestSerial = 0;
  let activeRequest;
  let lastResult;
  let lastRequest;
  let resizeTimer;
  let renderedWidth = 0;
  let renderedHeight = 0;
  let entries = [];
  let entryIndex = -1;
  const reducedMotion = window.matchMedia("(prefers-reduced-motion: reduce)");

  function setBusy(busy, message) {
    elements.loading.hidden = !busy;
    if (message) elements.loading.lastElementChild.textContent = message;
    elements.status.textContent = busy ? "Refreshing bounded graph…" : "Current · click any node to refocus";
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
    if (!response.ok) throw new Error(body.error || `Local explorer request failed (${response.status}).`);
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
    elements.meta.textContent = `${result.nodes.length} nodes · ${request.direction} · depth ${request.max_depth}`;
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
        if (serial === requestSerial) {
          showError(new Error("Graphviz did not finish rendering this bounded view within 30 seconds."));
        }
        resolve();
      }, 30000);
      renderer.renderDot(result.dot, function () {
        window.clearTimeout(timeout);
        if (serial === requestSerial) {
          bindNodes(result.nodes);
          setBusy(false);
        }
        resolve();
      });
    });
  }

  function bindNodes(nodes) {
    for (const node of nodes) {
      const element = document.getElementById(node.svg_id);
      if (!element) continue;
      element.setAttribute("role", "button");
      element.setAttribute("tabindex", "0");
      element.setAttribute("aria-label", `Refocus on ${node.label}`);
      const refocus = () => {
        const request = requestFromControls(node.id);
        load(request, "push");
      };
      // Graphviz preserves keyed DOM nodes across transitions. Assign handlers
      // instead of accumulating listeners every time the same node survives.
      element.onclick = refocus;
      element.onkeydown = event => {
        if (event.key === "Enter" || event.key === " ") {
          event.preventDefault();
          refocus();
        }
      };
    }
  }

  function initializeRenderer() {
    if (!window.d3 || typeof d3.select("#graph").graphviz !== "function") {
      throw new Error("Embedded Graphviz assets did not initialize.");
    }
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

  async function start() {
    try {
      initializeRenderer();
      const config = await fetchJSON("api/config", { method: "GET" });
      const initial = config.initial;
      updateOptions(elements.kinds, [], initial.kinds || []);
      updateOptions(elements.providers, [], initial.providers || []);
      updateOptions(elements.evidence, [], initial.evidence || []);
      fillControls(initial);
      await load(initial, "push");
    } catch (error) {
      showError(error);
    }
  }

  start();
}());
