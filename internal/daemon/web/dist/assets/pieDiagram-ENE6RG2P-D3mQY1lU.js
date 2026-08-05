import{p as nt}from"./chunk-JWPE2WC7-Dlch_xO9.js";import{x as it,m as st,n as ot,s as lt,g as ct,c as ut,b as gt,_ as d,l as B,o as dt,d as pt,y as ht,C as ft,M as mt,j as vt}from"./mermaid.core-DoumPN9C.js";import{p as xt}from"./cynefin-VYW2F7L2-D4iyQcIb.js";import{k as T,t as G,r as yt,u as St}from"./index-CCHUlPM2.js";import{d as J}from"./arc-koyc1qY2.js";function wt(t,n){return n<t?-1:n>t?1:n>=t?0:NaN}function At(t){return t}function Ct(){var t=At,n=wt,S=null,b=T(0),l=T(G),p=T(0);function i(e){var r,o=(e=yt(e)).length,h,w,$=0,f=new Array(o),s=new Array(o),D=+b.apply(this,arguments),z=Math.min(G,Math.max(-G,l.apply(this,arguments)-D)),k,L=Math.min(Math.abs(z)/o,p.apply(this,arguments)),u=L*(z<0?-1:1),A;for(r=0;r<o;++r)(A=s[f[r]=r]=+t(e[r],r,e))>0&&($+=A);for(n!=null?f.sort(function(E,m){return n(s[E],s[m])}):S!=null&&f.sort(function(E,m){return S(e[E],e[m])}),r=0,w=$?(z-o*u)/$:0;r<o;++r,D=k)h=f[r],A=s[h],k=D+(A>0?A*w:0)+u,s[h]={data:e[h],index:r,value:A,startAngle:D,endAngle:k,padAngle:L};return s}return i.value=function(e){return arguments.length?(t=typeof e=="function"?e:T(+e),i):t},i.sortValues=function(e){return arguments.length?(n=e,S=null,i):n},i.sort=function(e){return arguments.length?(S=e,n=null,i):S},i.startAngle=function(e){return arguments.length?(b=typeof e=="function"?e:T(+e),i):b},i.endAngle=function(e){return arguments.length?(l=typeof e=="function"?e:T(+e),i):l},i.padAngle=function(e){return arguments.length?(p=typeof e=="function"?e:T(+e),i):p},i}var K=it.pie,I={sections:new Map,showData:!1,config:K},F=I.sections,V=I.showData,$t=structuredClone(K),Dt=d(()=>structuredClone($t),"getConfig"),Tt=d(()=>{F=new Map,V=I.showData,dt()},"clear"),bt=d(({label:t,value:n})=>{if(n<0)throw new Error(`"${t}" has invalid value: ${n}. Negative values are not allowed in pie charts. All slice values must be >= 0.`);F.has(t)||(F.set(t,n),B.debug(`added new section: ${t}, with value: ${n}`))},"addSection"),kt=d(()=>F,"getSections"),Mt=d(t=>{V=t},"setShowData"),zt=d(()=>V,"getShowData"),Q={getConfig:Dt,clear:Tt,setDiagramTitle:st,getDiagramTitle:ot,setAccTitle:lt,getAccTitle:ct,setAccDescription:ut,getAccDescription:gt,addSection:bt,getSections:kt,setShowData:Mt,getShowData:zt},Et=d((t,n)=>{nt(t,n),n.setShowData(t.showData),t.sections.map(n.addSection)},"populateDb"),Rt={parse:d(async t=>{const n=await xt("pie",t);B.debug(n),Et(n,Q)},"parse")},Lt=d(t=>`
  .pieCircle{
    stroke: ${t.pieStrokeColor};
    stroke-width : ${t.pieStrokeWidth};
    opacity : ${t.pieOpacity};
  }
  .pieCircle.highlighted{
    scale: 1.05;
    opacity: 1;
  }
  .pieCircle.highlightedOnHover:hover{
    transition-duration: 250ms;
    scale: 1.05;
    opacity: 1;
  }
  .pieOuterCircle{
    stroke: ${t.pieOuterStrokeColor};
    stroke-width: ${t.pieOuterStrokeWidth};
    fill: none;
  }
  .pieTitleText {
    text-anchor: middle;
    font-size: ${t.pieTitleTextSize};
    fill: ${t.pieTitleTextColor};
    font-family: ${t.fontFamily};
  }
  .slice {
    font-family: ${t.fontFamily};
    fill: ${t.pieSectionTextColor};
    font-size:${t.pieSectionTextSize};
    // fill: white;
  }
  .legend text {
    fill: ${t.pieLegendTextColor};
    font-family: ${t.fontFamily};
    font-size: ${t.pieLegendTextSize};
  }
`,"getStyles"),Wt=Lt,_t=d(t=>{const n=[...t.values()].reduce((l,p)=>l+p,0),S=[...t.entries()].map(([l,p])=>({label:l,value:p})).filter(l=>l.value/n*100>=1);return Ct().value(l=>l.value).sort(null)(S)},"createPieArcs"),Ft=d((t,n,S,b)=>{var q;B.debug(`rendering pie chart
`+t);const l=b.db,p=pt(),i=ht(l.getConfig(),p.pie),e=40,r=18,o=4,h=450,w=h,$=ft(n),f=$.append("g");f.attr("transform","translate("+w/2+","+h/2+")");const{themeVariables:s}=p;let[D]=mt(s.pieOuterStrokeWidth);D??(D=2);const z=i.legendPosition,k=i.textPosition,L=i.donutHole>0&&i.donutHole<=.9?i.donutHole:0,u=Math.min(w,h)/2-e,A=J().innerRadius(L*u).outerRadius(u),E=J().innerRadius(u*k).outerRadius(u*k),m=f.append("g");m.append("circle").attr("cx",0).attr("cy",0).attr("r",u+D/2).attr("class","pieOuterCircle");const W=l.getSections(),Y=_t(W),tt=[s.pie1,s.pie2,s.pie3,s.pie4,s.pie5,s.pie6,s.pie7,s.pie8,s.pie9,s.pie10,s.pie11,s.pie12];let H=0;W.forEach(a=>{H+=a});const j=Y.filter(a=>(a.data.value/H*100).toFixed(0)!=="0"),N=St(tt).domain([...W.keys()]);m.selectAll("mySlices").data(j).enter().append("path").attr("d",A).attr("fill",a=>N(a.data.label)).attr("class",a=>{let c="pieCircle";return i.highlightSlice==="hover"?c+=" highlightedOnHover":i.highlightSlice===a.data.label&&(c+=" highlighted"),c}),m.selectAll("mySlices").data(j).enter().append("text").text(a=>(a.data.value/H*100).toFixed(0)+"%").attr("transform",a=>"translate("+E.centroid(a)+")").style("text-anchor","middle").attr("class","slice");const et=f.append("text").text(l.getDiagramTitle()).attr("x",0).attr("y",-(h-50)/2).attr("class","pieTitleText"),R=[...W.entries()].map(([a,c])=>({label:a,value:c})),C=f.selectAll(".legend").data(R).enter().append("g").attr("class","legend");C.append("rect").attr("width",r).attr("height",r).style("fill",a=>N(a.label)).style("stroke",a=>N(a.label)),C.append("text").attr("x",r+o).attr("y",r-o).text(a=>l.getShowData()?`${a.label} [${a.value}]`:a.label);const M=Math.max(...C.selectAll("text").nodes().map(a=>(a==null?void 0:a.getBoundingClientRect().width)??0));let _=h,O=w+e;const g=r+o,P=R.length*g;switch(z){case"center":C.attr("transform",(a,c)=>{const v=g*R.length/2,x=-M/2-(r+o),y=c*g-v;return"translate("+x+","+y+")"});break;case"top":_+=P,C.attr("transform",(a,c)=>{const v=u,x=-M/2-(r+o),y=c*g-v;return`translate(${x}, ${y})`}),m.attr("transform",()=>`translate(0, ${P+g})`);break;case"bottom":_+=P,C.attr("transform",(a,c)=>{const v=-u-g,x=-M/2-(r+o),y=c*g-v;return"translate("+x+","+y+")"});break;case"left":O+=r+o+M,C.attr("transform",(a,c)=>{const v=g*R.length/2,x=-u-(r+o),y=c*g-v;return"translate("+x+","+y+")"}),m.attr("transform",()=>`translate(${M+r+o}, 0)`);break;case"right":default:O+=r+o+M,C.attr("transform",(a,c)=>{const v=g*R.length/2,x=12*r,y=c*g-v;return"translate("+x+","+y+")"});break}const U=((q=et.node())==null?void 0:q.getBoundingClientRect().width)??0,at=w/2-U/2,rt=w/2+U/2,X=Math.min(0,at),Z=Math.max(O,rt)-X;$.attr("viewBox",`${X} 0 ${Z} ${_}`),vt($,_,Z,i.useMaxWidth)},"draw"),Ht={draw:Ft},Vt={parser:Rt,db:Q,renderer:Ht,styles:Wt};export{Vt as diagram};
