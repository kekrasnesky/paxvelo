(function(){
  var d=document,root=d.documentElement;

  // Phone menu
  var burger=d.querySelector('.burger');
  function setMenu(open){root.classList.toggle('menu-open',open);if(burger){burger.setAttribute('aria-expanded',open);burger.setAttribute('aria-label',open?'Close menu':'Open menu')}}
  if(burger)burger.addEventListener('click',function(){setMenu(!root.classList.contains('menu-open'))});

  // Info dropdown
  var dd=d.querySelector('.dd'),ddBtn=dd&&dd.querySelector('.dd-btn');
  function setDD(open){if(dd){dd.classList.toggle('open',open);ddBtn.setAttribute('aria-expanded',open)}}
  if(ddBtn){
    ddBtn.addEventListener('click',function(e){e.stopPropagation();setDD(!dd.classList.contains('open'))});
    d.addEventListener('click',function(e){if(!dd.contains(e.target))setDD(false)});
  }
  d.addEventListener('keydown',function(e){if(e.key==='Escape'){setMenu(false);setDD(false)}});

  // Instagram posts: load Instagram's script only when they're about to scroll into view
  var insta=d.querySelectorAll('.insta');
  function loadInsta(){
    if(window.instgrm){window.instgrm.Embeds.process();return}
    if(d.getElementById('insta-js'))return;
    var s=d.createElement('script');s.id='insta-js';s.async=true;s.src='https://www.instagram.com/embed.js';d.body.appendChild(s);
  }
  if(insta.length){
    if('IntersectionObserver' in window){
      var seen=new IntersectionObserver(function(es){
        if(es.some(function(e){return e.isIntersecting})){seen.disconnect();loadInsta()}
      },{rootMargin:'400px'});
      insta.forEach(function(el){seen.observe(el)});
    }else loadInsta();
  }

  // Join form: write phone numbers as 301-555-0123 while they're typed
  d.querySelectorAll('form.join input[type=tel][pattern]').forEach(function(el){
    function fmt(){
      var n=el.value.replace(/\D/g,'');
      if(n[0]==='1')n=n.slice(1); // no US area code starts with 1, so it's the country code
      n=n.slice(0,10);
      el.value=n.length>6?n.slice(0,3)+'-'+n.slice(3,6)+'-'+n.slice(6):n.length>3?n.slice(0,3)+'-'+n.slice(3):n;
    }
    el.addEventListener('input',fmt);
    if(el.value)fmt();
  });

  // Join form: submit in the background and swap in the result
  var form=d.querySelector('form.join[action="/join"]');
  if(form&&window.fetch)form.addEventListener('submit',function(e){
    e.preventDefault();
    var btn=form.querySelector('button'),msg=form.querySelector('.msg');
    function fail(text){msg.textContent=text;msg.classList.add('err');btn.disabled=false}
    btn.disabled=true;msg.textContent='';msg.classList.remove('err');
    fetch(form.action,{method:'POST',body:new URLSearchParams(new FormData(form)),headers:{Accept:'application/json'}})
      .then(function(r){return r.json()})
      .then(function(j){
        if(!j.ok)return fail(j.message);
        var done=d.querySelector('.joined'),pay=done.querySelector('.joined-pay');
        done.querySelector('.joined-title').textContent=j.message;
        done.querySelector('.joined-detail').textContent=j.detail||'';
        if(j.pay_url){var a=pay.querySelector('a');a.href=j.pay_url;a.textContent=j.pay_label;pay.hidden=false}else{pay.hidden=true}
        form.hidden=true;done.hidden=false;done.focus();
      })
      .catch(function(){fail('Couldn’t reach the server. Check your connection and try again.')});
  });
})();
