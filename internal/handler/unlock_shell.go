package handler

// unlockShells holds the finished prompt per language, each with the SHA-256
// hashes of its own inline blocks — exactly as the viewer shell's are — so the
// page carries zero external subresources and still forbids any injected
// inline execution.
var unlockShells = renderShells("unlock", unlockShellTmpl, unlockShellTextByLang)

// unlockShellTmpl is the code prompt a visitor without a valid unlock ticket
// gets in place of the viewer shell. It submits to /s/:id/unlock as a fetch()
// call — which is what satisfies the CSRF middleware's X-Requested-With
// requirement — and reloads on success, at which point the ticket cookie is
// present and the real shell renders.
//
// The page never says whether the code it was given was close to right, and the
// server answers a wrong one and a rate-limited one differently only in status;
// both render as the same message here plus, for 429, the wait hint.
const unlockShellTmpl = `<!DOCTYPE html>
<html lang="{{.Lang}}">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>{{.Title}}</title>
<!--og-->
<link rel="icon" href="data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 32 32'%3E%3Crect width='32' height='32' rx='7' fill='%2316150f'/%3E%3Ctext x='16' y='21' font-family='ui-monospace,monospace' font-size='13' font-weight='bold' fill='%23fafaf8' text-anchor='middle'%3E%26lt%3B/%26gt%3B%3C/text%3E%3C/svg%3E">
<style>
:root{--bg:#fafaf8;--surface:#fff;--fg:#16150f;--fg-soft:#6b6960;--line:#e7e5dd;--line-strong:#d4d2c8;--ink:#16150f;--ink-fg:#fafaf8;--danger:#b3261e;}
.dark{--bg:#0e0e0c;--surface:#181814;--fg:#f3f2ea;--fg-soft:#a3a195;--line:#262620;--line-strong:#34332b;--ink:#f3f2ea;--ink-fg:#16150f;--danger:#f2b8b5;}
*{box-sizing:border-box;}
html,body{height:100%;margin:0;}
body{font-family:system-ui,-apple-system,"Segoe UI","Noto Sans SC",sans-serif;background:var(--bg);color:var(--fg);display:flex;align-items:center;justify-content:center;padding:24px;-webkit-font-smoothing:antialiased;}
.card{width:min(380px,100%);text-align:center;}
.mark{width:44px;height:44px;border-radius:12px;display:inline-flex;align-items:center;justify-content:center;font-family:ui-monospace,Menlo,monospace;font-size:18px;background:var(--ink);color:var(--ink-fg);margin-bottom:20px;}
h1{font-weight:800;font-size:24px;letter-spacing:-.02em;margin:0 0 8px;}
.sub{color:var(--fg-soft);font-size:14px;line-height:1.6;margin:0 0 24px;}
.code-input{width:100%;height:48px;text-align:center;border:1px solid var(--line-strong);border-radius:10px;background:var(--surface);color:var(--fg);font:600 22px/1 ui-monospace,SFMono-Regular,Menlo,monospace;letter-spacing:.4em;text-indent:.4em;}
.code-input:focus{outline:2px solid var(--ink);outline-offset:1px;}
.btn{margin-top:12px;width:100%;height:40px;border-radius:10px;border:1px solid var(--ink);background:var(--ink);color:var(--ink-fg);font:500 14px system-ui,-apple-system,"Segoe UI","Noto Sans SC",sans-serif;cursor:pointer;}
.btn[disabled]{opacity:.55;cursor:default;}
.err{margin:12px 0 0;min-height:20px;font-size:13px;line-height:1.5;color:var(--danger);}
.home{display:inline-block;margin-top:20px;color:var(--fg-soft);font-size:13px;text-decoration:none;}
.home:hover{color:var(--fg);}
</style>
</head>
<body>
<main class="card">
  <span class="mark" aria-hidden="true">&lt;/&gt;</span>
  <h1>{{.Heading}}</h1>
  <p class="sub">{{.Body}}</p>
  <form id="unlock-form" novalidate>
    <input class="code-input" id="code" name="code" type="text" inputmode="numeric" autocomplete="one-time-code"
           maxlength="6" pattern="[0-9]{6}" aria-label="{{.InputLabel}}" autofocus>
    <button class="btn" id="submit" type="submit">{{.SubmitButton}}</button>
  </form>
  <p class="err" id="error" role="alert" aria-live="polite"></p>
  <a class="home" href="/">{{.HomeLink}}</a>
</main>
<script>
(function(){
  var root=document.documentElement;
  var saved=localStorage.getItem('page-theme');
  if(saved?saved==='dark':window.matchMedia('(prefers-color-scheme: dark)').matches){root.classList.add('dark');}
  var nano=location.pathname.split('/')[2]||'';
  var pendingKey='placard-unlock-'+nano;
  var form=document.getElementById('unlock-form');
  var input=document.getElementById('code');
  var submit=document.getElementById('submit');
  var error=document.getElementById('error');
  // A correct code reloads, expecting the ticket cookie to come back with the
  // request. When the browser refuses to keep it — cookies blocked, the
  // per-domain cookie cap reached — this page is served again and the visitor
  // would otherwise retype a correct code forever with nothing to read. The
  // marker survives exactly that reload and turns the second visit into an
  // explanation; sessionStorage can itself be unavailable, in which case the
  // page simply behaves as it did before.
  try{
    var pending=sessionStorage.getItem(pendingKey);
    sessionStorage.removeItem(pendingKey);
    if(pending&&Date.now()-parseInt(pending,10)<30000){
      error.textContent='{{.CookieError}}';
    }
  }catch(ignore){}
  input.addEventListener('input',function(){
    input.value=input.value.replace(/\D/g,'').slice(0,6);
    error.textContent='';
  });
  form.addEventListener('submit',function(e){
    e.preventDefault();
    if(input.value.length!==6||submit.disabled){return;}
    submit.disabled=true;
    error.textContent='';
    fetch('/s/'+encodeURIComponent(nano)+'/unlock',{
      method:'POST',
      credentials:'same-origin',
      headers:{'Content-Type':'application/json','X-Requested-With':'fetch'},
      body:JSON.stringify({code:input.value})
    }).then(function(r){
      if(r.ok){
        try{sessionStorage.setItem(pendingKey,String(Date.now()));}catch(ignore){}
        location.reload();
        return;
      }
      submit.disabled=false;
      error.textContent=r.status===429?'{{.RateError}}':'{{.WrongError}}';
      input.select();
    }).catch(function(){
      submit.disabled=false;
      error.textContent='{{.NetworkError}}';
    });
  });
})();
</script>
</body>
</html>`
