// Phone run-off check (docs/look.md, "Phones"): lists every page with
// something wider than a 320px or 375px screen: admin pages in the normal
// admin and in the admin app frame (pv_app cookie), public pages once.
// Chrome can't show iPhone-only widths (date and time fields:
// TestDateFieldsShrink), so an empty table isn't the whole story.
//
// Run it against a -demo preview (CLAUDE.md, Previews), never real members:
// sign in, open any /admin page, and paste this file into the browser's
// console. It finds the pages itself by following links from Home, the app
// Home, the public home page and the calendar (GET only, nothing that signs out or downloads; one page per
// kind, so /admin/members/1 stands for every member), loads each in a hidden
// frame of each width (as srcdoc: admin pages refuse to be framed), and
// prints a table. An empty table means no run-off. It ends with the frame
// you started in.
//
// "Run-off" is any element whose box reaches past either edge of the screen,
// unless it sits inside something that scrolls sideways on purpose
// (overflow-x auto/scroll: menu rows, wide tables). .wrap clips at the edge,
// so the browser shows no scroll bar for these: the content is just cut off.
(async () => {
  const WIDTHS = [320, 375];
  const START = ["/admin?app=0", "/admin/app?app=1", "/", "/calendar"];
  const MAX_PAGES = 150;
  const SKIP = /\/(logout|login|forgot|password|strava)(\/|$)|\.(csv|ics|pdf|jpe?g|png|webp|gif|svg|ico|mp4|zip|json|txt|xml|js|css|woff2|webmanifest)$|\/(photo|file|receipt|download|static|media|files)(\/|$)/i;
  const appWas = !!document.querySelector(".app-top"); // pv_app is HttpOnly: tell by the top bar

  const norm = (href) => {
    const u = new URL(href, location.origin);
    if (u.origin !== location.origin) return "";
    for (const k of ["app", "ok", "err"]) u.searchParams.delete(k);
    const p = u.pathname + (u.search || "");
    return SKIP.test(u.pathname) ? "" : p;
  };

  // 1. Find the pages: every /admin link reachable from both Homes.
  // One page per kind: digits in a path part (ids, dates, ride keys) and
  // query values don't make a new kind.
  const kind = (p) => {
    const u = new URL(p, location.origin);
    return u.pathname.split("/").map((x) => (/\d/.test(x) ? "*" : x)).join("/") + "?" + [...u.searchParams.keys()].sort().join("&");
  };
  const pages = [], seen = new Set(), kinds = new Set();
  const queue = START.slice();
  while (queue.length && pages.length < MAX_PAGES) {
    const raw = queue.shift();
    const res = await fetch(raw, { credentials: "same-origin" });
    if (!res.ok || !(res.headers.get("content-type") || "").includes("text/html")) continue;
    const path = norm(res.url);
    if (path && !seen.has(path)) { seen.add(path); kinds.add(kind(path)); pages.push(path); }
    const doc = new DOMParser().parseFromString(await res.text(), "text/html");
    for (const a of doc.querySelectorAll("a[href]")) {
      if (a.hasAttribute("download") || a.target === "_blank") continue;
      const p = norm(a.getAttribute("href"));
      if (p && !seen.has(p) && !kinds.has(kind(p)) && !queue.some((q) => kind(q) === kind(p))) queue.push(p);
    }
  }

  // 2. Measure one page in a hidden frame of the given width.
  const describe = (el) => {
    let s = el.tagName.toLowerCase();
    if (el.id) s += "#" + el.id;
    const cls = [...el.classList].slice(0, 3).join(".");
    if (cls) s += "." + cls;
    const t = (el.textContent || el.value || "").trim().replace(/\s+/g, " ").slice(0, 40);
    return t ? s + ' "' + t + '"' : s;
  };
  const measure = async (path, width) => {
    const res = await fetch(path, { credentials: "same-origin" });
    const html = (await res.text()).replace(/<head>/i, `<head><base href="${location.origin + path}">`);
    return measureHTML(html, width);
  };
  const measureHTML = (html, width) => new Promise((done) => {
    const f = document.createElement("iframe");
    f.style.cssText = `position:fixed;left:-${width + 50}px;top:0;width:${width}px;height:800px;border:0;visibility:hidden`;
    const finish = (rows) => { f.remove(); done(rows); };
    const timer = setTimeout(() => finish([{ el: "(did not load in 15 s)", px: "" }]), 15000);
    f.onload = () => setTimeout(() => {
      clearTimeout(timer);
      const d = f.contentDocument, w = f.contentWindow;
      if (!d || !d.body) return finish([{ el: "(no page)", px: "" }]);
      const vw = d.documentElement.clientWidth, out = [];
      const scrolls = (el) => {
        for (let p = el.parentElement; p && p !== d.body; p = p.parentElement) {
          const o = w.getComputedStyle(p).overflowX;
          if (o === "auto" || o === "scroll") return true;
        }
        return false;
      };
      const over = (el) => {
        if (el.closest("[hidden],template,script,style,head,dialog:not([open])")) return false;
        const cs = w.getComputedStyle(el);
        if (cs.display === "none" || cs.visibility === "hidden" || cs.position === "fixed") return false;
        const r = el.getBoundingClientRect();
        if (r.width < 1 || r.height < 1) return false;
        if (cs.clip !== "auto" && cs.position === "absolute") return false; // screen-reader-only text
        if (cs.position === "absolute" && (r.right < 0 || r.left > vw)) return false; // parked off screen on purpose: skip link, honeypot
        return (r.right > vw + 0.5 || r.left < -0.5) && !scrolls(el);
      };
      for (const el of d.body.querySelectorAll("*")) {
        if (!over(el) || (el.parentElement && el.parentElement !== d.body && over(el.parentElement))) continue;
        const r = el.getBoundingClientRect();
        out.push({ el: describe(el), px: Math.round(r.left < 0 ? -r.left : r.right - vw) });
      }
      if (d.documentElement.scrollWidth > vw + 0.5 && !out.length) out.push({ el: "(page scrolls sideways)", px: d.documentElement.scrollWidth - vw });
      finish(out);
    }, 400);
    f.srcdoc = html;
    document.body.appendChild(f);
  });

  // 3. Every page × width × frame; the frame is a cookie, so one frame at a time.
  const found = [];
  for (const app of ["0", "1"]) {
    await fetch("/admin?app=" + app, { credentials: "same-origin" });
    const frame = app === "1" ? "app" : "admin";
    const jobs = [];
    for (const p of pages) {
      const pub = !p.startsWith("/admin");
      if (pub && app === "1") continue; // public pages don't change with the frame
      for (const wd of WIDTHS) jobs.push([p, wd, pub ? "site" : frame]);
    }
    const run = async () => {
      while (jobs.length) {
        const [p, wd, fr] = jobs.shift();
        for (const r of await measure(p, wd)) found.push({ frame: fr, width: wd, page: p, element: r.el, "px over": r.px });
      }
    };
    await Promise.all([run(), run(), run(), run()]);
  }
  await fetch("/admin?app=" + (appWas ? "1" : "0"), { credentials: "same-origin" });

  console.log(`Run-off check: ${pages.length} pages × ${WIDTHS.join("/")}px × admin/app frame (public pages once): ${found.length ? found.length + " problem(s)" : "nothing runs off"}.`);
  if (found.length) console.table(found);
  return { pages: pages.length, problems: found };
})();
