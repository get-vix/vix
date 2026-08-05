import{_ as l,aR as q,d as ht,s as Ve,g as Pe,m as ze,n as Re,c as He,b as Be,o as Ge,k as Xe,l as lt,j as je,e as Ue,u as qe}from"./mermaid.core-DoumPN9C.js";import{y as me,z as ke,R as ye,C as ge,A as Ze,B as $t,D as Qe,v as It,x as Yt,s as bt,E as Ke,F as Je,G as tn,l as en,H as ie,I as re,J as se,K as ae,L as oe,M as ce,N as le,O as nn,P as rn,Q as sn,S as an,T as on,U as cn,V as ln}from"./index-CCHUlPM2.js";const un=Math.PI/180,dn=180/Math.PI,Ct=18,pe=.96422,ve=1,xe=.82521,Te=4/29,mt=6/29,be=3*mt*mt,fn=mt*mt*mt;function we(t){if(t instanceof nt)return new nt(t.l,t.a,t.b,t.opacity);if(t instanceof rt)return _e(t);t instanceof ye||(t=Ze(t));var e=Wt(t.r),n=Wt(t.g),i=Wt(t.b),s=Ft((.2225045*e+.7168786*n+.0606169*i)/ve),m,f;return e===n&&n===i?m=f=s:(m=Ft((.4360747*e+.3850649*n+.1430804*i)/pe),f=Ft((.0139322*e+.0971045*n+.7141733*i)/xe)),new nt(116*s-16,500*(m-s),200*(s-f),t.opacity)}function hn(t,e,n,i){return arguments.length===1?we(t):new nt(t,e,n,i??1)}function nt(t,e,n,i){this.l=+t,this.a=+e,this.b=+n,this.opacity=+i}me(nt,hn,ke(ge,{brighter(t){return new nt(this.l+Ct*(t??1),this.a,this.b,this.opacity)},darker(t){return new nt(this.l-Ct*(t??1),this.a,this.b,this.opacity)},rgb(){var t=(this.l+16)/116,e=isNaN(this.a)?t:t+this.a/500,n=isNaN(this.b)?t:t-this.b/200;return e=pe*Lt(e),t=ve*Lt(t),n=xe*Lt(n),new ye(Ot(3.1338561*e-1.6168667*t-.4906146*n),Ot(-.9787684*e+1.9161415*t+.033454*n),Ot(.0719453*e-.2289914*t+1.4052427*n),this.opacity)}}));function Ft(t){return t>fn?Math.pow(t,1/3):t/be+Te}function Lt(t){return t>mt?t*t*t:be*(t-Te)}function Ot(t){return 255*(t<=.0031308?12.92*t:1.055*Math.pow(t,1/2.4)-.055)}function Wt(t){return(t/=255)<=.04045?t/12.92:Math.pow((t+.055)/1.055,2.4)}function mn(t){if(t instanceof rt)return new rt(t.h,t.c,t.l,t.opacity);if(t instanceof nt||(t=we(t)),t.a===0&&t.b===0)return new rt(NaN,0<t.l&&t.l<100?0:NaN,t.l,t.opacity);var e=Math.atan2(t.b,t.a)*dn;return new rt(e<0?e+360:e,Math.sqrt(t.a*t.a+t.b*t.b),t.l,t.opacity)}function Pt(t,e,n,i){return arguments.length===1?mn(t):new rt(t,e,n,i??1)}function rt(t,e,n,i){this.h=+t,this.c=+e,this.l=+n,this.opacity=+i}function _e(t){if(isNaN(t.h))return new nt(t.l,0,0,t.opacity);var e=t.h*un;return new nt(t.l,Math.cos(e)*t.c,Math.sin(e)*t.c,t.opacity)}me(rt,Pt,ke(ge,{brighter(t){return new rt(this.h,this.c,this.l+Ct*(t??1),this.opacity)},darker(t){return new rt(this.h,this.c,this.l-Ct*(t??1),this.opacity)},rgb(){return _e(this).rgb()}}));function kn(t){return function(e,n){var i=t((e=Pt(e)).h,(n=Pt(n)).h),s=$t(e.c,n.c),m=$t(e.l,n.l),f=$t(e.opacity,n.opacity);return function(b){return e.h=i(b),e.c=s(b),e.l=m(b),e.opacity=f(b),e+""}}}const yn=kn(Qe);function gn(t){return t}var _t=1,Nt=2,zt=3,wt=4,ue=1e-6;function pn(t){return"translate("+t+",0)"}function vn(t){return"translate(0,"+t+")"}function xn(t){return e=>+t(e)}function Tn(t,e){return e=Math.max(0,t.bandwidth()-e*2)/2,t.round()&&(e=Math.round(e)),n=>+t(n)+e}function bn(){return!this.__axis}function De(t,e){var n=[],i=null,s=null,m=6,f=6,b=3,C=typeof window<"u"&&window.devicePixelRatio>1?0:.5,L=t===_t||t===wt?-1:1,w=t===wt||t===Nt?"x":"y",N=t===_t||t===zt?pn:vn;function _(D){var X=i??(e.ticks?e.ticks.apply(e,n):e.domain()),R=s??(e.tickFormat?e.tickFormat.apply(e,n):gn),g=Math.max(m,0)+b,M=e.range(),W=+M[0]+C,O=+M[M.length-1]+C,H=(e.bandwidth?Tn:xn)(e.copy(),C),z=D.selection?D.selection():D,I=z.selectAll(".domain").data([null]),x=z.selectAll(".tick").data(X,e).order(),k=x.exit(),E=x.enter().append("g").attr("class","tick"),h=x.select("line"),T=x.select("text");I=I.merge(I.enter().insert("path",".tick").attr("class","domain").attr("stroke","currentColor")),x=x.merge(E),h=h.merge(E.append("line").attr("stroke","currentColor").attr(w+"2",L*m)),T=T.merge(E.append("text").attr("fill","currentColor").attr(w,L*g).attr("dy",t===_t?"0em":t===zt?"0.71em":"0.32em")),D!==z&&(I=I.transition(D),x=x.transition(D),h=h.transition(D),T=T.transition(D),k=k.transition(D).attr("opacity",ue).attr("transform",function(v){return isFinite(v=H(v))?N(v+C):this.getAttribute("transform")}),E.attr("opacity",ue).attr("transform",function(v){var p=this.parentNode.__axis;return N((p&&isFinite(p=p(v))?p:H(v))+C)})),k.remove(),I.attr("d",t===wt||t===Nt?f?"M"+L*f+","+W+"H"+C+"V"+O+"H"+L*f:"M"+C+","+W+"V"+O:f?"M"+W+","+L*f+"V"+C+"H"+O+"V"+L*f:"M"+W+","+C+"H"+O),x.attr("opacity",1).attr("transform",function(v){return N(H(v)+C)}),h.attr(w+"2",L*m),T.attr(w,L*g).text(R),z.filter(bn).attr("fill","none").attr("font-size",10).attr("font-family","sans-serif").attr("text-anchor",t===Nt?"start":t===wt?"end":"middle"),z.each(function(){this.__axis=H})}return _.scale=function(D){return arguments.length?(e=D,_):e},_.ticks=function(){return n=Array.from(arguments),_},_.tickArguments=function(D){return arguments.length?(n=D==null?[]:Array.from(D),_):n.slice()},_.tickValues=function(D){return arguments.length?(i=D==null?null:Array.from(D),_):i&&i.slice()},_.tickFormat=function(D){return arguments.length?(s=D,_):s},_.tickSize=function(D){return arguments.length?(m=f=+D,_):m},_.tickSizeInner=function(D){return arguments.length?(m=+D,_):m},_.tickSizeOuter=function(D){return arguments.length?(f=+D,_):f},_.tickPadding=function(D){return arguments.length?(b=+D,_):b},_.offset=function(D){return arguments.length?(C=+D,_):C},_}function wn(t){return De(_t,t)}function _n(t){return De(zt,t)}var Se={exports:{}};(function(t,e){(function(n,i){t.exports=i()})(It,function(){var n="day";return function(i,s,m){var f=function(L){return L.add(4-L.isoWeekday(),n)},b=s.prototype;b.isoWeekYear=function(){return f(this).year()},b.isoWeek=function(L){if(!this.$utils().u(L))return this.add(7*(L-this.isoWeek()),n);var w,N,_,D,X=f(this),R=(w=this.isoWeekYear(),N=this.$u,_=(N?m.utc:m)().year(w).startOf("year"),D=4-_.isoWeekday(),_.isoWeekday()>4&&(D+=7),_.add(D,n));return X.diff(R,"week")+1},b.isoWeekday=function(L){return this.$utils().u(L)?this.day()||7:this.day(this.day()%7?L:L-7)};var C=b.startOf;b.startOf=function(L,w){var N=this.$utils(),_=!!N.u(w)||w;return N.p(L)==="isoweek"?_?this.date(this.date()-(this.isoWeekday()-1)).startOf("day"):this.date(this.date()-1-(this.isoWeekday()-1)+7).endOf("day"):C.bind(this)(L,w)}}})})(Se);var Dn=Se.exports;const Sn=Yt(Dn);var Ce={exports:{}};(function(t,e){(function(n,i){t.exports=i()})(It,function(){var n={LTS:"h:mm:ss A",LT:"h:mm A",L:"MM/DD/YYYY",LL:"MMMM D, YYYY",LLL:"MMMM D, YYYY h:mm A",LLLL:"dddd, MMMM D, YYYY h:mm A"},i=/(\[[^[]*\])|([-_:/.,()\s]+)|(A|a|Q|YYYY|YY?|ww?|MM?M?M?|Do|DD?|hh?|HH?|mm?|ss?|S{1,3}|z|ZZ?)/g,s=/\d/,m=/\d\d/,f=/\d\d?/,b=/\d*[^-_:/,()\s\d]+/,C={},L=function(g){return(g=+g)+(g>68?1900:2e3)},w=function(g){return function(M){this[g]=+M}},N=[/[+-]\d\d:?(\d\d)?|Z/,function(g){(this.zone||(this.zone={})).offset=function(M){if(!M||M==="Z")return 0;var W=M.match(/([+-]|\d\d)/g),O=60*W[1]+(+W[2]||0);return O===0?0:W[0]==="+"?-O:O}(g)}],_=function(g){var M=C[g];return M&&(M.indexOf?M:M.s.concat(M.f))},D=function(g,M){var W,O=C.meridiem;if(O){for(var H=1;H<=24;H+=1)if(g.indexOf(O(H,0,M))>-1){W=H>12;break}}else W=g===(M?"pm":"PM");return W},X={A:[b,function(g){this.afternoon=D(g,!1)}],a:[b,function(g){this.afternoon=D(g,!0)}],Q:[s,function(g){this.month=3*(g-1)+1}],S:[s,function(g){this.milliseconds=100*+g}],SS:[m,function(g){this.milliseconds=10*+g}],SSS:[/\d{3}/,function(g){this.milliseconds=+g}],s:[f,w("seconds")],ss:[f,w("seconds")],m:[f,w("minutes")],mm:[f,w("minutes")],H:[f,w("hours")],h:[f,w("hours")],HH:[f,w("hours")],hh:[f,w("hours")],D:[f,w("day")],DD:[m,w("day")],Do:[b,function(g){var M=C.ordinal,W=g.match(/\d+/);if(this.day=W[0],M)for(var O=1;O<=31;O+=1)M(O).replace(/\[|\]/g,"")===g&&(this.day=O)}],w:[f,w("week")],ww:[m,w("week")],M:[f,w("month")],MM:[m,w("month")],MMM:[b,function(g){var M=_("months"),W=(_("monthsShort")||M.map(function(O){return O.slice(0,3)})).indexOf(g)+1;if(W<1)throw new Error;this.month=W%12||W}],MMMM:[b,function(g){var M=_("months").indexOf(g)+1;if(M<1)throw new Error;this.month=M%12||M}],Y:[/[+-]?\d+/,w("year")],YY:[m,function(g){this.year=L(g)}],YYYY:[/\d{4}/,w("year")],Z:N,ZZ:N};function R(g){var M,W;M=g,W=C&&C.formats;for(var O=(g=M.replace(/(\[[^\]]+])|(LTS?|l{1,4}|L{1,4})/g,function(h,T,v){var p=v&&v.toUpperCase();return T||W[v]||n[v]||W[p].replace(/(\[[^\]]+])|(MMMM|MM|DD|dddd)/g,function(a,d,y){return d||y.slice(1)})})).match(i),H=O.length,z=0;z<H;z+=1){var I=O[z],x=X[I],k=x&&x[0],E=x&&x[1];O[z]=E?{regex:k,parser:E}:I.replace(/^\[|\]$/g,"")}return function(h){for(var T={},v=0,p=0;v<H;v+=1){var a=O[v];if(typeof a=="string")p+=a.length;else{var d=a.regex,y=a.parser,u=h.slice(p),S=d.exec(u)[0];y.call(T,S),h=h.replace(S,"")}}return function(r){var Y=r.afternoon;if(Y!==void 0){var o=r.hours;Y?o<12&&(r.hours+=12):o===12&&(r.hours=0),delete r.afternoon}}(T),T}}return function(g,M,W){W.p.customParseFormat=!0,g&&g.parseTwoDigitYear&&(L=g.parseTwoDigitYear);var O=M.prototype,H=O.parse;O.parse=function(z){var I=z.date,x=z.utc,k=z.args;this.$u=x;var E=k[1];if(typeof E=="string"){var h=k[2]===!0,T=k[3]===!0,v=h||T,p=k[2];T&&(p=k[2]),C=this.$locale(),!h&&p&&(C=W.Ls[p]),this.$d=function(u,S,r,Y){try{if(["x","X"].indexOf(S)>-1)return new Date((S==="X"?1e3:1)*u);var o=R(S)(u),G=o.year,c=o.month,A=o.day,$=o.hours,P=o.minutes,F=o.seconds,B=o.milliseconds,V=o.zone,st=o.week,ot=new Date,vt=A||(G||c?1:ot.getDate()),dt=G||ot.getFullYear(),j=0;G&&!c||(j=c>0?c-1:ot.getMonth());var K,Z=$||0,ct=P||0,J=F||0,at=B||0;return V?new Date(Date.UTC(dt,j,vt,Z,ct,J,at+60*V.offset*1e3)):r?new Date(Date.UTC(dt,j,vt,Z,ct,J,at)):(K=new Date(dt,j,vt,Z,ct,J,at),st&&(K=Y(K).week(st).toDate()),K)}catch{return new Date("")}}(I,E,x,W),this.init(),p&&p!==!0&&(this.$L=this.locale(p).$L),v&&I!=this.format(E)&&(this.$d=new Date("")),C={}}else if(E instanceof Array)for(var a=E.length,d=1;d<=a;d+=1){k[1]=E[d-1];var y=W.apply(this,k);if(y.isValid()){this.$d=y.$d,this.$L=y.$L,this.init();break}d===a&&(this.$d=new Date(""))}else H.call(this,z)}}})})(Ce);var Cn=Ce.exports;const Mn=Yt(Cn);var Me={exports:{}};(function(t,e){(function(n,i){t.exports=i()})(It,function(){return function(n,i){var s=i.prototype,m=s.format;s.format=function(f){var b=this,C=this.$locale();if(!this.isValid())return m.bind(this)(f);var L=this.$utils(),w=(f||"YYYY-MM-DDTHH:mm:ssZ").replace(/\[([^\]]+)]|Q|wo|ww|w|WW|W|zzz|z|gggg|GGGG|Do|X|x|k{1,2}|S/g,function(N){switch(N){case"Q":return Math.ceil((b.$M+1)/3);case"Do":return C.ordinal(b.$D);case"gggg":return b.weekYear();case"GGGG":return b.isoWeekYear();case"wo":return C.ordinal(b.week(),"W");case"w":case"ww":return L.s(b.week(),N==="w"?1:2,"0");case"W":case"WW":return L.s(b.isoWeek(),N==="W"?1:2,"0");case"k":case"kk":return L.s(String(b.$H===0?24:b.$H),N==="k"?1:2,"0");case"X":return Math.floor(b.$d.getTime()/1e3);case"x":return b.$d.getTime();case"z":return"["+b.offsetName()+"]";case"zzz":return"["+b.offsetName("long")+"]";default:return N}});return m.bind(this)(w)}}})})(Me);var En=Me.exports;const In=Yt(En);var Ee={exports:{}};(function(t,e){(function(n,i){t.exports=i()})(It,function(){var n,i,s=1e3,m=6e4,f=36e5,b=864e5,C=31536e6,L=2628e6,w=/^(-|\+)?P(?:([-+]?[0-9,.]*)Y)?(?:([-+]?[0-9,.]*)M)?(?:([-+]?[0-9,.]*)W)?(?:([-+]?[0-9,.]*)D)?(?:T(?:([-+]?[0-9,.]*)H)?(?:([-+]?[0-9,.]*)M)?(?:([-+]?[0-9,.]*)S)?)?$/,N=/\[([^\]]+)]|YYYY|YY|Y|M{1,2}|D{1,2}|H{1,2}|m{1,2}|s{1,2}|SSS/g,_={years:C,months:L,days:b,hours:f,minutes:m,seconds:s,milliseconds:1,weeks:6048e5},D=function(I){return I instanceof H},X=function(I,x,k){return new H(I,k,x.$l)},R=function(I){return i.p(I)+"s"},g=function(I){return I<0},M=function(I){return g(I)?Math.ceil(I):Math.floor(I)},W=function(I){return Math.abs(I)},O=function(I,x){return I?g(I)?{negative:!0,format:""+W(I)+x}:{negative:!1,format:""+I+x}:{negative:!1,format:""}},H=function(){function I(k,E,h){var T=this;if(this.$d={},this.$l=h,k===void 0&&(this.$ms=0,this.parseFromMilliseconds()),E)return X(k*_[R(E)],this);if(typeof k=="number")return this.$ms=k,this.parseFromMilliseconds(),this;if(typeof k=="object")return Object.keys(k).forEach(function(a){T.$d[R(a)]=k[a]}),this.calMilliseconds(),this;if(typeof k=="string"){var v=k.match(w);if(v){var p=v.slice(2).map(function(a){return a!=null?Number(a):0});return this.$d.years=p[0],this.$d.months=p[1],this.$d.weeks=p[2],this.$d.days=p[3],this.$d.hours=p[4],this.$d.minutes=p[5],this.$d.seconds=p[6],this.calMilliseconds(),this}}return this}var x=I.prototype;return x.calMilliseconds=function(){var k=this;this.$ms=Object.keys(this.$d).reduce(function(E,h){return E+(k.$d[h]||0)*_[h]},0)},x.parseFromMilliseconds=function(){var k=this.$ms;this.$d.years=M(k/C),k%=C,this.$d.months=M(k/L),k%=L,this.$d.days=M(k/b),k%=b,this.$d.hours=M(k/f),k%=f,this.$d.minutes=M(k/m),k%=m,this.$d.seconds=M(k/s),k%=s,this.$d.milliseconds=k},x.toISOString=function(){var k=O(this.$d.years,"Y"),E=O(this.$d.months,"M"),h=+this.$d.days||0;this.$d.weeks&&(h+=7*this.$d.weeks);var T=O(h,"D"),v=O(this.$d.hours,"H"),p=O(this.$d.minutes,"M"),a=this.$d.seconds||0;this.$d.milliseconds&&(a+=this.$d.milliseconds/1e3,a=Math.round(1e3*a)/1e3);var d=O(a,"S"),y=k.negative||E.negative||T.negative||v.negative||p.negative||d.negative,u=v.format||p.format||d.format?"T":"",S=(y?"-":"")+"P"+k.format+E.format+T.format+u+v.format+p.format+d.format;return S==="P"||S==="-P"?"P0D":S},x.toJSON=function(){return this.toISOString()},x.format=function(k){var E=k||"YYYY-MM-DDTHH:mm:ss",h={Y:this.$d.years,YY:i.s(this.$d.years,2,"0"),YYYY:i.s(this.$d.years,4,"0"),M:this.$d.months,MM:i.s(this.$d.months,2,"0"),D:this.$d.days,DD:i.s(this.$d.days,2,"0"),H:this.$d.hours,HH:i.s(this.$d.hours,2,"0"),m:this.$d.minutes,mm:i.s(this.$d.minutes,2,"0"),s:this.$d.seconds,ss:i.s(this.$d.seconds,2,"0"),SSS:i.s(this.$d.milliseconds,3,"0")};return E.replace(N,function(T,v){return v||String(h[T])})},x.as=function(k){return this.$ms/_[R(k)]},x.get=function(k){var E=this.$ms,h=R(k);return h==="milliseconds"?E%=1e3:E=h==="weeks"?M(E/_[h]):this.$d[h],E||0},x.add=function(k,E,h){var T;return T=E?k*_[R(E)]:D(k)?k.$ms:X(k,this).$ms,X(this.$ms+T*(h?-1:1),this)},x.subtract=function(k,E){return this.add(k,E,!0)},x.locale=function(k){var E=this.clone();return E.$l=k,E},x.clone=function(){return X(this.$ms,this)},x.humanize=function(k){return n().add(this.$ms,"ms").locale(this.$l).fromNow(!k)},x.valueOf=function(){return this.asMilliseconds()},x.milliseconds=function(){return this.get("milliseconds")},x.asMilliseconds=function(){return this.as("milliseconds")},x.seconds=function(){return this.get("seconds")},x.asSeconds=function(){return this.as("seconds")},x.minutes=function(){return this.get("minutes")},x.asMinutes=function(){return this.as("minutes")},x.hours=function(){return this.get("hours")},x.asHours=function(){return this.as("hours")},x.days=function(){return this.get("days")},x.asDays=function(){return this.as("days")},x.weeks=function(){return this.get("weeks")},x.asWeeks=function(){return this.as("weeks")},x.months=function(){return this.get("months")},x.asMonths=function(){return this.as("months")},x.years=function(){return this.get("years")},x.asYears=function(){return this.as("years")},I}(),z=function(I,x,k){return I.add(x.years()*k,"y").add(x.months()*k,"M").add(x.days()*k,"d").add(x.hours()*k,"h").add(x.minutes()*k,"m").add(x.seconds()*k,"s").add(x.milliseconds()*k,"ms")};return function(I,x,k){n=k,i=k().$utils(),k.duration=function(T,v){var p=k.locale();return X(T,{$l:p},v)},k.isDuration=D;var E=x.prototype.add,h=x.prototype.subtract;x.prototype.add=function(T,v){return D(T)?z(this,T,1):E.bind(this)(T,v)},x.prototype.subtract=function(T,v){return D(T)?z(this,T,-1):h.bind(this)(T,v)}}})})(Ee);var Yn=Ee.exports;const An=Yt(Yn);var Rt=function(){var t=l(function(p,a,d,y){for(d=d||{},y=p.length;y--;d[p[y]]=a);return d},"o"),e=[6,8,10,12,13,14,15,16,17,18,20,21,22,23,24,25,26,27,28,29,30,31,33,35,36,38,40],n=[1,26],i=[1,27],s=[1,28],m=[1,29],f=[1,30],b=[1,31],C=[1,32],L=[1,33],w=[1,34],N=[1,9],_=[1,10],D=[1,11],X=[1,12],R=[1,13],g=[1,14],M=[1,15],W=[1,16],O=[1,19],H=[1,20],z=[1,21],I=[1,22],x=[1,23],k=[1,25],E=[1,35],h={trace:l(function(){},"trace"),yy:{},symbols_:{error:2,start:3,gantt:4,document:5,EOF:6,line:7,SPACE:8,statement:9,NL:10,weekday:11,weekday_monday:12,weekday_tuesday:13,weekday_wednesday:14,weekday_thursday:15,weekday_friday:16,weekday_saturday:17,weekday_sunday:18,weekend:19,weekend_friday:20,weekend_saturday:21,dateFormat:22,inclusiveEndDates:23,topAxis:24,axisFormat:25,tickInterval:26,excludes:27,includes:28,todayMarker:29,title:30,acc_title:31,acc_title_value:32,acc_descr:33,acc_descr_value:34,acc_descr_multiline_value:35,section:36,clickStatement:37,taskTxt:38,taskData:39,click:40,callbackname:41,callbackargs:42,href:43,clickStatementDebug:44,$accept:0,$end:1},terminals_:{2:"error",4:"gantt",6:"EOF",8:"SPACE",10:"NL",12:"weekday_monday",13:"weekday_tuesday",14:"weekday_wednesday",15:"weekday_thursday",16:"weekday_friday",17:"weekday_saturday",18:"weekday_sunday",20:"weekend_friday",21:"weekend_saturday",22:"dateFormat",23:"inclusiveEndDates",24:"topAxis",25:"axisFormat",26:"tickInterval",27:"excludes",28:"includes",29:"todayMarker",30:"title",31:"acc_title",32:"acc_title_value",33:"acc_descr",34:"acc_descr_value",35:"acc_descr_multiline_value",36:"section",38:"taskTxt",39:"taskData",40:"click",41:"callbackname",42:"callbackargs",43:"href"},productions_:[0,[3,3],[5,0],[5,2],[7,2],[7,1],[7,1],[7,1],[11,1],[11,1],[11,1],[11,1],[11,1],[11,1],[11,1],[19,1],[19,1],[9,1],[9,1],[9,1],[9,1],[9,1],[9,1],[9,1],[9,1],[9,1],[9,1],[9,1],[9,2],[9,2],[9,1],[9,1],[9,1],[9,2],[37,2],[37,3],[37,3],[37,4],[37,3],[37,4],[37,2],[44,2],[44,3],[44,3],[44,4],[44,3],[44,4],[44,2]],performAction:l(function(a,d,y,u,S,r,Y){var o=r.length-1;switch(S){case 1:return r[o-1];case 2:this.$=[];break;case 3:r[o-1].push(r[o]),this.$=r[o-1];break;case 4:case 5:this.$=r[o];break;case 6:case 7:this.$=[];break;case 8:u.setWeekday("monday");break;case 9:u.setWeekday("tuesday");break;case 10:u.setWeekday("wednesday");break;case 11:u.setWeekday("thursday");break;case 12:u.setWeekday("friday");break;case 13:u.setWeekday("saturday");break;case 14:u.setWeekday("sunday");break;case 15:u.setWeekend("friday");break;case 16:u.setWeekend("saturday");break;case 17:u.setDateFormat(r[o].substr(11)),this.$=r[o].substr(11);break;case 18:u.enableInclusiveEndDates(),this.$=r[o].substr(18);break;case 19:u.TopAxis(),this.$=r[o].substr(8);break;case 20:u.setAxisFormat(r[o].substr(11)),this.$=r[o].substr(11);break;case 21:u.setTickInterval(r[o].substr(13)),this.$=r[o].substr(13);break;case 22:u.setExcludes(r[o].substr(9)),this.$=r[o].substr(9);break;case 23:u.setIncludes(r[o].substr(9)),this.$=r[o].substr(9);break;case 24:u.setTodayMarker(r[o].substr(12)),this.$=r[o].substr(12);break;case 27:u.setDiagramTitle(r[o].substr(6)),this.$=r[o].substr(6);break;case 28:this.$=r[o].trim(),u.setAccTitle(this.$);break;case 29:case 30:this.$=r[o].trim(),u.setAccDescription(this.$);break;case 31:u.addSection(r[o].substr(8)),this.$=r[o].substr(8);break;case 33:u.addTask(r[o-1],r[o]),this.$="task";break;case 34:this.$=r[o-1],u.setClickEvent(r[o-1],r[o],null);break;case 35:this.$=r[o-2],u.setClickEvent(r[o-2],r[o-1],r[o]);break;case 36:this.$=r[o-2],u.setClickEvent(r[o-2],r[o-1],null),u.setLink(r[o-2],r[o]);break;case 37:this.$=r[o-3],u.setClickEvent(r[o-3],r[o-2],r[o-1]),u.setLink(r[o-3],r[o]);break;case 38:this.$=r[o-2],u.setClickEvent(r[o-2],r[o],null),u.setLink(r[o-2],r[o-1]);break;case 39:this.$=r[o-3],u.setClickEvent(r[o-3],r[o-1],r[o]),u.setLink(r[o-3],r[o-2]);break;case 40:this.$=r[o-1],u.setLink(r[o-1],r[o]);break;case 41:case 47:this.$=r[o-1]+" "+r[o];break;case 42:case 43:case 45:this.$=r[o-2]+" "+r[o-1]+" "+r[o];break;case 44:case 46:this.$=r[o-3]+" "+r[o-2]+" "+r[o-1]+" "+r[o];break}},"anonymous"),table:[{3:1,4:[1,2]},{1:[3]},t(e,[2,2],{5:3}),{6:[1,4],7:5,8:[1,6],9:7,10:[1,8],11:17,12:n,13:i,14:s,15:m,16:f,17:b,18:C,19:18,20:L,21:w,22:N,23:_,24:D,25:X,26:R,27:g,28:M,29:W,30:O,31:H,33:z,35:I,36:x,37:24,38:k,40:E},t(e,[2,7],{1:[2,1]}),t(e,[2,3]),{9:36,11:17,12:n,13:i,14:s,15:m,16:f,17:b,18:C,19:18,20:L,21:w,22:N,23:_,24:D,25:X,26:R,27:g,28:M,29:W,30:O,31:H,33:z,35:I,36:x,37:24,38:k,40:E},t(e,[2,5]),t(e,[2,6]),t(e,[2,17]),t(e,[2,18]),t(e,[2,19]),t(e,[2,20]),t(e,[2,21]),t(e,[2,22]),t(e,[2,23]),t(e,[2,24]),t(e,[2,25]),t(e,[2,26]),t(e,[2,27]),{32:[1,37]},{34:[1,38]},t(e,[2,30]),t(e,[2,31]),t(e,[2,32]),{39:[1,39]},t(e,[2,8]),t(e,[2,9]),t(e,[2,10]),t(e,[2,11]),t(e,[2,12]),t(e,[2,13]),t(e,[2,14]),t(e,[2,15]),t(e,[2,16]),{41:[1,40],43:[1,41]},t(e,[2,4]),t(e,[2,28]),t(e,[2,29]),t(e,[2,33]),t(e,[2,34],{42:[1,42],43:[1,43]}),t(e,[2,40],{41:[1,44]}),t(e,[2,35],{43:[1,45]}),t(e,[2,36]),t(e,[2,38],{42:[1,46]}),t(e,[2,37]),t(e,[2,39])],defaultActions:{},parseError:l(function(a,d){if(d.recoverable)this.trace(a);else{var y=new Error(a);throw y.hash=d,y}},"parseError"),parse:l(function(a){var d=this,y=[0],u=[],S=[null],r=[],Y=this.table,o="",G=0,c=0,A=2,$=1,P=r.slice.call(arguments,1),F=Object.create(this.lexer),B={yy:{}};for(var V in this.yy)Object.prototype.hasOwnProperty.call(this.yy,V)&&(B.yy[V]=this.yy[V]);F.setInput(a,B.yy),B.yy.lexer=F,B.yy.parser=this,typeof F.yylloc>"u"&&(F.yylloc={});var st=F.yylloc;r.push(st);var ot=F.options&&F.options.ranges;typeof B.yy.parseError=="function"?this.parseError=B.yy.parseError:this.parseError=Object.getPrototypeOf(this).parseError;function vt(Q){y.length=y.length-2*Q,S.length=S.length-Q,r.length=r.length-Q}l(vt,"popStack");function dt(){var Q;return Q=u.pop()||F.lex()||$,typeof Q!="number"&&(Q instanceof Array&&(u=Q,Q=u.pop()),Q=d.symbols_[Q]||Q),Q}l(dt,"lex");for(var j,K,Z,ct,J={},at,tt,ne,Tt;;){if(K=y[y.length-1],this.defaultActions[K]?Z=this.defaultActions[K]:((j===null||typeof j>"u")&&(j=dt()),Z=Y[K]&&Y[K][j]),typeof Z>"u"||!Z.length||!Z[0]){var At="";Tt=[];for(at in Y[K])this.terminals_[at]&&at>A&&Tt.push("'"+this.terminals_[at]+"'");F.showPosition?At="Parse error on line "+(G+1)+`:
`+F.showPosition()+`
Expecting `+Tt.join(", ")+", got '"+(this.terminals_[j]||j)+"'":At="Parse error on line "+(G+1)+": Unexpected "+(j==$?"end of input":"'"+(this.terminals_[j]||j)+"'"),this.parseError(At,{text:F.match,token:this.terminals_[j]||j,line:F.yylineno,loc:st,expected:Tt})}if(Z[0]instanceof Array&&Z.length>1)throw new Error("Parse Error: multiple actions possible at state: "+K+", token: "+j);switch(Z[0]){case 1:y.push(j),S.push(F.yytext),r.push(F.yylloc),y.push(Z[1]),j=null,c=F.yyleng,o=F.yytext,G=F.yylineno,st=F.yylloc;break;case 2:if(tt=this.productions_[Z[1]][1],J.$=S[S.length-tt],J._$={first_line:r[r.length-(tt||1)].first_line,last_line:r[r.length-1].last_line,first_column:r[r.length-(tt||1)].first_column,last_column:r[r.length-1].last_column},ot&&(J._$.range=[r[r.length-(tt||1)].range[0],r[r.length-1].range[1]]),ct=this.performAction.apply(J,[o,c,G,B.yy,Z[1],S,r].concat(P)),typeof ct<"u")return ct;tt&&(y=y.slice(0,-1*tt*2),S=S.slice(0,-1*tt),r=r.slice(0,-1*tt)),y.push(this.productions_[Z[1]][0]),S.push(J.$),r.push(J._$),ne=Y[y[y.length-2]][y[y.length-1]],y.push(ne);break;case 3:return!0}}return!0},"parse")},T=function(){var p={EOF:1,parseError:l(function(d,y){if(this.yy.parser)this.yy.parser.parseError(d,y);else throw new Error(d)},"parseError"),setInput:l(function(a,d){return this.yy=d||this.yy||{},this._input=a,this._more=this._backtrack=this.done=!1,this.yylineno=this.yyleng=0,this.yytext=this.matched=this.match="",this.conditionStack=["INITIAL"],this.yylloc={first_line:1,first_column:0,last_line:1,last_column:0},this.options.ranges&&(this.yylloc.range=[0,0]),this.offset=0,this},"setInput"),input:l(function(){var a=this._input[0];this.yytext+=a,this.yyleng++,this.offset++,this.match+=a,this.matched+=a;var d=a.match(/(?:\r\n?|\n).*/g);return d?(this.yylineno++,this.yylloc.last_line++):this.yylloc.last_column++,this.options.ranges&&this.yylloc.range[1]++,this._input=this._input.slice(1),a},"input"),unput:l(function(a){var d=a.length,y=a.split(/(?:\r\n?|\n)/g);this._input=a+this._input,this.yytext=this.yytext.substr(0,this.yytext.length-d),this.offset-=d;var u=this.match.split(/(?:\r\n?|\n)/g);this.match=this.match.substr(0,this.match.length-1),this.matched=this.matched.substr(0,this.matched.length-1),y.length-1&&(this.yylineno-=y.length-1);var S=this.yylloc.range;return this.yylloc={first_line:this.yylloc.first_line,last_line:this.yylineno+1,first_column:this.yylloc.first_column,last_column:y?(y.length===u.length?this.yylloc.first_column:0)+u[u.length-y.length].length-y[0].length:this.yylloc.first_column-d},this.options.ranges&&(this.yylloc.range=[S[0],S[0]+this.yyleng-d]),this.yyleng=this.yytext.length,this},"unput"),more:l(function(){return this._more=!0,this},"more"),reject:l(function(){if(this.options.backtrack_lexer)this._backtrack=!0;else return this.parseError("Lexical error on line "+(this.yylineno+1)+`. You can only invoke reject() in the lexer when the lexer is of the backtracking persuasion (options.backtrack_lexer = true).
`+this.showPosition(),{text:"",token:null,line:this.yylineno});return this},"reject"),less:l(function(a){this.unput(this.match.slice(a))},"less"),pastInput:l(function(){var a=this.matched.substr(0,this.matched.length-this.match.length);return(a.length>20?"...":"")+a.substr(-20).replace(/\n/g,"")},"pastInput"),upcomingInput:l(function(){var a=this.match;return a.length<20&&(a+=this._input.substr(0,20-a.length)),(a.substr(0,20)+(a.length>20?"...":"")).replace(/\n/g,"")},"upcomingInput"),showPosition:l(function(){var a=this.pastInput(),d=new Array(a.length+1).join("-");return a+this.upcomingInput()+`
`+d+"^"},"showPosition"),test_match:l(function(a,d){var y,u,S;if(this.options.backtrack_lexer&&(S={yylineno:this.yylineno,yylloc:{first_line:this.yylloc.first_line,last_line:this.last_line,first_column:this.yylloc.first_column,last_column:this.yylloc.last_column},yytext:this.yytext,match:this.match,matches:this.matches,matched:this.matched,yyleng:this.yyleng,offset:this.offset,_more:this._more,_input:this._input,yy:this.yy,conditionStack:this.conditionStack.slice(0),done:this.done},this.options.ranges&&(S.yylloc.range=this.yylloc.range.slice(0))),u=a[0].match(/(?:\r\n?|\n).*/g),u&&(this.yylineno+=u.length),this.yylloc={first_line:this.yylloc.last_line,last_line:this.yylineno+1,first_column:this.yylloc.last_column,last_column:u?u[u.length-1].length-u[u.length-1].match(/\r?\n?/)[0].length:this.yylloc.last_column+a[0].length},this.yytext+=a[0],this.match+=a[0],this.matches=a,this.yyleng=this.yytext.length,this.options.ranges&&(this.yylloc.range=[this.offset,this.offset+=this.yyleng]),this._more=!1,this._backtrack=!1,this._input=this._input.slice(a[0].length),this.matched+=a[0],y=this.performAction.call(this,this.yy,this,d,this.conditionStack[this.conditionStack.length-1]),this.done&&this._input&&(this.done=!1),y)return y;if(this._backtrack){for(var r in S)this[r]=S[r];return!1}return!1},"test_match"),next:l(function(){if(this.done)return this.EOF;this._input||(this.done=!0);var a,d,y,u;this._more||(this.yytext="",this.match="");for(var S=this._currentRules(),r=0;r<S.length;r++)if(y=this._input.match(this.rules[S[r]]),y&&(!d||y[0].length>d[0].length)){if(d=y,u=r,this.options.backtrack_lexer){if(a=this.test_match(y,S[r]),a!==!1)return a;if(this._backtrack){d=!1;continue}else return!1}else if(!this.options.flex)break}return d?(a=this.test_match(d,S[u]),a!==!1?a:!1):this._input===""?this.EOF:this.parseError("Lexical error on line "+(this.yylineno+1)+`. Unrecognized text.
`+this.showPosition(),{text:"",token:null,line:this.yylineno})},"next"),lex:l(function(){var d=this.next();return d||this.lex()},"lex"),begin:l(function(d){this.conditionStack.push(d)},"begin"),popState:l(function(){var d=this.conditionStack.length-1;return d>0?this.conditionStack.pop():this.conditionStack[0]},"popState"),_currentRules:l(function(){return this.conditionStack.length&&this.conditionStack[this.conditionStack.length-1]?this.conditions[this.conditionStack[this.conditionStack.length-1]].rules:this.conditions.INITIAL.rules},"_currentRules"),topState:l(function(d){return d=this.conditionStack.length-1-Math.abs(d||0),d>=0?this.conditionStack[d]:"INITIAL"},"topState"),pushState:l(function(d){this.begin(d)},"pushState"),stateStackSize:l(function(){return this.conditionStack.length},"stateStackSize"),options:{"case-insensitive":!0},performAction:l(function(d,y,u,S){switch(u){case 0:return this.begin("open_directive"),"open_directive";case 1:return this.begin("acc_title"),31;case 2:return this.popState(),"acc_title_value";case 3:return this.begin("acc_descr"),33;case 4:return this.popState(),"acc_descr_value";case 5:this.begin("acc_descr_multiline");break;case 6:this.popState();break;case 7:return"acc_descr_multiline_value";case 8:break;case 9:break;case 10:break;case 11:return 10;case 12:break;case 13:break;case 14:this.begin("href");break;case 15:this.popState();break;case 16:return 43;case 17:this.begin("callbackname");break;case 18:this.popState();break;case 19:this.popState(),this.begin("callbackargs");break;case 20:return 41;case 21:this.popState();break;case 22:return 42;case 23:this.begin("click");break;case 24:this.popState();break;case 25:return 40;case 26:return 4;case 27:return 22;case 28:return 23;case 29:return 24;case 30:return 25;case 31:return 26;case 32:return 28;case 33:return 27;case 34:return 29;case 35:return 12;case 36:return 13;case 37:return 14;case 38:return 15;case 39:return 16;case 40:return 17;case 41:return 18;case 42:return 20;case 43:return 21;case 44:return"date";case 45:return 30;case 46:return"accDescription";case 47:return 36;case 48:return 38;case 49:return 39;case 50:return":";case 51:return 6;case 52:return"INVALID"}},"anonymous"),rules:[/^(?:%%\{)/i,/^(?:accTitle\s*:\s*)/i,/^(?:(?!\n||)*[^\n]*)/i,/^(?:accDescr\s*:\s*)/i,/^(?:(?!\n||)*[^\n]*)/i,/^(?:accDescr\s*\{\s*)/i,/^(?:[\}])/i,/^(?:[^\}]*)/i,/^(?:%%(?!\{)*[^\n]*)/i,/^(?:[^\}]%%*[^\n]*)/i,/^(?:%%*[^\n]*[\n]*)/i,/^(?:[\n]+)/i,/^(?:\s+)/i,/^(?:%[^\n]*)/i,/^(?:href[\s]+["])/i,/^(?:["])/i,/^(?:[^"]*)/i,/^(?:call[\s]+)/i,/^(?:\([\s]*\))/i,/^(?:\()/i,/^(?:[^(]*)/i,/^(?:\))/i,/^(?:[^)]*)/i,/^(?:click[\s]+)/i,/^(?:[\s\n])/i,/^(?:[^\s\n]*)/i,/^(?:gantt\b)/i,/^(?:dateFormat\s[^#\n;]+)/i,/^(?:inclusiveEndDates\b)/i,/^(?:topAxis\b)/i,/^(?:axisFormat\s[^#\n;]+)/i,/^(?:tickInterval\s[^#\n;]+)/i,/^(?:includes\s[^#\n;]+)/i,/^(?:excludes\s[^#\n;]+)/i,/^(?:todayMarker\s[^\n;]+)/i,/^(?:weekday\s+monday\b)/i,/^(?:weekday\s+tuesday\b)/i,/^(?:weekday\s+wednesday\b)/i,/^(?:weekday\s+thursday\b)/i,/^(?:weekday\s+friday\b)/i,/^(?:weekday\s+saturday\b)/i,/^(?:weekday\s+sunday\b)/i,/^(?:weekend\s+friday\b)/i,/^(?:weekend\s+saturday\b)/i,/^(?:\d\d\d\d-\d\d-\d\d\b)/i,/^(?:title\s[^\n]+)/i,/^(?:accDescription\s[^#\n;]+)/i,/^(?:section\s[^\n]+)/i,/^(?:[^:\n]+)/i,/^(?::[^#\n;]+)/i,/^(?::)/i,/^(?:$)/i,/^(?:.)/i],conditions:{acc_descr_multiline:{rules:[6,7],inclusive:!1},acc_descr:{rules:[4],inclusive:!1},acc_title:{rules:[2],inclusive:!1},callbackargs:{rules:[21,22],inclusive:!1},callbackname:{rules:[18,19,20],inclusive:!1},href:{rules:[15,16],inclusive:!1},click:{rules:[24,25],inclusive:!1},INITIAL:{rules:[0,1,3,5,8,9,10,11,12,13,14,17,23,26,27,28,29,30,31,32,33,34,35,36,37,38,39,40,41,42,43,44,45,46,47,48,49,50,51,52],inclusive:!0}}};return p}();h.lexer=T;function v(){this.yy={}}return l(v,"Parser"),v.prototype=h,h.Parser=v,new v}();Rt.parser=Rt;var $n=Rt;q.extend(Sn);q.extend(Mn);q.extend(In);var de={friday:5,saturday:6},et="",Xt="",jt=void 0,Ut="",yt=[],gt=[],qt=new Map,Zt=[],Mt=[],pt="",Qt="",Ie=["active","done","crit","milestone","vert"],Kt=[],ft="",xt=!1,Jt=!1,te="sunday",Et="saturday",Ht=0,Fn=l(function(){Zt=[],Mt=[],pt="",Kt=[],Dt=0,Gt=void 0,St=void 0,U=[],et="",Xt="",Qt="",jt=void 0,Ut="",yt=[],gt=[],xt=!1,Jt=!1,Ht=0,qt=new Map,ft="",Ge(),te="sunday",Et="saturday"},"clear"),Ln=l(function(t){ft=t},"setDiagramId"),On=l(function(t){Xt=t},"setAxisFormat"),Wn=l(function(){return Xt},"getAxisFormat"),Nn=l(function(t){jt=t},"setTickInterval"),Vn=l(function(){return jt},"getTickInterval"),Pn=l(function(t){Ut=t},"setTodayMarker"),zn=l(function(){return Ut},"getTodayMarker"),Rn=l(function(t){et=t},"setDateFormat"),Hn=l(function(){xt=!0},"enableInclusiveEndDates"),Bn=l(function(){return xt},"endDatesAreInclusive"),Gn=l(function(){Jt=!0},"enableTopAxis"),Xn=l(function(){return Jt},"topAxisEnabled"),jn=l(function(t){Qt=t},"setDisplayMode"),Un=l(function(){return Qt},"getDisplayMode"),qn=l(function(){return et},"getDateFormat"),Ye=l((t,e)=>{const n=e.toLowerCase().split(/[\s,]+/).filter(i=>i!=="");return[...new Set([...t,...n])]},"mergeTokens"),Zn=l(function(t){yt=Ye(yt,t)},"setIncludes"),Qn=l(function(){return yt},"getIncludes"),Kn=l(function(t){gt=Ye(gt,t)},"setExcludes"),Jn=l(function(){return gt},"getExcludes"),ti=l(function(){return qt},"getLinks"),ei=l(function(t){pt=t,Zt.push(t)},"addSection"),ni=l(function(){return Zt},"getSections"),ii=l(function(){let t=fe();const e=10;let n=0;for(;!t&&n<e;)t=fe(),n++;return Mt=U,Mt},"getTasks"),Ae=l(function(t,e,n,i){const s=t.format(e.trim()),m=t.format("YYYY-MM-DD");return i.includes(s)||i.includes(m)?!1:n.includes("weekends")&&(t.isoWeekday()===de[Et]||t.isoWeekday()===de[Et]+1)||n.includes(t.format("dddd").toLowerCase())?!0:n.includes(s)||n.includes(m)},"isInvalidDate"),ri=l(function(t){te=t},"setWeekday"),si=l(function(){return te},"getWeekday"),ai=l(function(t){Et=t},"setWeekend"),$e=l(function(t,e,n,i){if(!n.length||t.manualEndTime)return;let s;t.startTime instanceof Date?s=q(t.startTime):s=q(t.startTime,e,!0),s=s.add(1,"d");let m;t.endTime instanceof Date?m=q(t.endTime):m=q(t.endTime,e,!0);const[f,b]=oi(s,m,e,n,i);t.endTime=f.toDate(),t.renderEndTime=b},"checkTaskDates"),oi=l(function(t,e,n,i,s){let m=!1,f=null;const b=e.add(1e4,"d");for(;t<=e;){if(m||(f=e.toDate()),m=Ae(t,n,i,s),m&&(e=e.add(1,"d"),e>b))throw new Error("Failed to find a valid date that was not excluded by `excludes` after 10,000 iterations.");t=t.add(1,"d")}return[e,f]},"fixTaskDates"),Bt=l(function(t,e,n){if(n=n.trim(),l(b=>{const C=b.trim();return C==="x"||C==="X"},"isTimestampFormat")(e)&&/^\d+$/.test(n))return new Date(Number(n));const m=/^after\s+(?<ids>[\d\w- ]+)/.exec(n);if(m!==null){let b=null;for(const L of m.groups.ids.split(" ")){let w=ut(L);w!==void 0&&(!b||w.endTime>b.endTime)&&(b=w)}if(b)return b.endTime;const C=new Date;return C.setHours(0,0,0,0),C}let f=q(n,e.trim(),!0);if(f.isValid())return f.toDate();{lt.debug("Invalid date:"+n),lt.debug("With date format:"+e.trim());const b=new Date(n);if(b===void 0||isNaN(b.getTime())||b.getFullYear()<-1e4||b.getFullYear()>1e4)throw new Error("Invalid date:"+n);return b}},"getStartDate"),Fe=l(function(t){const e=/^(\d+(?:\.\d+)?)([Mdhmswy]|ms)$/.exec(t.trim());return e!==null?[Number.parseFloat(e[1]),e[2]]:[NaN,"ms"]},"parseDuration"),Le=l(function(t,e,n,i=!1){n=n.trim();const m=/^until\s+(?<ids>[\d\w- ]+)/.exec(n);if(m!==null){let w=null;for(const _ of m.groups.ids.split(" ")){let D=ut(_);D!==void 0&&(!w||D.startTime<w.startTime)&&(w=D)}if(w)return w.startTime;const N=new Date;return N.setHours(0,0,0,0),N}let f=q(n,e.trim(),!0);if(f.isValid())return i&&(f=f.add(1,"d")),f.toDate();let b=q(t);const[C,L]=Fe(n);if(!Number.isNaN(C)){const w=b.add(C,L);w.isValid()&&(b=w)}return b.toDate()},"getEndDate"),Dt=0,kt=l(function(t){return t===void 0?(Dt=Dt+1,"task"+Dt):t},"parseId"),ci=l(function(t,e){let n;e.substr(0,1)===":"?n=e.substr(1,e.length):n=e;const i=n.split(","),s={};ee(i,s,Ie);for(let f=0;f<i.length;f++)i[f]=i[f].trim();let m="";switch(i.length){case 1:s.id=kt(),s.startTime=t.endTime,m=i[0];break;case 2:s.id=kt(),s.startTime=Bt(void 0,et,i[0]),m=i[1];break;case 3:s.id=kt(i[0]),s.startTime=Bt(void 0,et,i[1]),m=i[2];break}return m&&(s.endTime=Le(s.startTime,et,m,xt),s.manualEndTime=q(m,"YYYY-MM-DD",!0).isValid(),$e(s,et,gt,yt)),s},"compileData"),li=l(function(t,e){let n;e.substr(0,1)===":"?n=e.substr(1,e.length):n=e;const i=n.split(","),s={};ee(i,s,Ie);for(let m=0;m<i.length;m++)i[m]=i[m].trim();switch(i.length){case 1:s.id=kt(),s.startTime={type:"prevTaskEnd",id:t},s.endTime={data:i[0]};break;case 2:s.id=kt(),s.startTime={type:"getStartDate",startData:i[0]},s.endTime={data:i[1]};break;case 3:s.id=kt(i[0]),s.startTime={type:"getStartDate",startData:i[1]},s.endTime={data:i[2]};break}return s},"parseData"),Gt,St,U=[],Oe={},ui=l(function(t,e){const n={section:pt,type:pt,processed:!1,manualEndTime:!1,renderEndTime:null,raw:{data:e},task:t,classes:[]},i=li(St,e);n.raw.startTime=i.startTime,n.raw.endTime=i.endTime,n.id=i.id,n.prevTaskId=St,n.active=i.active,n.done=i.done,n.crit=i.crit,n.milestone=i.milestone,n.vert=i.vert,n.vert?n.order=-1:(n.order=Ht,Ht++);const s=U.push(n);St=n.id,Oe[n.id]=s-1},"addTask"),ut=l(function(t){const e=Oe[t];return U[e]},"findTaskById"),di=l(function(t,e){const n={section:pt,type:pt,description:t,task:t,classes:[]},i=ci(Gt,e);n.startTime=i.startTime,n.endTime=i.endTime,n.id=i.id,n.active=i.active,n.done=i.done,n.crit=i.crit,n.milestone=i.milestone,n.vert=i.vert,Gt=n,Mt.push(n)},"addTaskOrg"),fe=l(function(){const t=l(function(n){const i=U[n];let s="";switch(U[n].raw.startTime.type){case"prevTaskEnd":{const m=ut(i.prevTaskId);i.startTime=m.endTime;break}case"getStartDate":s=Bt(void 0,et,U[n].raw.startTime.startData),s&&(U[n].startTime=s);break}return U[n].startTime&&(U[n].endTime=Le(U[n].startTime,et,U[n].raw.endTime.data,xt),U[n].endTime&&(U[n].processed=!0,U[n].manualEndTime=q(U[n].raw.endTime.data,"YYYY-MM-DD",!0).isValid(),$e(U[n],et,gt,yt))),U[n].processed},"compileTask");let e=!0;for(const[n,i]of U.entries())t(n),e=e&&i.processed;return e},"compileTasks"),fi=l(function(t,e){let n=e;ht().securityLevel!=="loose"&&(n=Xe(e)),t.split(",").forEach(function(i){ut(i)!==void 0&&(Ne(i,()=>{window.open(n,"_self")}),qt.set(i,n))}),We(t,"clickable")},"setLink"),We=l(function(t,e){t.split(",").forEach(function(n){let i=ut(n);i!==void 0&&i.classes.push(e)})},"setClass"),hi=l(function(t,e,n){if(ht().securityLevel!=="loose"||e===void 0)return;let i=[];if(typeof n=="string"){i=n.split(/,(?=(?:(?:[^"]*"){2})*[^"]*$)/);for(let m=0;m<i.length;m++){let f=i[m].trim();f.startsWith('"')&&f.endsWith('"')&&(f=f.substr(1,f.length-2)),i[m]=f}}i.length===0&&i.push(t),ut(t)!==void 0&&Ne(t,()=>{qe.runFunc(e,...i)})},"setClickFun"),Ne=l(function(t,e){Kt.push(function(){const n=ft?`${ft}-${t}`:t,i=document.querySelector(`[id="${n}"]`);i!==null&&i.addEventListener("click",function(){e()})},function(){const n=ft?`${ft}-${t}`:t,i=document.querySelector(`[id="${n}-text"]`);i!==null&&i.addEventListener("click",function(){e()})})},"pushFun"),mi=l(function(t,e,n){t.split(",").forEach(function(i){hi(i,e,n)}),We(t,"clickable")},"setClickEvent"),ki=l(function(t){Kt.forEach(function(e){e(t)})},"bindFunctions"),yi={getConfig:l(()=>ht().gantt,"getConfig"),clear:Fn,setDateFormat:Rn,getDateFormat:qn,enableInclusiveEndDates:Hn,endDatesAreInclusive:Bn,enableTopAxis:Gn,topAxisEnabled:Xn,setAxisFormat:On,getAxisFormat:Wn,setTickInterval:Nn,getTickInterval:Vn,setTodayMarker:Pn,getTodayMarker:zn,setAccTitle:Ve,getAccTitle:Pe,setDiagramTitle:ze,getDiagramTitle:Re,setDiagramId:Ln,setDisplayMode:jn,getDisplayMode:Un,setAccDescription:He,getAccDescription:Be,addSection:ei,getSections:ni,getTasks:ii,addTask:ui,findTaskById:ut,addTaskOrg:di,setIncludes:Zn,getIncludes:Qn,setExcludes:Kn,getExcludes:Jn,setClickEvent:mi,setLink:fi,getLinks:ti,bindFunctions:ki,parseDuration:Fe,isInvalidDate:Ae,setWeekday:ri,getWeekday:si,setWeekend:ai};function ee(t,e,n){let i=!0;for(;i;)i=!1,n.forEach(function(s){const m="^\\s*"+s+"\\s*$",f=new RegExp(m);t[0].match(f)&&(e[s]=!0,t.shift(1),i=!0)})}l(ee,"getTaskTags");q.extend(An);var gi=l(function(){lt.debug("Something is calling, setConf, remove the call")},"setConf"),he={monday:nn,tuesday:rn,wednesday:sn,thursday:an,friday:on,saturday:cn,sunday:ln},pi=l((t,e)=>{let n=[...t].map(()=>-1/0),i=[...t].sort((m,f)=>m.startTime-f.startTime||m.order-f.order),s=0;for(const m of i)for(let f=0;f<n.length;f++)if(m.startTime>=n[f]){n[f]=m.endTime,m.order=f+e,f>s&&(s=f);break}return s},"getMaxIntersections"),it,Vt=1e4,vi=l(function(t,e,n,i){const s=ht().gantt;i.db.setDiagramId(e);const m=ht().securityLevel;let f;m==="sandbox"&&(f=bt("#i"+e));const b=m==="sandbox"?bt(f.nodes()[0].contentDocument.body):bt("body"),C=m==="sandbox"?f.nodes()[0].contentDocument:document,L=C.getElementById(e);it=L.parentElement.offsetWidth,it===void 0&&(it=1200),s.useWidth!==void 0&&(it=s.useWidth);const w=i.db.getTasks(),N=w.filter(h=>!h.vert);let _=[];for(const h of N)_.push(h.type);_=E(_);const D={};let X=2*s.topPadding;if(i.db.getDisplayMode()==="compact"||s.displayMode==="compact"){const h={};for(const v of N)h[v.section]===void 0?h[v.section]=[v]:h[v.section].push(v);let T=0;for(const v of Object.keys(h)){const p=pi(h[v],T)+1;T+=p,X+=p*(s.barHeight+s.barGap),D[v]=p}}else{X+=N.length*(s.barHeight+s.barGap);for(const h of _)D[h]=N.filter(T=>T.type===h).length}L.setAttribute("viewBox","0 0 "+it+" "+X);const R=b.select(`[id="${e}"]`),g=Ke().domain([Je(w,function(h){return h.startTime}),tn(w,function(h){return h.endTime})]).rangeRound([0,it-s.leftPadding-s.rightPadding]);function M(h,T){const v=h.startTime,p=T.startTime;let a=0;return v>p?a=1:v<p&&(a=-1),a}l(M,"taskCompare"),w.sort(M),W(w,it,X),je(R,X,it,s.useMaxWidth),R.append("text").text(i.db.getDiagramTitle()).attr("x",it/2).attr("y",s.titleTopMargin).attr("class","titleText");function W(h,T,v){const p=s.barHeight,a=p+s.barGap,d=s.topPadding,y=s.leftPadding,u=en().domain([0,_.length]).range(["#00B9FA","#F95002"]).interpolate(yn);H(a,d,y,T,v,h,i.db.getExcludes(),i.db.getIncludes()),I(y,d,T,v),O(h,a,d,y,p,u,T),x(a,d),k(y,d,T,v)}l(W,"makeGantt");function O(h,T,v,p,a,d,y){h.sort((c,A)=>c.vert===A.vert?0:c.vert?1:-1);const u=h.filter(c=>!c.vert),r=[...new Set(u.map(c=>c.order))].map(c=>u.find(A=>A.order===c));R.append("g").selectAll("rect").data(r).enter().append("rect").attr("x",0).attr("y",function(c,A){return A=c.order,A*T+v-2}).attr("width",function(){return y-s.rightPadding/2}).attr("height",T).attr("class",function(c){for(const[A,$]of _.entries())if(c.type===$)return"section section"+A%s.numberSectionStyles;return"section section0"}).enter();const Y=R.append("g").selectAll("rect").data(h).enter(),o=i.db.getLinks();if(Y.append("rect").attr("id",function(c){return e+"-"+c.id}).attr("rx",3).attr("ry",3).attr("x",function(c){return c.milestone?g(c.startTime)+p+.5*(g(c.endTime)-g(c.startTime))-.5*a:g(c.startTime)+p}).attr("y",function(c,A){return A=c.order,c.vert?s.gridLineStartPadding:A*T+v}).attr("width",function(c){return c.milestone?a:c.vert?.08*a:g(c.renderEndTime||c.endTime)-g(c.startTime)}).attr("height",function(c){return c.vert?u.length*(s.barHeight+s.barGap)+s.barHeight*2:a}).attr("transform-origin",function(c,A){return A=c.order,(g(c.startTime)+p+.5*(g(c.endTime)-g(c.startTime))).toString()+"px "+(A*T+v+.5*a).toString()+"px"}).attr("class",function(c){const A="task";let $="";c.classes.length>0&&($=c.classes.join(" "));let P=0;for(const[B,V]of _.entries())c.type===V&&(P=B%s.numberSectionStyles);let F="";return c.active?c.crit?F+=" activeCrit":F=" active":c.done?c.crit?F=" doneCrit":F=" done":c.crit&&(F+=" crit"),F.length===0&&(F=" task"),c.milestone&&(F=" milestone "+F),c.vert&&(F=" vert "+F),F+=P,F+=" "+$,A+F}),Y.append("text").attr("id",function(c){return e+"-"+c.id+"-text"}).text(function(c){return c.task}).attr("font-size",s.fontSize).attr("x",function(c){let A=g(c.startTime),$=g(c.renderEndTime||c.endTime);if(c.milestone&&(A+=.5*(g(c.endTime)-g(c.startTime))-.5*a,$=A+a),c.vert)return g(c.startTime)+p;const P=this.getBBox().width;return P>$-A?$+P+1.5*s.leftPadding>y?A+p-5:$+p+5:($-A)/2+A+p}).attr("y",function(c,A){return c.vert?s.gridLineStartPadding+u.length*(s.barHeight+s.barGap)+60:(A=c.order,A*T+s.barHeight/2+(s.fontSize/2-2)+v)}).attr("text-height",a).attr("class",function(c){const A=g(c.startTime);let $=g(c.endTime);c.milestone&&($=A+a);const P=this.getBBox().width;let F="";c.classes.length>0&&(F=c.classes.join(" "));let B=0;for(const[st,ot]of _.entries())c.type===ot&&(B=st%s.numberSectionStyles);let V="";return c.active&&(c.crit?V="activeCritText"+B:V="activeText"+B),c.done?c.crit?V=V+" doneCritText"+B:V=V+" doneText"+B:c.crit&&(V=V+" critText"+B),c.milestone&&(V+=" milestoneText"),c.vert&&(V+=" vertText"),P>$-A?$+P+1.5*s.leftPadding>y?F+" taskTextOutsideLeft taskTextOutside"+B+" "+V:F+" taskTextOutsideRight taskTextOutside"+B+" "+V+" width-"+P:F+" taskText taskText"+B+" "+V+" width-"+P}),ht().securityLevel==="sandbox"){let c;c=bt("#i"+e);const A=c.nodes()[0].contentDocument;Y.filter(function($){return o.has($.id)}).each(function($){var P=A.querySelector("#"+CSS.escape(e+"-"+$.id)),F=A.querySelector("#"+CSS.escape(e+"-"+$.id+"-text"));const B=P.parentNode;var V=A.createElement("a");V.setAttribute("xlink:href",o.get($.id)),V.setAttribute("target","_top"),B.appendChild(V),V.appendChild(P),V.appendChild(F)})}}l(O,"drawRects");function H(h,T,v,p,a,d,y,u){if(y.length===0&&u.length===0)return;let S,r;for(const{startTime:$,endTime:P}of d)(S===void 0||$<S)&&(S=$),(r===void 0||P>r)&&(r=P);if(!S||!r)return;if(q(r).diff(q(S),"year")>5){lt.warn("The difference between the min and max time is more than 5 years. This will cause performance issues. Skipping drawing exclude days.");return}const Y=i.db.getDateFormat(),o=[];let G=null,c=q(S);for(;c.valueOf()<=r;)i.db.isInvalidDate(c,Y,y,u)?G?G.end=c:G={start:c,end:c}:G&&(o.push(G),G=null),c=c.add(1,"d");R.append("g").selectAll("rect").data(o).enter().append("rect").attr("id",$=>e+"-exclude-"+$.start.format("YYYY-MM-DD")).attr("x",$=>g($.start.startOf("day"))+v).attr("y",s.gridLineStartPadding).attr("width",$=>g($.end.endOf("day"))-g($.start.startOf("day"))).attr("height",a-T-s.gridLineStartPadding).attr("transform-origin",function($,P){return(g($.start)+v+.5*(g($.end)-g($.start))).toString()+"px "+(P*h+.5*a).toString()+"px"}).attr("class","exclude-range")}l(H,"drawExcludeDays");function z(h,T,v,p){if(v<=0||h>T)return 1/0;const a=T-h,d=q.duration({[p??"day"]:v}).asMilliseconds();return d<=0?1/0:Math.ceil(a/d)}l(z,"getEstimatedTickCount");function I(h,T,v,p){const a=i.db.getDateFormat(),d=i.db.getAxisFormat();let y;d?y=d:a==="D"?y="%d":y=s.axisFormat??"%Y-%m-%d";let u=_n(g).tickSize(-p+T+s.gridLineStartPadding).tickFormat(ie(y));const r=/^([1-9]\d*)(millisecond|second|minute|hour|day|week|month)$/.exec(i.db.getTickInterval()||s.tickInterval);if(r!==null){const Y=parseInt(r[1],10);if(isNaN(Y)||Y<=0)lt.warn(`Invalid tick interval value: "${r[1]}". Skipping custom tick interval.`);else{const o=r[2],G=i.db.getWeekday()||s.weekday,c=g.domain(),A=c[0],$=c[1],P=z(A,$,Y,o);if(P>Vt)lt.warn(`The tick interval "${Y}${o}" would generate ${P} ticks, which exceeds the maximum allowed (${Vt}). This may indicate an invalid date or time range. Skipping custom tick interval.`);else switch(o){case"millisecond":u.ticks(le.every(Y));break;case"second":u.ticks(ce.every(Y));break;case"minute":u.ticks(oe.every(Y));break;case"hour":u.ticks(ae.every(Y));break;case"day":u.ticks(se.every(Y));break;case"week":u.ticks(he[G].every(Y));break;case"month":u.ticks(re.every(Y));break}}}if(R.append("g").attr("class","grid").attr("transform","translate("+h+", "+(p-50)+")").call(u).selectAll("text").style("text-anchor","middle").attr("fill","#000").attr("stroke","none").attr("font-size",10).attr("dy","1em"),i.db.topAxisEnabled()||s.topAxis){let Y=wn(g).tickSize(-p+T+s.gridLineStartPadding).tickFormat(ie(y));if(r!==null){const o=parseInt(r[1],10);if(isNaN(o)||o<=0)lt.warn(`Invalid tick interval value: "${r[1]}". Skipping custom tick interval.`);else{const G=r[2],c=i.db.getWeekday()||s.weekday,A=g.domain(),$=A[0],P=A[1];if(z($,P,o,G)<=Vt)switch(G){case"millisecond":Y.ticks(le.every(o));break;case"second":Y.ticks(ce.every(o));break;case"minute":Y.ticks(oe.every(o));break;case"hour":Y.ticks(ae.every(o));break;case"day":Y.ticks(se.every(o));break;case"week":Y.ticks(he[c].every(o));break;case"month":Y.ticks(re.every(o));break}}}R.append("g").attr("class","grid").attr("transform","translate("+h+", "+T+")").call(Y).selectAll("text").style("text-anchor","middle").attr("fill","#000").attr("stroke","none").attr("font-size",10)}}l(I,"makeGrid");function x(h,T){let v=0;const p=Object.keys(D).map(a=>[a,D[a]]);R.append("g").selectAll("text").data(p).enter().append(function(a){const d=a[0].split(Ue.lineBreakRegex),y=-(d.length-1)/2,u=C.createElementNS("http://www.w3.org/2000/svg","text");u.setAttribute("dy",y+"em");for(const[S,r]of d.entries()){const Y=C.createElementNS("http://www.w3.org/2000/svg","tspan");Y.setAttribute("alignment-baseline","central"),Y.setAttribute("x","10"),S>0&&Y.setAttribute("dy","1em"),Y.textContent=r,u.appendChild(Y)}return u}).attr("x",10).attr("y",function(a,d){if(d>0)for(let y=0;y<d;y++)return v+=p[d-1][1],a[1]*h/2+v*h+T;else return a[1]*h/2+T}).attr("font-size",s.sectionFontSize).attr("class",function(a){for(const[d,y]of _.entries())if(a[0]===y)return"sectionTitle sectionTitle"+d%s.numberSectionStyles;return"sectionTitle"})}l(x,"vertLabels");function k(h,T,v,p){const a=i.db.getTodayMarker();if(a==="off")return;const d=R.append("g").attr("class","today"),y=new Date,u=d.append("line");u.attr("x1",g(y)+h).attr("x2",g(y)+h).attr("y1",s.titleTopMargin).attr("y2",p-s.titleTopMargin).attr("class","today"),a!==""&&u.attr("style",a.replace(/,/g,";"))}l(k,"drawToday");function E(h){const T={},v=[];for(let p=0,a=h.length;p<a;++p)Object.prototype.hasOwnProperty.call(T,h[p])||(T[h[p]]=!0,v.push(h[p]));return v}l(E,"checkUnique")},"draw"),xi={setConf:gi,draw:vi},Ti=l(t=>`
  .mermaid-main-font {
        font-family: ${t.fontFamily};
  }

  .exclude-range {
    fill: ${t.excludeBkgColor};
  }

  .section {
    stroke: none;
    opacity: 0.2;
  }

  .section0 {
    fill: ${t.sectionBkgColor};
  }

  .section2 {
    fill: ${t.sectionBkgColor2};
  }

  .section1,
  .section3 {
    fill: ${t.altSectionBkgColor};
    opacity: 0.2;
  }

  .sectionTitle0 {
    fill: ${t.titleColor};
  }

  .sectionTitle1 {
    fill: ${t.titleColor};
  }

  .sectionTitle2 {
    fill: ${t.titleColor};
  }

  .sectionTitle3 {
    fill: ${t.titleColor};
  }

  .sectionTitle {
    text-anchor: start;
    font-family: ${t.fontFamily};
  }


  /* Grid and axis */

  .grid .tick {
    stroke: ${t.gridColor};
    opacity: 0.8;
    shape-rendering: crispEdges;
  }

  .grid .tick text {
    font-family: ${t.fontFamily};
    fill: ${t.textColor};
  }

  .grid path {
    stroke-width: 0;
  }


  /* Today line */

  .today {
    fill: none;
    stroke: ${t.todayLineColor};
    stroke-width: 2px;
  }


  /* Task styling */

  /* Default task */

  .task {
    stroke-width: 2;
  }

  .taskText {
    text-anchor: middle;
    font-family: ${t.fontFamily};
  }

  .taskTextOutsideRight {
    fill: ${t.taskTextDarkColor};
    text-anchor: start;
    font-family: ${t.fontFamily};
  }

  .taskTextOutsideLeft {
    fill: ${t.taskTextDarkColor};
    text-anchor: end;
  }


  /* Special case clickable */

  .task.clickable {
    cursor: pointer;
  }

  .taskText.clickable {
    cursor: pointer;
    fill: ${t.taskTextClickableColor} !important;
    font-weight: bold;
  }

  .taskTextOutsideLeft.clickable {
    cursor: pointer;
    fill: ${t.taskTextClickableColor} !important;
    font-weight: bold;
  }

  .taskTextOutsideRight.clickable {
    cursor: pointer;
    fill: ${t.taskTextClickableColor} !important;
    font-weight: bold;
  }


  /* Specific task settings for the sections*/

  .taskText0,
  .taskText1,
  .taskText2,
  .taskText3 {
    fill: ${t.taskTextColor};
  }

  .task0,
  .task1,
  .task2,
  .task3 {
    fill: ${t.taskBkgColor};
    stroke: ${t.taskBorderColor};
  }

  .taskTextOutside0,
  .taskTextOutside2
  {
    fill: ${t.taskTextOutsideColor};
  }

  .taskTextOutside1,
  .taskTextOutside3 {
    fill: ${t.taskTextOutsideColor};
  }


  /* Active task */

  .active0,
  .active1,
  .active2,
  .active3 {
    fill: ${t.activeTaskBkgColor};
    stroke: ${t.activeTaskBorderColor};
  }

  .activeText0,
  .activeText1,
  .activeText2,
  .activeText3 {
    fill: ${t.taskTextDarkColor} !important;
  }


  /* Completed task */

  .done0,
  .done1,
  .done2,
  .done3 {
    stroke: ${t.doneTaskBorderColor};
    fill: ${t.doneTaskBkgColor};
    stroke-width: 2;
  }

  .doneText0,
  .doneText1,
  .doneText2,
  .doneText3 {
    fill: ${t.taskTextDarkColor} !important;
  }

  /* Done task text displayed outside the bar sits against the diagram background,
     not against the done-task bar, so it must use the outside/contrast color. */
  .doneText0.taskTextOutsideLeft,
  .doneText0.taskTextOutsideRight,
  .doneText1.taskTextOutsideLeft,
  .doneText1.taskTextOutsideRight,
  .doneText2.taskTextOutsideLeft,
  .doneText2.taskTextOutsideRight,
  .doneText3.taskTextOutsideLeft,
  .doneText3.taskTextOutsideRight {
    fill: ${t.taskTextOutsideColor} !important;
  }


  /* Tasks on the critical line */

  .crit0,
  .crit1,
  .crit2,
  .crit3 {
    stroke: ${t.critBorderColor};
    fill: ${t.critBkgColor};
    stroke-width: 2;
  }

  .activeCrit0,
  .activeCrit1,
  .activeCrit2,
  .activeCrit3 {
    stroke: ${t.critBorderColor};
    fill: ${t.activeTaskBkgColor};
    stroke-width: 2;
  }

  .doneCrit0,
  .doneCrit1,
  .doneCrit2,
  .doneCrit3 {
    stroke: ${t.critBorderColor};
    fill: ${t.doneTaskBkgColor};
    stroke-width: 2;
    cursor: pointer;
    shape-rendering: crispEdges;
  }

  .milestone {
    transform: rotate(45deg) scale(0.8,0.8);
  }

  .milestoneText {
    font-style: italic;
  }
  .doneCritText0,
  .doneCritText1,
  .doneCritText2,
  .doneCritText3 {
    fill: ${t.taskTextDarkColor} !important;
  }

  /* Done-crit task text outside the bar — same reasoning as doneText above. */
  .doneCritText0.taskTextOutsideLeft,
  .doneCritText0.taskTextOutsideRight,
  .doneCritText1.taskTextOutsideLeft,
  .doneCritText1.taskTextOutsideRight,
  .doneCritText2.taskTextOutsideLeft,
  .doneCritText2.taskTextOutsideRight,
  .doneCritText3.taskTextOutsideLeft,
  .doneCritText3.taskTextOutsideRight {
    fill: ${t.taskTextOutsideColor} !important;
  }

  .vert {
    stroke: ${t.vertLineColor};
  }

  .vertText {
    font-size: 15px;
    text-anchor: middle;
    fill: ${t.vertLineColor} !important;
  }

  .activeCritText0,
  .activeCritText1,
  .activeCritText2,
  .activeCritText3 {
    fill: ${t.taskTextDarkColor} !important;
  }

  .titleText {
    text-anchor: middle;
    font-size: 18px;
    fill: ${t.titleColor||t.textColor};
    font-family: ${t.fontFamily};
  }
`,"getStyles"),bi=Ti,Di={parser:$n,db:yi,renderer:xi,styles:bi};export{Di as diagram};
