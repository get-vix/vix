package report

import "html/template"

var pageTmpl = template.Must(template.New("page").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>vix e2e report</title>
<style>
  :root {
    --bg:#0d1117; --bg2:#010409; --panel:#161b22; --panel2:#1c2128;
    --border:#30363d; --border2:#21262d; --fg:#e6edf3; --dim:#8b949e; --faint:#6e7681;
    --accent:#58a6ff;
    --pass:#3fb950; --fail:#f85149; --skip:#8b949e; --crash:#d29922;
  }
  * { box-sizing:border-box; }
  html { scroll-behavior:smooth; }
  body { margin:0; background:var(--bg); color:var(--fg);
         font:14px/1.6 -apple-system,BlinkMacSystemFont,"Segoe UI",Helvetica,Arial,sans-serif;
         -webkit-font-smoothing:antialiased; }
  a { color:var(--accent); text-decoration:none; }
  .hidden { display:none !important; }

  /* ---- header ---- */
  header { position:sticky; top:0; z-index:20; padding:16px 28px 14px;
           background:rgba(13,17,23,.85); backdrop-filter:blur(10px);
           border-bottom:1px solid var(--border); }
  .head-top { display:flex; align-items:center; gap:20px; flex-wrap:wrap; }
  header h1 { margin:0; font-size:17px; font-weight:700; letter-spacing:.2px; }
  header h1 span { color:var(--dim); font-weight:500; }
  .tools { margin-left:auto; display:flex; gap:8px; align-items:center; }
  #search { background:var(--panel); border:1px solid var(--border); color:var(--fg);
            border-radius:7px; padding:7px 11px; font-size:13px; width:220px; outline:none; }
  #search:focus { border-color:var(--accent); }
  .tools button { background:var(--panel); border:1px solid var(--border); color:var(--dim);
                  border-radius:7px; padding:7px 10px; font-size:12px; cursor:pointer; }
  .tools button:hover { color:var(--fg); border-color:var(--faint); }

  .progress { display:flex; height:7px; border-radius:5px; overflow:hidden;
              margin:14px 0 12px; background:var(--border2); }
  .progress .seg { min-width:0; }
  .seg.pass{background:var(--pass)} .seg.fail{background:var(--fail)}
  .seg.skip{background:var(--skip)} .seg.crash{background:var(--crash)}

  .chips { display:flex; gap:8px; flex-wrap:wrap; }
  .chip { display:inline-flex; align-items:center; gap:7px; cursor:pointer;
          background:var(--panel); border:1px solid var(--border); color:var(--fg);
          border-radius:20px; padding:5px 13px; font-size:12.5px; font-weight:600;
          transition:border-color .12s,background .12s; }
  .chip:hover { border-color:var(--faint); }
  .chip.active { background:var(--panel2); border-color:var(--accent); }
  .chip .dot { width:8px; height:8px; border-radius:50%; background:var(--faint); }
  .chip kbd { font:11px/1 ui-monospace,SFMono-Regular,Menlo,monospace; color:var(--faint);
              background:var(--bg2); border:1px solid var(--border); border-radius:4px;
              padding:1px 5px; margin-left:1px; }
  .chip.active kbd { color:var(--accent); border-color:var(--accent); }

  .dot.s-passed,.s-passed>.dot{background:var(--pass)}
  .dot.s-failed,.s-failed>.dot{background:var(--fail)}
  .dot.s-skipped,.s-skipped>.dot{background:var(--skip)}
  .dot.s-crashed,.s-crashed>.dot{background:var(--crash)}
  .dot.s-running,.s-running>.dot{background:var(--skip)}

  /* ---- layout ---- */
  .layout { display:flex; align-items:flex-start; }
  nav { width:280px; flex:none; position:sticky; top:135px; align-self:flex-start;
        max-height:calc(100vh - 135px); overflow:auto; padding:18px 12px 40px 20px;
        border-right:1px solid var(--border); }
  .nav-cat { margin-bottom:14px; }
  nav h3 { margin:0 0 6px; font-size:11px; letter-spacing:.7px; text-transform:uppercase;
           color:var(--faint); font-weight:700; }
  .nav-link { display:flex; align-items:center; gap:9px; padding:4px 9px; border-radius:6px;
              color:var(--dim); font-size:13px; line-height:1.35; }
  .nav-link .dot { width:7px; height:7px; border-radius:50%; flex:none; }
  .nav-link:hover { background:var(--panel); color:var(--fg); }
  .nav-link.current { background:var(--panel2); color:var(--fg); }
  .nav-link[data-status=failed]{color:#ff7b72} .nav-link[data-status=crashed]{color:#e3b341}

  main { flex:1; padding:20px 28px 80px; min-width:0; }
  section.sub { margin-bottom:8px; }
  section.sub h2 { font-size:13px; letter-spacing:.4px; text-transform:uppercase;
                   color:var(--dim); font-weight:700; margin:22px 0 12px; padding-bottom:6px;
                   border-bottom:1px solid var(--border2); }

  /* ---- card ---- */
  .card { background:var(--panel); border:1px solid var(--border); border-radius:10px;
          padding:16px 18px; margin:0 0 14px; scroll-margin-top:150px;
          border-left:3px solid var(--border); }
  .card.s-passed { border-left-color:var(--pass); }
  .card.s-failed { border-left-color:var(--fail); box-shadow:0 0 0 1px rgba(248,81,73,.25); }
  .card.s-crashed { border-left-color:var(--crash); box-shadow:0 0 0 1px rgba(210,153,34,.25); }
  .card.s-skipped { border-left-color:var(--skip); }
  .card-head { display:flex; align-items:center; gap:10px; }
  .card-head h4 { margin:0; font-size:15px; font-weight:600; }
  .card-head .dur { margin-left:auto; color:var(--faint); font-size:12px;
                    font-family:ui-monospace,SFMono-Regular,Menlo,monospace; white-space:nowrap; }
  .badge { font-size:10.5px; font-weight:700; text-transform:uppercase; letter-spacing:.5px;
           padding:3px 9px; border-radius:20px; }
  .badge.s-passed{background:rgba(63,185,80,.16); color:var(--pass)}
  .badge.s-failed{background:rgba(248,81,73,.16); color:var(--fail)}
  .badge.s-skipped{background:rgba(139,148,158,.16); color:var(--skip)}
  .badge.s-crashed{background:rgba(210,153,34,.16); color:var(--crash)}
  .badge.s-running{background:rgba(139,148,158,.16); color:var(--skip)}
  .meta { color:var(--faint); font-size:12px; margin:7px 0 0;
          font-family:ui-monospace,SFMono-Regular,Menlo,monospace; }
  .desc { margin:8px 0 4px; color:var(--fg); }

  /* ---- screenshots ---- */
  .shots { display:flex; flex-wrap:wrap; gap:14px; margin:12px 0 4px; }
  figure.shot { margin:0; }
  figure.shot img { display:block; max-width:520px; height:auto; border-radius:8px;
                    border:1px solid var(--border); }
  figure.shot figcaption { font-size:11.5px; color:var(--dim); margin-top:6px; text-align:center;
                           font-family:ui-monospace,SFMono-Regular,Menlo,monospace; }

  /* ---- collapsibles ---- */
  details { margin:12px 0 0; border:1px solid var(--border2); border-radius:8px; overflow:hidden; }
  summary { cursor:pointer; list-style:none; padding:9px 13px; font-size:12.5px; font-weight:600;
            color:var(--dim); background:var(--panel2); user-select:none; display:flex;
            align-items:center; gap:8px; }
  summary::-webkit-details-marker { display:none; }
  summary::before { content:"\25B8"; color:var(--faint); transition:transform .12s; }
  details[open] > summary::before { transform:rotate(90deg); }
  summary:hover { color:var(--fg); }
  .details-body { padding:0; }

  /* ---- source code (chroma github-dark, inline styles) ---- */
  .code { padding:0; }
  .code pre { margin:0; padding:13px 15px; border-radius:0 0 7px 7px; max-height:520px;
              overflow:auto; font:12px/1.5 ui-monospace,SFMono-Regular,Menlo,monospace; }

  /* ---- terminal block ---- */
  .terminal { background:var(--bg2); border:1px solid var(--border); border-radius:8px;
              overflow:hidden; }
  .term-bar { display:flex; align-items:center; gap:7px; padding:7px 12px;
              background:var(--panel2); border-bottom:1px solid var(--border); }
  .term-bar .tdot { width:11px; height:11px; border-radius:50%; }
  .tdot.r{background:#ff5f56} .tdot.y{background:#ffbd2e} .tdot.g{background:#27c93f}
  .term-bar .term-title { margin-left:6px; font-size:11.5px; color:var(--faint);
                          font-family:ui-monospace,SFMono-Regular,Menlo,monospace; }
  .term-body { margin:0; padding:13px 15px; overflow:auto; max-height:560px; color:#c9d1d9;
               font:12px/1.45 ui-monospace,SFMono-Regular,Menlo,monospace;
               white-space:pre; tab-size:4; }
  .shot .terminal { max-width:560px; }

  .empty { color:var(--dim); padding:60px; text-align:center; }
</style>
</head>
<body>
<header>
  <div class="head-top">
    <h1>vix <span>end-to-end report</span></h1>
    <div class="tools">
      <input id="search" type="search" placeholder="Filter tests…" autocomplete="off">
      <button id="expand">Expand all</button>
      <button id="collapse">Collapse all</button>
    </div>
  </div>
  <div class="progress" title="{{.Passed}} passed / {{.Failed}} failed / {{.Crashed}} crashed / {{.Skipped}} skipped">
    <div class="seg pass" style="flex:{{.Passed}}"></div>
    <div class="seg fail" style="flex:{{.Failed}}"></div>
    <div class="seg crash" style="flex:{{.Crashed}}"></div>
    <div class="seg skip" style="flex:{{.Skipped}}"></div>
  </div>
  <div class="chips">
    <button class="chip active" data-filter="all" data-key="a">{{.Total}} total<kbd>A</kbd></button>
    <button class="chip" data-filter="passed" data-key="p"><span class="dot s-passed"></span>{{.Passed}} passed<kbd>P</kbd></button>
    <button class="chip" data-filter="failed" data-key="f"><span class="dot s-failed"></span>{{.Failed}} failed<kbd>F</kbd></button>
    <button class="chip" data-filter="skipped" data-key="s"><span class="dot s-skipped"></span>{{.Skipped}} skipped<kbd>S</kbd></button>
    <button class="chip" data-filter="crashed" data-key="c"><span class="dot s-crashed"></span>{{.Crashed}} crashed<kbd>C</kbd></button>
  </div>
</header>
<div class="layout">
<nav>
  {{range .Categories}}
  <div class="nav-cat">
    <h3>{{.Name}}</h3>
    {{range .Subs}}{{range .Tests}}
    <a class="nav-link {{.StatusClass}}" data-status="{{.Status}}" data-name="{{.Name}}" href="#{{.Anchor}}"><span class="dot {{.StatusClass}}"></span>{{.Name}}</a>
    {{end}}{{end}}
  </div>
  {{end}}
</nav>
<main>
  {{range .Categories}}
  {{range .Subs}}
  <section class="sub">
    <h2>{{.Name}}</h2>
    {{range .Tests}}
    <article class="card {{.StatusClass}}" id="{{.Anchor}}" data-status="{{.Status}}" data-name="{{.Name}}">
      <div class="card-head">
        <span class="badge {{.StatusClass}}">{{.Status}}</span>
        <h4>{{.Name}}</h4>
        <span class="dur">{{.Duration}}</span>
      </div>
      <div class="meta">wire: {{.Wire}}</div>
      {{if .Description}}<div class="desc">{{.Description}}</div>{{end}}
      {{if .Shots}}
      <div class="shots">
        {{range .Shots}}
        <figure class="shot">
          {{if .PNG}}<a href="{{.PNG}}" target="_blank"><img src="{{.PNG}}" alt="{{.Label}}" loading="lazy"></a>
          {{else}}<div class="terminal"><pre class="term-body">{{.Text}}</pre></div>{{end}}
          <figcaption>{{.Label}}</figcaption>
        </figure>
        {{end}}
      </div>
      {{end}}
      {{if .CodeHTML}}<details class="impl"><summary>Implementation</summary><div class="details-body code">{{.CodeHTML}}</div></details>{{end}}
      {{if .HasDiag}}<details class="diag"{{if or (eq .Status "failed") (eq .Status "crashed")}} open{{end}}><summary>Diagnostics</summary><div class="details-body"><div class="terminal"><div class="term-bar"><span class="tdot r"></span><span class="tdot y"></span><span class="tdot g"></span><span class="term-title">diagnostics</span></div><pre class="term-body">{{.Diagnostics}}</pre></div></div></details>{{end}}
    </article>
    {{end}}
  </section>
  {{end}}
  {{end}}
  {{if not .Categories}}<div class="empty">No results found.</div>{{end}}
</main>
</div>
<script>
(function(){
  var cards=[].slice.call(document.querySelectorAll('.card'));
  var links=[].slice.call(document.querySelectorAll('.nav-link'));
  var chips=[].slice.call(document.querySelectorAll('.chip'));
  var search=document.getElementById('search');
  var status='all';

  function apply(){
    var q=search.value.trim().toLowerCase();
    function vis(el){
      var okS=status==='all'||el.getAttribute('data-status')===status;
      var okQ=!q||el.getAttribute('data-name').toLowerCase().indexOf(q)>=0;
      var show=okS&&okQ;
      el.classList.toggle('hidden',!show);
      return show;
    }
    cards.forEach(vis);
    links.forEach(vis);
    document.querySelectorAll('section.sub').forEach(function(s){
      var any=[].some.call(s.querySelectorAll('.card'),function(c){return !c.classList.contains('hidden');});
      s.classList.toggle('hidden',!any);
    });
    document.querySelectorAll('.nav-cat').forEach(function(g){
      var any=[].some.call(g.querySelectorAll('.nav-link'),function(a){return !a.classList.contains('hidden');});
      g.classList.toggle('hidden',!any);
    });
  }
  function selectFilter(f){
    var target=null;
    chips.forEach(function(x){
      var on=x.getAttribute('data-filter')===f;
      x.classList.toggle('active',on);
      if(on) target=x;
    });
    if(target){ status=f; apply(); }
  }
  chips.forEach(function(ch){ch.addEventListener('click',function(){
    selectFilter(ch.getAttribute('data-filter'));
  });});
  search.addEventListener('input',apply);

  // keyboard shortcuts: A/P/F/S/C jump straight to a status filter, "/" focuses
  // search, Esc clears it. Ignored while typing in the search box.
  var keyMap={a:'all',p:'passed',f:'failed',s:'skipped',c:'crashed'};
  document.addEventListener('keydown',function(e){
    if(e.metaKey||e.ctrlKey||e.altKey) return;
    if(e.key==='/' && document.activeElement!==search){ e.preventDefault(); search.focus(); return; }
    if(document.activeElement===search){
      if(e.key==='Escape'){ search.value=''; search.blur(); apply(); }
      return;
    }
    var f=keyMap[e.key.toLowerCase()];
    if(f){ e.preventDefault(); selectFilter(f); }
  });

  document.getElementById('expand').addEventListener('click',function(){
    document.querySelectorAll('details').forEach(function(d){d.open=true;});});
  document.getElementById('collapse').addEventListener('click',function(){
    document.querySelectorAll('details').forEach(function(d){d.open=false;});});

  // scroll-spy: highlight nav entry for the card nearest the top
  var byId={};
  links.forEach(function(a){byId[a.getAttribute('href').slice(1)]=a;});
  if('IntersectionObserver' in window){
    var obs=new IntersectionObserver(function(entries){
      entries.forEach(function(e){
        if(e.isIntersecting){
          var a=byId[e.target.id]; if(!a) return;
          links.forEach(function(x){x.classList.remove('current');});
          a.classList.add('current');
        }
      });
    },{rootMargin:'-30% 0px -60% 0px'});
    cards.forEach(function(c){obs.observe(c);});
  }
})();
</script>
</body>
</html>`))
