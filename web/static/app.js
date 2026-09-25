// Small, dependency-free browser code. All server text is inserted with
// textContent (never innerHTML), so resume text cannot inject HTML.
(function () {
  "use strict";

  const $ = (sel, root = document) => root.querySelector(sel);
  const el = (tag, cls, text) => {
    const n = document.createElement(tag);
    if (cls) n.className = cls;
    if (text !== undefined) n.textContent = text;
    return n;
  };

  document.addEventListener("click", (e) => {
    if (e.target.closest("[data-reload]")) location.reload();
  });

  // ── Job page: watch progress without reloading ──────────────────────────
  const status = $("#job-status");
  if (status) {
    let shownDone = Number(status.dataset.done);
    const poll = async () => {
      try {
        const r = await fetch(`/jobs/${status.dataset.job}/status`);
        if (!r.ok) return;
        const s = await r.json();
        for (const k of ["total", "done", "pending", "failed"]) {
          const n = $(`[data-k="${k}"]`, status);
          if (n) n.textContent = s[k];
        }
        if (s.done !== shownDone) $("#new-results").classList.remove("hidden");
        if (s.pending === 0 && s.done === shownDone) return; // nothing left to watch
      } catch (_) { /* try again next tick */ }
      setTimeout(poll, 3000);
    };
    setTimeout(poll, 3000);
  }

  // ── Job page: chat over resumes (streamed answer) ───────────────────────
  const form = $("#chat-form");
  if (form) {
    const log = $("#chat-log");
    const input = $("#chat-q");
    const button = $("button", form);
    const history = [];

    document.querySelectorAll("[data-q]").forEach((b) =>
      b.addEventListener("click", () => { input.value = b.dataset.q; form.requestSubmit(); })
    );

    form.addEventListener("submit", async (e) => {
      e.preventDefault();
      const q = input.value.trim();
      if (!q) return;
      input.value = "";
      button.disabled = true;

      log.appendChild(el("div", "ml-8 rounded-lg bg-teal-600 px-3 py-2 text-white", q));
      const box = el("div", "space-y-2");
      const statusLine = el("p", "text-xs text-gray-500", "Thinking…");
      const answer = el("div", "whitespace-pre-wrap rounded-lg bg-gray-50 px-3 py-2 text-gray-800 ring-1 ring-gray-200 hidden");
      const sources = el("div", "hidden");
      box.append(statusLine, answer, sources);
      log.appendChild(box);
      log.scrollTop = log.scrollHeight;

      let text = "";
      try {
        const r = await fetch(`/jobs/${form.dataset.job}/chat`, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ question: q, history: history.slice(-4) }),
        });
        if (!r.ok) throw new Error(await r.text());
        await readSSE(r.body, (event, data) => {
          if (event === "status") statusLine.textContent = data;
          if (event === "error") { statusLine.textContent = data; statusLine.className = "text-xs text-rose-700"; }
          if (event === "sources") renderSources(sources, data);
          if (event === "token") {
            text += data;
            answer.textContent = clean(text);
            answer.classList.remove("hidden");
            log.scrollTop = log.scrollHeight;
          }
          if (event === "done") statusLine.remove();
        });
        if (text) history.push({ role: "user", content: q }, { role: "assistant", content: text });
      } catch (err) {
        statusLine.textContent = (err && err.message) || "Something went wrong.";
        statusLine.className = "text-xs text-rose-700";
      } finally {
        button.disabled = false;
        input.focus();
      }
    });
  }

  // Small models sometimes list people "with missing information" even when told
  // not to. Those lines add nothing, so hide them.
  const filler = /(missing information|no information|not mentioned|not in the shortlist|no mention)/i;
  function clean(t) {
    return t.split("\n").filter((l) => !(/^\s*[*\-•]/.test(l) && filler.test(l))).join("\n");
  }

  function renderSources(box, list) {
    if (!list || !list.length) return;
    box.classList.remove("hidden");
    const d = el("details", "text-xs");
    d.appendChild(el("summary", "cursor-pointer text-gray-500", `Sources: ${list.length} resume extracts`));
    const ul = el("ul", "mt-2 space-y-2");
    for (const s of list) {
      const li = el("li", "rounded-md bg-white p-2 ring-1 ring-gray-200");
      const a = el("a", "font-medium text-teal-700 hover:underline", s.name);
      a.href = `/candidates/${s.candidate_id}`;
      li.append(a, el("span", "ml-2 text-gray-400", `match ${s.match}`), el("p", "mt-1 text-gray-600", s.snippet));
      ul.appendChild(li);
    }
    d.appendChild(ul);
    box.appendChild(d);
  }

  // Reads a text/event-stream response body and calls onEvent(event, parsedData).
  async function readSSE(body, onEvent) {
    const reader = body.getReader();
    const dec = new TextDecoder();
    let buf = "";
    for (;;) {
      const { value, done } = await reader.read();
      if (done) break;
      buf += dec.decode(value, { stream: true });
      let i;
      while ((i = buf.indexOf("\n\n")) >= 0) {
        const raw = buf.slice(0, i);
        buf = buf.slice(i + 2);
        let event = "message", data = "";
        for (const line of raw.split("\n")) {
          if (line.startsWith("event: ")) event = line.slice(7);
          else if (line.startsWith("data: ")) data += line.slice(6);
        }
        try { onEvent(event, JSON.parse(data)); } catch (_) { /* skip bad frame */ }
      }
    }
  }

  // ── Live pipeline dashboard ─────────────────────────────────────────────
  const dash = $("#dash");
  if (dash) {
    const conn = $("#conn");
    const slider = $("#workers");
    const sliderVal = $("#workers-val");
    let dragging = false;

    const stateClass = (s) =>
      s === "idle" ? "bg-gray-100 text-gray-600"
      : s.startsWith("retry") || s.startsWith("waiting") ? "bg-amber-50 text-amber-700"
      : "bg-teal-50 text-teal-700";

    const render = (snap) => {
      for (const n of dash.querySelectorAll("[data-k]")) {
        const v = snap[n.dataset.k];
        if (v !== undefined) n.textContent = v;
      }
      const br = $('[data-k="breaker"]', dash);
      br.className = "mt-1 text-lg font-semibold " + (snap.breaker === "closed" ? "text-emerald-700" : "text-amber-700");
      br.textContent = snap.breaker === "closed" ? "healthy" : snap.breaker === "open" ? "paused (30 s)" : "testing";

      const workers = snap.workers || [];
      if (!dragging) { slider.value = workers.length || 1; sliderVal.textContent = workers.length; }
      slider.max = snap.max_workers;

      const list = $("#workers-list");
      list.replaceChildren();
      if (!workers.length) list.appendChild(el("li", "px-5 py-6 text-gray-500", "Starting… the AI models may still be downloading."));
      for (const w of workers) {
        const li = el("li", "flex flex-wrap items-center gap-3 px-5 py-3");
        li.append(
          el("span", "w-20 font-medium", `Worker ${w.id}`),
          el("span", `rounded-md px-2 py-0.5 text-xs font-medium ${stateClass(w.state)}`, w.state),
          el("span", "flex-1 truncate text-gray-600", w.resume || "—"),
          el("span", "text-xs text-gray-400", `${w.seconds.toFixed(1)} s · ${w.handled} done`)
        );
        list.appendChild(li);
      }

      const events = $("#events");
      events.replaceChildren();
      const kindClass = { done: "text-emerald-700", failed: "text-rose-700", retry: "text-amber-700", info: "text-gray-600" };
      for (const ev of snap.events || []) {
        const li = el("li", "flex gap-3 px-5 py-2");
        li.append(el("span", "shrink-0 text-gray-400", new Date(ev.at).toLocaleTimeString()), el("span", kindClass[ev.kind] || "", ev.text));
        events.appendChild(li);
      }
      if (!(snap.events || []).length) events.appendChild(el("li", "px-5 py-6 text-gray-500", "Nothing yet. Upload resumes on a job page."));
    };

    const connect = () => {
      const es = new EventSource("/pipeline/stream");
      es.onopen = () => { conn.textContent = "● Live"; conn.className = "text-xs text-emerald-700"; };
      es.onmessage = (m) => render(JSON.parse(m.data));
      es.onerror = () => { conn.textContent = "Reconnecting…"; conn.className = "text-xs text-amber-700"; };
    };
    connect();

    slider.addEventListener("input", () => { dragging = true; sliderVal.textContent = slider.value; });
    slider.addEventListener("change", async () => {
      try {
        await fetch("/pipeline/workers", { method: "POST", body: new URLSearchParams({ workers: slider.value }) });
      } finally { dragging = false; }
    });
  }
})();
