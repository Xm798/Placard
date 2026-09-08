package handler

import (
	"io"
	"strings"
)

// linkRelayMessageType is the wire protocol between the sandboxed document and
// the viewer shell; both inline scripts and the contract tests reference it.
const linkRelayMessageType = "placard:open-link"
const linkRelayMaxHrefChars = "4096"

// linkRelayScript lets the sandboxed document report link activations to the
// trusted viewer shell. All policy decisions live in the shell; this script only
// reports clicks. If appended inside an unclosed raw-text or comment context
// (<script>, <style>, <textarea>, or <!--), it may be swallowed and not run. This
// accepted degradation falls back to the parent securitypolicyviolation path,
// where only the URL origin remains and the confirmation dialog still gates access;
// it never causes code that should not execute to run.
const linkRelayScript = `<script>
(function(){
  try{
    function relay(href){
      try{parent.postMessage({type:'` + linkRelayMessageType + `',href:href},'*');}catch(ignore){}
    }
    document.addEventListener('click',function(e){
      try{
        if(e.defaultPrevented){return;}
        var target=e.target;
        var anchor=target&&target.closest?target.closest('a[href]'):null;
        if(!anchor){return;}
        var raw=anchor.getAttribute('href');
        if(raw&&raw.charAt(0)==='#'){return;}
        e.preventDefault();
        relay(anchor.href.slice(0,` + linkRelayMaxHrefChars + `));
      }catch(ignore){}
    },true);
    window.open=function(u){
      relay(String(u).slice(0,` + linkRelayMaxHrefChars + `));
      return null;
    };
  }catch(ignore){}
})();
</script>`

func withLinkRelay(rc io.ReadCloser) io.ReadCloser {
	// Append the relay after the stored HTML so its doctype and parsing mode
	// remain untouched.
	return struct {
		io.Reader
		io.Closer
	}{
		Reader: io.MultiReader(rc, strings.NewReader(linkRelayScript)),
		Closer: rc,
	}
}
