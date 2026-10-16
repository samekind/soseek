import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import {spawn} from 'node:child_process';
import {fileURLToPath} from 'node:url';
import {rateLimitSignals,metrics,summarize,makeVariants} from '../scripts/evaluate.mjs';

const root=path.resolve(path.dirname(fileURLToPath(import.meta.url)),'..');
test('rate limiting recognizes real API variants and retry-after signals',()=>{
 for(const error of [{code:'rate_limited'},{code:'upstream_rate_limited'},{code:'http_429'},{code:'http429'},{status_code:429},{retry_after_seconds:37}]){
  assert.equal(rateLimitSignals({attempts:[{error}]}).length,1,JSON.stringify(error));
 }
 assert.equal(rateLimitSignals({error:{code:'upstream_timeout',retry_after_seconds:0}}).length,0);
 assert.equal(rateLimitSignals({results:[{snippet:'HTTP 429 rate_limited Retry-After documentation'}]}).length,0);
});
test('cost absence stays distinct from known zero and failed cases count in denominator',()=>{
 const q={id:'q',category:'technical',expected_domains:['example.org']};
 const failed=metrics(q,'exa',{exit:4,ms:42,result:{error:{code:'upstream_timeout'}}});
 assert.equal(failed.reported_micros,null);assert.equal(failed.expected_domain_present,false);
 const good=metrics(q,'exa',{exit:0,ms:12,result:{status:'succeeded',results:[{url:'https://docs.example.org/a'}],usage:{reported_micros:0,estimated_micros:0,reserved_micros:50000,unsettled_attempts:1},attempts:[{usage:{cost_known:false,reported_micros:0,estimated_micros:0}}]}});
 assert.equal(good.reported_micros,0);assert.equal(good.unknown_charge_attempts,1);
 const summary=summarize([failed,good])[0];assert.equal(summary.cases,2);assert.equal(summary.failed,1);assert.equal(summary.expected_domain_hits,1);assert.equal(summary.expected_domain_cases,2);assert.equal(summary.reserved_usd,.05);assert.equal(summary.cost_fields_missing,1);
});
test('live runner with mock CLI saves 429 then stops before the next question',async()=>{
 const directory=await fs.mkdtemp(path.join(os.tmpdir(),'soseek-eval-test-'));
 try{
  const binary=path.join(directory,'fake-soseek'),out=path.join(directory,'results'),calls=path.join(directory,'calls');
  const mock=`#!/usr/bin/env node\nconst fs=require('node:fs');const a=process.argv.slice(2);fs.appendFileSync(${JSON.stringify(calls)},JSON.stringify(a)+'\\n');if(a[0]==='usage'){console.log(JSON.stringify({reserved_micros:0,reported_micros:0}));process.exit(0)}if(a[a.indexOf('--budget-usd')+1]!=='0.20')process.exit(2);if(a.includes('--providers')){console.log(JSON.stringify({run_id:'run4',status:'partial',results:[{url:'https://go.dev/'}],attempts:[{error:{code:'upstream_rate_limited',retry_after_seconds:90}}]}));process.exit(0)}console.log(JSON.stringify({run_id:'run1',status:'succeeded',results:[{url:'https://go.dev/'}],usage:{reported_micros:7000,reserved_micros:0,estimated_micros:0,unsettled_attempts:0},attempts:[]}));\n`;
  await fs.writeFile(binary,mock,{mode:0o700});
  const child=spawn(process.execPath,[path.join(root,'scripts/evaluate.mjs'),'--live','--binary',binary,'--out',out,'--limit','2'],{stdio:['ignore','pipe','pipe']});
  let stderr='';child.stderr.on('data',x=>stderr+=x);child.stdout.resume();const code=await new Promise(resolve=>child.on('close',resolve));assert.equal(code,1,stderr);
  const recorded=(await fs.readFile(calls,'utf8')).trim().split('\n').map(JSON.parse);
  assert.equal(recorded.filter(x=>x[0]==='search').length,3);assert.equal(recorded.filter(x=>x[0]==='usage').length,1);
  const report=JSON.parse(await fs.readFile(path.join(out,'stop-report.json'),'utf8'));assert.equal(report.reason,'rate_limit_observed');assert.equal(report.automatic_retry,false);assert.equal(report.finished_runs,3);
  const summary=JSON.parse(await fs.readFile(path.join(out,'summary.json'),'utf8'));assert.equal(summary.completed,false);assert.equal(summary.finished_runs,3);
  const raw=JSON.parse(await fs.readFile(path.join(out,'tech-01-merged.json'),'utf8'));assert.equal(raw.result.attempts[0].error.retry_after_seconds,90);
  if(process.platform!=='win32')for(const entry of await fs.readdir(out)){const stat=await fs.stat(path.join(out,entry));assert.equal(stat.mode&0o777,0o600,entry);}
 }finally{await fs.rm(directory,{recursive:true,force:true});}
});

test('provider list determines variants without touching excluded providers',()=>{
 assert.deepEqual(makeVariants(['exa','tavily']).map(v=>v[0]),['exa','tavily','merged']);
 assert.deepEqual(makeVariants(['exa','tavily'])[2][1],['--providers','exa,tavily']);
 assert.throws(()=>makeVariants(['exa','exa']));
});
