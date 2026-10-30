(function(){
  var d=document,root=d.documentElement;

  // Phone menu. It covers the page, so while it's open everything behind it is
  // inert (keyboard and screen readers stay in the menu) and focus starts on
  // its first link.
  var burger=d.querySelector('.burger'),nav=d.getElementById('nav');
  var behind=[d.querySelector('.skip'),d.querySelector('.hdr .logo'),d.getElementById('main'),d.querySelector('.ftr')];
  function setMenu(open){
    root.classList.toggle('menu-open',open);
    behind.forEach(function(el){if(el)el.inert=open});
    if(burger){burger.setAttribute('aria-expanded',open);burger.setAttribute('aria-label',open?'Close menu':'Open menu')}
  }
  if(burger)burger.addEventListener('click',function(){
    var open=!root.classList.contains('menu-open');
    setMenu(open);
    if(open&&nav)nav.querySelector('a,button').focus();
  });
  // Widening the window past phone size hides the burger, so close the menu with it
  if(window.matchMedia)matchMedia('(min-width:768px)').addEventListener('change',function(m){if(m.matches)setMenu(false)});

  // Info dropdown
  var dd=d.querySelector('.dd'),ddBtn=dd&&dd.querySelector('.dd-btn');
  function setDD(open){if(dd){dd.classList.toggle('open',open);ddBtn.setAttribute('aria-expanded',open)}}
  if(ddBtn){
    ddBtn.addEventListener('click',function(e){e.stopPropagation();setDD(!dd.classList.contains('open'))});
    d.addEventListener('click',function(e){if(!dd.contains(e.target))setDD(false)});
  }
  d.addEventListener('keydown',function(e){
    if(e.key!=='Escape')return;
    if(root.classList.contains('menu-open')){setMenu(false);if(burger)burger.focus()}
    setDD(false);
  });
  // A page brought back by the Back button is restored exactly as it was left,
  // which would be with the menu still open after tapping one of its links
  addEventListener('pageshow',function(e){if(e.persisted){setMenu(false);setDD(false)}});

  // Instagram posts the site doesn't draw itself: load Instagram's script only
  // when they're about to scroll into view
  var insta=d.querySelectorAll('.insta-embeds');
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

  // Home page photo row: arrows step through it three at a time (a row plus
  // its 10px gap), and hide
  // at either end (wider screens only; phones swipe).
  d.querySelectorAll('.photo-strip.many').forEach(function(strip){
    var row=strip.querySelector('.photos'),prev=strip.querySelector('.prev'),next=strip.querySelector('.next');
    function update(){
      var end=row.scrollWidth-row.clientWidth;
      prev.hidden=row.scrollLeft<=2;
      next.hidden=end<=2||row.scrollLeft>=end-2;
    }
    prev.addEventListener('click',function(){row.scrollBy({left:-(row.clientWidth+10)})});
    next.addEventListener('click',function(){row.scrollBy({left:row.clientWidth+10})});
    row.addEventListener('scroll',update,{passive:true});
    window.addEventListener('resize',update);
    update();
  });

  // With more than 6 photos the row is a slideshow: it moves on every 5
  // seconds (a screenful on wider screens, one photo on phones) and starts
  // over at the end. It waits while someone's pointing at it, touching it, or
  // has it focused, and while it's off screen; not at all for people who
  // prefer less motion.
  d.querySelectorAll('.photo-strip.slides').forEach(function(strip){
    if(window.matchMedia&&matchMedia('(prefers-reduced-motion: reduce)').matches)return;
    var row=strip.querySelector('.photos'),hover=false,focus=false,seen=true,until=0;
    function step(){
      if(hover||focus||!seen||d.hidden||Date.now()<until)return;
      var end=row.scrollWidth-row.clientWidth;if(end<=2)return;
      if(row.scrollLeft>=end-2){row.scrollTo({left:0,behavior:'smooth'});return}
      var by=innerWidth>=700?row.clientWidth+10:row.querySelector('img').getBoundingClientRect().width+10;
      row.scrollBy({left:by,behavior:'smooth'});
    }
    strip.addEventListener('mouseenter',function(){hover=true});
    strip.addEventListener('mouseleave',function(){hover=false});
    strip.addEventListener('focusin',function(){focus=true});
    strip.addEventListener('focusout',function(){focus=false});
    strip.addEventListener('pointerdown',function(){until=Date.now()+15000});
    if('IntersectionObserver' in window)new IntersectionObserver(function(es){seen=es[0].isIntersecting}).observe(strip);
    setInterval(step,5000);
  });

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
