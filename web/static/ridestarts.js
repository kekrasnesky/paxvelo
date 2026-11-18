// Starting values on the Add a ride forms (ridestarts.go), for the public
// pages (before site.js) and the admin (with photos.js). A form with
// data-starts holds each weekday's start time, place, pace groups and level;
// picking a date fills them in, but only into fields that are empty or
// still hold the page's own value (a new form's 8:00, or what this filled
// in before), never into anything typed.
function fillStarts(){
  document.querySelectorAll('form[data-starts]').forEach(function(f){
    var starts={};try{starts=JSON.parse(f.dataset.starts)||{}}catch(x){}
    var date=f.querySelector('input[name=date]');if(!date)return;
    var fresh=f.hasAttribute('data-fresh'),own={};
    function val(n){
      if(n==='level')return [].filter.call(f.querySelectorAll('input[name=level]'),function(c){return c.checked}).map(function(c){return c.value}).join(' ');
      var e=f.elements[n];return e?e.value:'';
    }
    function set(n,v){
      if(n==='level'){var on=v.split(' ');f.querySelectorAll('input[name=level]').forEach(function(c){c.checked=on.indexOf(c.value)>=0})}
      else{var e=f.elements[n];if(!e)return;e.value=v;if(e.value!==v)return}
      own[n]=val(n);
      var e=n==='level'?f.querySelector('input[name=level]'):f.elements[n];
      if(e)e.dispatchEvent(new Event('change',{bubbles:true}));
    }
    var start={}; // what each field held before anything was filled in
    ['time','place','pace','level'].forEach(function(n){var v=val(n);if(fresh||v===''){own[n]=v;start[n]=v}});
    date.addEventListener('change',function(){
      var d=new Date(date.value+'T12:00:00');if(isNaN(d))return;
      var s=starts[d.getDay()]||{};
      [['time',s.time],['place',s.place],['pace',s.pace],['level',s.level]].forEach(function(p){
        var n=p[0];
        if(!(n==='level'?f.querySelector('input[name=level]'):f.elements[n])||!(n in own))return;
        var cur=val(n);if(cur!==''&&cur!==own[n])return; // typed in: leave it
        var v=p[1]||start[n]; // nothing for this weekday: back to how it started
        if(v!==cur)set(n,v);
      });
    });
  });
}
if(document.readyState==='loading')document.addEventListener('DOMContentLoaded',fillStarts);else fillStarts();
