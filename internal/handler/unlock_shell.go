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
<link rel="icon" href="data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 32 32'%3E%3Crect x='1.5' y='1.5' width='29' height='29' fill='%23ff6fb5' stroke='%230a0a0a' stroke-width='3'/%3E%3Ctext x='16' y='21' font-family='ui-monospace,monospace' font-size='13' font-weight='900' fill='%230a0a0a' text-anchor='middle'%3E%26lt%3B/%26gt%3B%3C/text%3E%3C/svg%3E">
<style>
:root{--bg:#ffde59;--surface:#fff;--fg:#0a0a0a;--fg-soft:#2e2e2e;--line:#0a0a0a;--line-strong:#0a0a0a;--edge:#0a0a0a;--shadow:#0a0a0a;--ink:#0a0a0a;--ink-fg:#ffde59;--accent:#ff6fb5;--pop:#5cc8ff;--hover:#fff4bf;--danger:#c40000;}
.dark{--bg:#141414;--surface:#1f1f1f;--fg:#f5f5f5;--fg-soft:#d4d4d4;--line:#f5f5f5;--line-strong:#f5f5f5;--edge:#f5f5f5;--shadow:#ffde59;--ink:#ffde59;--ink-fg:#0a0a0a;--hover:#2b2b2b;--danger:#ff7a7a;}
*{box-sizing:border-box;}
html,body{height:100%;margin:0;}
body{font-family:system-ui,-apple-system,"Segoe UI","Noto Sans SC",sans-serif;background:var(--bg);color:var(--fg);display:flex;align-items:center;justify-content:center;padding:24px;-webkit-font-smoothing:antialiased;}
.card{width:min(400px,100%);text-align:center;background:var(--surface);border:3px solid var(--edge);box-shadow:8px 8px 0 var(--shadow);padding:32px 28px;}
.mark{width:48px;height:48px;display:inline-flex;align-items:center;justify-content:center;font:900 18px ui-monospace,Menlo,monospace;background:var(--accent);color:#0a0a0a;border:3px solid var(--edge);box-shadow:3px 3px 0 var(--shadow);transform:rotate(-6deg);margin-bottom:22px;}
h1{font-weight:900;font-size:26px;letter-spacing:-.03em;text-transform:uppercase;margin:0 0 8px;}
.sub{color:var(--fg-soft);font-size:14px;line-height:1.6;margin:0 0 24px;}
.code-input{width:100%;height:50px;text-align:center;border:2.5px solid var(--edge);background:var(--surface);color:var(--fg);font:800 22px/1 ui-monospace,SFMono-Regular,Menlo,monospace;letter-spacing:.4em;text-indent:.4em;}
.code-input:focus{outline:none;box-shadow:4px 4px 0 var(--pop);}
.btn{margin-top:14px;width:100%;height:44px;border:2.5px solid var(--edge);background:var(--accent);color:#0a0a0a;font:800 14px system-ui,-apple-system,"Segoe UI","Noto Sans SC",sans-serif;text-transform:uppercase;letter-spacing:.04em;cursor:pointer;box-shadow:4px 4px 0 var(--shadow);transition:transform .08s,box-shadow .08s;}
.btn:hover:not([disabled]){transform:translate(-1px,-1px);box-shadow:6px 6px 0 var(--shadow);}
.btn:active:not([disabled]){transform:translate(4px,4px);box-shadow:0 0 0 var(--shadow);}
.btn[disabled]{opacity:.55;cursor:default;box-shadow:none;}
.err{margin:12px 0 0;min-height:20px;font-size:13px;font-weight:700;line-height:1.5;color:var(--danger);}
.home{display:inline-block;margin-top:20px;padding:2px 4px;color:var(--fg);font-size:13px;font-weight:700;text-decoration:underline;text-decoration-thickness:2px;text-underline-offset:3px;}
.home:hover{background:var(--pop);color:#0a0a0a;}
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
