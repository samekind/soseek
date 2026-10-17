// Real CLI -> cloud evaluation. This incurs upstream usage. No automatic retries.
import fs from 'node:fs/promises';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {spawn} from 'node:child_process';

const root=path.resolve(path.dirname(fileURLToPath(import.meta.url)),'..');
export function makeVariants(providers){
 if(!providers.length||providers.some(p=>!/^[-a-z0-9_]+$/.test(p))||new Set(providers).size!==providers.length)throw Error('--providers must contain unique provider names');
 const result=providers.map(p=>[p,['--provider',p]]);
 if(providers.length>1)result.push(['merged',['--providers',providers.join(',')]]);
 return result;
}
export const variants=makeVariants(['exa','tavily']);

// Look at structured error/status fields only. Search text mentioning 429 is
// evidence, not a reason to call a provider again or terminate the experiment.
export function rateLimitSignals(document){
 const found=[];
 function walk(node,location='$'){
  if(!node||typeof node!=='object')return;
  if(Array.isArray(node)){node.forEach((v,i)=>walk(v,`${location}[${i}]`));return;}
  const code=typeof node.code==='string'?node.code.toLowerCase():'';
  const retry=Number(node.retry_after_seconds);
  const status=node.status_code??node.http_status??node.http_status_code??node.status;
  if(/(^|_)rate_?limited$/.test(code)||/^http_?429$/.test(code)||code==='429'||status===429||status==='429'||Number.isFinite(retry)&&retry>0){
   found.push({location,code:node.code??null,status:status??null,retry_after_seconds:Number.isFinite(retry)?retry:null,message:typeof node.message==='string'?node.message:null});
  }
  for(const [k,v]of Object.entries(node))if(k!=='raw'&&k!=='results')walk(v,`${location}.${k}`);
 }
 walk(document);return found;
}
const numeric=value=>typeof value==='number'&&Number.isFinite(value)?value:null;
export function metrics(q,variant,r){
 const d=r.result??{},sources=Array.isArray(d.results)?d.results:[],attempts=Array.isArray(d.attempts)?d.attempts:[];
 const urls=[...new Set(sources.map(x=>x.canonical_url||x.url).filter(Boolean))];
 const expected=q.expected_domains??[];
 const official=expected.length?urls.some(u=>{try{const host=new URL(u).hostname;return expected.some(e=>host===e||host.endsWith('.'+e));}catch{return false}}):null;
 const errorCodes=[d.error?.code,...attempts.map(a=>a.error?.code)].filter(Boolean);
 return {
  question_id:q.id,category:q.category,variant,status:d.status||'failed',exit:r.exit,elapsed_ms:Math.round(r.ms),run_id:d.run_id??null,
  result_count:sources.length,unique_urls:urls.length,expected_domain_present:official,
  reported_micros:numeric(d.usage?.reported_micros),estimated_micros:numeric(d.usage?.estimated_micros),reserved_micros:numeric(d.usage?.reserved_micros),
  unsettled_attempts:numeric(d.usage?.unsettled_attempts),credits:numeric(d.usage?.credits),
  attempted_calls:attempts.length,cached_attempts:attempts.filter(x=>x.cached).length,
  unknown_charge_attempts:attempts.filter(a=>!a.cached&&!a.usage?.cost_known&&!(a.usage?.reported_micros>0)&&!(a.usage?.estimated_micros>0)).length,
  error_codes:[...new Set(errorCodes)],warnings:d.warnings??[],stop_reason:d.stop_reason??null
 };
}
const quantile=(values,p)=>{if(!values.length)return null;const a=[...values].sort((x,y)=>x-y);return a[Math.max(0,Math.ceil(a.length*p)-1)];};
export function summarize(rows,selectedVariants=variants){
 const sum=(rr,key)=>rr.reduce((s,x)=>s+(numeric(x[key])??0),0);
 return selectedVariants.map(([name])=>{
  const rr=rows.filter(x=>x.variant===name),known=rr.filter(x=>x.expected_domain_present!==null),errors={};
  for(const row of rr)for(const code of row.error_codes??[])errors[code]=(errors[code]??0)+1;
  return {
   variant:name,cases:rr.length,succeeded:rr.filter(x=>x.status==='succeeded').length,partial:rr.filter(x=>x.status==='partial').length,failed:rr.filter(x=>x.status==='failed').length,cancelled:rr.filter(x=>x.status==='cancelled').length,
   expected_domain_hits:known.filter(x=>x.expected_domain_present).length,expected_domain_cases:known.length,
   p50_ms:quantile(rr.map(x=>x.elapsed_ms),.5),p95_ms:quantile(rr.map(x=>x.elapsed_ms),.95),
   reported_usd:sum(rr,'reported_micros')/1e6,estimated_usd:sum(rr,'estimated_micros')/1e6,reserved_usd:sum(rr,'reserved_micros')/1e6,
   cost_fields_missing:rr.filter(x=>x.reported_micros===null).length,unsettled_attempts:sum(rr,'unsettled_attempts'),unknown_charge_attempts:sum(rr,'unknown_charge_attempts'),
   credits:sum(rr,'credits'),attempted_calls:sum(rr,'attempted_calls'),cached_attempts:sum(rr,'cached_attempts'),error_codes:errors
  };
 });
}
async function save(file,value){
 const temporary=file+'.tmp';const handle=await fs.open(temporary,'w',0o600);
 try{await handle.chmod(0o600);await handle.writeFile(JSON.stringify(value,null,2)+'\n');await handle.sync();}finally{await handle.close();}
 await fs.rename(temporary,file);
}
export function runCLI(binary,argv){
 return new Promise(resolve=>{
  const start=performance.now();const child=spawn(binary,argv,{env:process.env,stdio:['ignore','pipe','pipe']});let stdout='',stderr='',settled=false;
  const timer=setTimeout(()=>child.kill('SIGTERM'),100000);
  const finish=value=>{if(settled)return;settled=true;clearTimeout(timer);resolve(value);};
  child.stdout.on('data',x=>stdout+=x);child.stderr.on('data',x=>stderr+=x);
  child.on('error',e=>finish({exit:-1,result:{error:{code:'cli_spawn_failed',message:e.message}},stdout,stderr,ms:performance.now()-start}));
  child.on('close',(exit,signal)=>{
   let result;try{result=JSON.parse(stdout)}catch{result={error:{code:'invalid_json',message:'CLI did not return one JSON object'}};}
   finish({exit:exit??-1,signal:signal??null,result,stdout,stderr,ms:performance.now()-start});
  });
 });
}

export async function main(args=process.argv.slice(2)){
 if(!args.includes('--live'))throw Error('Pass --live to run the paid cloud evaluation. Set SOSEEK_URL and SOSEEK_TOKEN_FILE.');
 const value=(name,fallback)=>{const i=args.indexOf(name);if(i<0)return fallback;if(!args[i+1]||args[i+1].startsWith('--'))throw Error(`${name} requires a value`);return args[i+1];};
 const binary=value('--binary',path.join(root,'bin/soseek'));
 const output=path.resolve(value('--out',path.join(root,'output/evaluation-'+Date.now())));
 const limit=Number(value('--limit','24')),budget=value('--budget-usd','0.20');
 const selectedVariants=makeVariants(value('--providers','exa,tavily').split(',').map(p=>p.trim()));
 const exclusionNotes=args.flatMap((arg,i)=>arg==='--exclude-reason'?[args[i+1]]:[]);
 if(exclusionNotes.some(note=>!note||!note.includes('=')))throw Error('--exclude-reason requires provider=reason');
 const excludedProviders=exclusionNotes.map(note=>{const i=note.indexOf('=');return {provider:note.slice(0,i),reason:note.slice(i+1)};});
 if(!Number.isInteger(limit)||limit<1||limit>24)throw Error('--limit must be 1..24');
 if(!/^\d+(\.\d{1,6})?$/.test(budget)||Number(budget)<=0)throw Error('--budget-usd must be a positive decimal with at most six places');
 const cases=JSON.parse(await fs.readFile(path.join(root,'evals/questions.json'),'utf8')).slice(0,limit);
 await fs.mkdir(path.dirname(output),{recursive:true,mode:0o700});await fs.mkdir(output,{recursive:false,mode:0o700});
 const startedAt=new Date().toISOString(),rows=[],usageSnapshots=[];
 const method='Sequential '+selectedVariants.map(v=>v[0]).join(', ')+' search for each public question; five returned results. Merge can reuse preceding same-user provider cache. Expected-domain presence is a provenance heuristic, not factual accuracy or blind relevance. Latencies include CLI, HTTPS, server scheduling and cache; no provider speed or fusion superiority claim.';
 const manifest={started_at:startedAt,server:process.env.SOSEEK_URL??null,questions:cases.length,planned_runs:cases.length*selectedVariants.length,per_run_budget_usd:budget,deadline_seconds:60,wait_timeout_seconds:90,variants:selectedVariants.map(v=>v[0]),excluded_providers:excludedProviders,fresh:false,method};
 await save(path.join(output,'manifest.json'),manifest);
 async function snapshot(label){
  const r=await runCLI(binary,['usage','--json']);
  const record={label,captured_at:new Date().toISOString(),...r};usageSnapshots.push(record);await save(path.join(output,'usage.json'),usageSnapshots);
  console.log(JSON.stringify({type:'usage',label,...r.result}));
  return r;
 }
 async function conclude(stop=null){
  const report={started_at:startedAt,checked_at:new Date().toISOString(),completed:stop===null,planned_runs:cases.length*selectedVariants.length,finished_runs:rows.length,method,variants:selectedVariants.map(v=>v[0]),excluded_providers:excludedProviders,summary:summarize(rows,selectedVariants),stop};
  await save(path.join(output,'summary.json'),report);
  if(stop)await save(path.join(output,'stop-report.json'),{...stop,finished_runs:rows.length,automatic_retry:false,usage_snapshots:usageSnapshots.length});
  console.log(JSON.stringify({type:'summary',output,...report}));return report;
 }
 const initial=await snapshot('before');
 if(initial.exit!==0){await conclude({reason:'usage_preflight_failed',error:initial.result?.error??null});return 1;}
 for(const q of cases){
  for(const [variant,flags]of selectedVariants){
   // Evaluation counts every attempt and retains its errors/charges; compact
   // CLI evidence views intentionally omit some of that audit information.
   const r=await runCLI(binary,['search',q.query,...flags,'--limit','5','--budget-usd',budget,'--deadline','60s','--wait-timeout','90s','--full','--json']);
   const row=metrics(q,variant,r);rows.push(row);
   await save(path.join(output,q.id+'-'+variant+'.json'),{question:q,...r});await save(path.join(output,'metrics.json'),rows);
   console.log(JSON.stringify({type:'run',...row}));
   const signals=rateLimitSignals(r.result);
   if(signals.length){await conclude({reason:'rate_limit_observed',question_id:q.id,variant,signals,action:'Stopped immediately after saving the response. No retry or subsequent evaluation request was issued.'});return 1;}
   const errors=row.error_codes;
   if(errors.some(c=>/budget|quota_exhausted|quota_exceeded/.test(c))){await conclude({reason:'budget_or_quota_blocked',question_id:q.id,variant,error_codes:errors,action:'Stopped without changing any quota or retrying.'});return 1;}
   if(r.exit===-1||r.exit===5||errors.includes('invalid_json')){await conclude({reason:'client_result_uncertain',question_id:q.id,variant,error_codes:errors,action:'Stopped; preserve any run/pending ID before deciding on further work.'});return 1;}
  }
  const usage=await snapshot('after-'+q.id);
  if(usage.exit!==0){await conclude({reason:'usage_checkpoint_failed',question_id:q.id,error:usage.result?.error??null});return 1;}
 }
 await conclude();return 0;
}
if(process.argv[1]&&path.resolve(process.argv[1])===fileURLToPath(import.meta.url)){
 main().then(code=>{process.exitCode=code;}).catch(error=>{console.error(error.message);process.exitCode=1;});
}
