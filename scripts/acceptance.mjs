// Opt-in real HTTPS acceptance. Uses existing personal token files; never prints tokens.
import fs from 'node:fs/promises';
import path from 'node:path';
import {spawn} from 'node:child_process';
import {randomUUID} from 'node:crypto';
const args=process.argv.slice(2),value=(n,d)=>{const i=args.indexOf(n);return i<0?d:args[i+1]};
if(!args.includes('--live'))throw Error('Pass --live; providers/research incur upstream usage. Set SOSEEK_URL, SOSEEK_TOKEN_FILE, and SOSEEK_OTHER_TOKEN_FILE for isolation.');
const suite=value('--suite','isolation'),binary=value('--binary',path.resolve('bin/soseek'));
const output=path.resolve(value('--out','output/acceptance-'+Date.now()));
await fs.mkdir(output,{recursive:true,mode:0o700});
const save=(n,v)=>fs.writeFile(path.join(output,n+'.json'),JSON.stringify(v,null,2)+'\n',{mode:0o600});
const limited=d=>d&&typeof d==='object'&&(Object.entries(d).some(([k,v])=>['rate_limited','upstream_rate_limited','http_429'].includes(v)||(k==='retry_after_seconds'&&v>0)||(k==='status'&&v===429))||Object.values(d).some(limited));
const records=[];
async function cli(name,argv,extra={}){
 const r=await new Promise(resolve=>{let stdout='',stderr='';const start=Date.now();const c=spawn(binary,argv,{env:{...process.env,...extra},stdio:['ignore','pipe','pipe']});const timer=setTimeout(()=>c.kill('SIGTERM'),240000);c.stdout.on('data',d=>stdout+=d);c.stderr.on('data',d=>stderr+=d);c.on('error',e=>{clearTimeout(timer);resolve({exit:-1,error:e.message})});c.on('close',exit=>{clearTimeout(timer);let result;try{result=JSON.parse(stdout)}catch{result={parse_error:true}}resolve({exit,result,stderr,elapsed_ms:Date.now()-start})})});
 await save(name,r);records.push({name,exit:r.exit,status:r.result?.status,run_id:r.result?.run_id,elapsed_ms:r.elapsed_ms,source_count:r.result?.results?.length,stop_reason:r.result?.stop_reason});await save('summary',records);
 console.log(JSON.stringify(records.at(-1)));
 if(limited(r))throw Error('Upstream rate limit observed; stopped without retry.');
 return r.result;
}
async function api(route,{method='GET',body,tokenFile=process.env.SOSEEK_TOKEN_FILE,headers={}}={}){
 const token=tokenFile?await fs.readFile(tokenFile,'utf8'):'invalid-test-token';
 const response=await fetch(new URL(route,process.env.SOSEEK_URL),{method,redirect:'error',headers:{Authorization:'Bearer '+token.trim(),'Content-Type':'application/json',...headers},body:body===undefined?undefined:JSON.stringify(body),signal:AbortSignal.timeout(20000)});
 const text=await response.text();let data;try{data=JSON.parse(text)}catch{data=text}return {status:response.status,headers:Object.fromEntries(response.headers),data};
}
function check(ok,msg){if(!ok)throw Error(msg)}
if(suite==='providers'){
 for(const provider of ['firecrawl','tavily','exa'])await cli('read-'+provider,['read','https://example.com','--provider',provider,'--budget-usd','0.10','--json']);
 await cli('search-firecrawl',['search','SQLite WAL single writer official documentation','--provider','firecrawl','--limit','2','--budget-usd','0.10','--json']);
 await cli('search-grok',['search','Does SQLite WAL allow multiple writers at the same instant? Find the official SQLite documentation.','--provider','grok','--budget-usd','0.25','--json']);
 for(const [name,input]of [['exa.find_similar',{url:'https://www.sqlite.org/wal.html',numResults:2}],['tavily.map',{url:'https://example.com',limit:1,max_depth:1}],['firecrawl.map',{url:'https://example.com',limit:1}],['firecrawl.crawl',{url:'https://example.com',limit:1,maxDiscoveryDepth:0}]]){
  const file=path.join(output,name+'-input.json');await fs.writeFile(file,JSON.stringify(input),{mode:0o600});await cli(name,['call',name,'--input',file,'--budget-usd',name.endsWith('crawl')?'0.55':'0.10','--json']);
 }
}else if(suite==='research'){
 await cli('pipeline',['research','SQLite WAL simultaneous readers single writer official documentation','--mode','pipeline','--providers','exa,tavily','--limit','2','--budget-usd','0.50','--json']);
 await cli('agent-wal',['answer','Research whether SQLite WAL permits more than one writer at the same instant. Search with exa, read one official sqlite.org source, then answer in Chinese with source citations. Do not use Grok native search for this specific comparison. Keep the final answer concise.','--budget-usd','1.20','--deadline','180s','--json']);
 await cli('agent-multi',['answer','SQLite WAL 与 PostgreSQL 在多个事务同时写入时有什么区别？请用至少两家不同的搜索服务交叉查找，再阅读官方文档，给出简短中文结论和引用。自行决定查询词、阅读页面以及何时证据足够。','--budget-usd','1.20','--deadline','180s','--json']);
}else if(suite==='isolation'){
 check(process.env.SOSEEK_OTHER_TOKEN_FILE,'SOSEEK_OTHER_TOKEN_FILE required');
 const body={operation:'search',query:'SQLite WAL simultaneous readers single writer official documentation',provider:'exa',limit:2,budget_usd:'0.10'};
 const key='acceptance-'+randomUUID();const first=await api('/v1/runs',{method:'POST',body,headers:{'Idempotency-Key':key}});check(first.status===202,'submit failed');const id=first.data.run_id;check(id,'missing run ID');
 const duplicate=await api('/v1/runs',{method:'POST',body,headers:{'Idempotency-Key':key}});check(duplicate.data.run_id===id,'duplicate created new run');
 const conflict=await api('/v1/runs',{method:'POST',body:{...body,query:'different'},headers:{'Idempotency-Key':key}});check(conflict.status===409,'changed body must conflict');
 await cli('owner-wait',['runs','wait',id,'--json']);
 const checks=[];
 for(const route of ['/v1/runs/'+id,'/v1/runs/'+id+'/result','/v1/runs/'+id+'/events','/v1/artifacts/'+id]){const r=await api(route,{tokenFile:process.env.SOSEEK_OTHER_TOKEN_FILE});checks.push({route,status:r.status});check(r.status===404,'cross-user access leaked: '+route)}
 const invalid=await api('/v1/capabilities',{tokenFile:null});check(invalid.status===401,'invalid token accepted');
 const ownArtifact=await api('/v1/artifacts/'+id);check(ownArtifact.status===200,'owner artifact unavailable');
 const events=await api('/v1/runs/'+id+'/events');check(events.status===200&&typeof events.data==='string'&&events.data.includes('event:'),'SSE failed');
 const ids=[...events.data.matchAll(/^id: (\d+)/gm)].map(m=>Number(m[1]));check(ids.length>0,'SSE IDs missing');
 const after=await api('/v1/runs/'+id+'/events',{headers:{'Last-Event-ID':String(ids[0])}});check([...after.data.matchAll(/^id: (\d+)/gm)].every(m=>Number(m[1])>ids[0]),'SSE replay cursor broken');
 const cancel=await api('/v1/runs',{method:'POST',body:{...body,query:'cancel acceptance '+randomUUID()},headers:{'Idempotency-Key':'cancel-'+randomUUID()}});check(cancel.status===202,'cancel submission failed');await api('/v1/runs/'+cancel.data.run_id+'/cancel',{method:'POST',body:{}});const cancelled=await cli('cancelled',['runs','wait',cancel.data.run_id,'--json']);check(cancelled.status==='cancelled','cancel did not reach cancelled state');
 await save('isolation',{idempotency:true,changed_body:conflict.status,invalid_token:invalid.status,cross_user:checks,owner_artifact:ownArtifact.status,sse_events:ids.length,sse_resume:true,cancellation:true});console.log('isolation, idempotency, SSE, artifact and cancellation passed');
}else throw Error('Unknown suite');
