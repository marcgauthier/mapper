// Logarithmic travel makes both overview and close-up zoom levels easy to reach.
const zoomControls = (() => {
  const slider=document.querySelector('#zoomSlider'),percent=document.querySelector('#zoomPercent');
  let map=null;
  const minimum=.01,maximum=8;
  const toLevel=value=>minimum*Math.pow(maximum/minimum,Number(value)/1000);
  const toSlider=level=>Math.log(level/minimum)/Math.log(maximum/minimum)*1000;
  function sync(){if(!map||map.destroyed())return;const value=Math.round(map.zoom()*100)+'%';slider.value=String(Math.max(0,Math.min(1000,toSlider(map.zoom()))));slider.setAttribute('aria-valuetext',value);percent.textContent=value;}
  function set(level){if(!map||map.destroyed())return;map.zoom({level:Math.max(minimum,Math.min(maximum,level)),renderedPosition:{x:map.width()/2,y:map.height()/2}});sync();}
  slider.addEventListener('input',()=>set(toLevel(slider.value)));
  document.querySelector('#zoomOut').addEventListener('click',()=>{if(map)set(map.zoom()/1.25)});
  document.querySelector('#zoomIn').addEventListener('click',()=>{if(map)set(map.zoom()*1.25)});
  document.querySelector('#zoomFit').addEventListener('click',()=>{if(map)map.fit(undefined,40)});
  function bind(next){map=next;map.minZoom(minimum);map.maxZoom(maximum);map.on('zoom',sync);sync();}
  return {bind};
})();
