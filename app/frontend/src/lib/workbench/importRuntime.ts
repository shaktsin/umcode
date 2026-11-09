export interface RuntimeStatus {ready:boolean;task:string;reason:string;labels?:string[];checkpoint?:string}
export interface ImportJob {id:string;status:string;epoch:number;epochs:number;trainLoss?:number;validationLoss?:number;result?:Record<string,unknown>;error?:string}
export type RuntimeCall=(method:string,params:Record<string,unknown>,timeout?:number)=>Promise<unknown>;
export interface ModelAsset {name:string;file:File}
export async function readRuntimeSnapshot(call:(action:string)=>Promise<unknown>) {
 const job=await call('job') as ImportJob|null;
 const status=await call('status') as RuntimeStatus;
 return {job,status};
}
export function modelAssets(files:File[]):ModelAsset[] {
 const first=files[0]?.webkitRelativePath?.split('/')[0];
 const strip=!!first && files.every(f=>f.webkitRelativePath?.startsWith(first+'/'));
 const result:ModelAsset[]=[],seen=new Set<string>();
 for(const file of files){
  const original=file.webkitRelativePath||file.name;
  const name=strip?original.slice(first!.length+1):original;
  if (name.split('/').some(p=>!p||p==='.'||p==='..')||/[\\\x00-\x1f:]/.test(name))throw Error('Invalid model folder path.');
  if(name.includes('/'))continue;
  if(!/\.safetensors(?:\.index\.json)?$/.test(name)&&!['config.json','generation_config.json','tokenizer.json','tokenizer_config.json','special_tokens_map.json','added_tokens.json','vocab.json','vocab.txt','merges.txt','tokenizer.model','sentencepiece.bpe.model','preprocessor_config.json','processor_config.json','feature_extractor_config.json'].includes(name))continue;
  if(seen.has(name))throw Error('Duplicate model file: '+name);seen.add(name);
  if(!Number.isSafeInteger(file.size)||file.size<=0)throw Error('Empty or invalid model file: '+name);
  result.push({name,file});
 }
 if(result.length>160||result.reduce((n,a)=>n+a.file.size,0)>8*1024**3)throw Error('Model folder exceeds 8 GiB or 160 supported files.');
 return result;
}
export function importAvailability(family:string,names:string[]){
 if(!names.includes('config.json'))throw Error('Missing config.json. Select it together with the safetensors weights.');
 if(!names.some(n=>n.endsWith('.safetensors')))throw Error('Missing safetensors weights. Select them together with config.json.');
 const image=['resnet','convnext','vit'].includes(family);
 const text=['gpt2','qwen2','llama','mistral','bert'].includes(family);
 const missing:string[]=[];
 if(image && !names.includes('preprocessor_config.json'))missing.push('preprocessor_config.json (image resizing and normalization)');
 if(text){
  if(!names.includes('tokenizer_config.json'))missing.push('tokenizer_config.json');
  if(!names.includes('tokenizer.json')&&!names.includes('tokenizer.model')&&!names.includes('sentencepiece.bpe.model')&&!names.includes('vocab.txt')&&!(names.includes('vocab.json')&&names.includes('merges.txt')))missing.push('tokenizer vocabulary files');
 }
 if(!image&&!text)missing.push('a supported Transformer or CNN runtime');
 return {complete:missing.length===0,missing,image};
}
function encode(bytes:Uint8Array){let s='';for(let i=0;i<bytes.length;i+=8192)s+=String.fromCharCode(...bytes.subarray(i,i+8192));return btoa(s);}
export async function uploadModelBundle(call:RuntimeCall,context:Record<string,unknown>,assets:ModelAsset[],current:()=>boolean,progress:(done:number,total:number)=>void){
 const api=(action:string,params:Record<string,unknown>={})=>call('workbench/import/runtime',{...context,action,...params},300000);
 const total=assets.reduce((n,a)=>n+a.file.size,0);let done=0,uploadId='';
 try{
  if(!current())throw Error('Model upload cancelled.');
  const result=await api('begin',{files:assets.map(a=>({name:a.name,size:a.file.size}))}) as {uploadId:string};
  uploadId=result.uploadId;
  for(const {name,file} of assets)for(let offset=0;offset<file.size;offset+=1048576){
   if(!current())throw Error('Model upload cancelled.');
   const bytes=new Uint8Array(await file.slice(offset,offset+1048576).arrayBuffer());
   await api('chunk',{uploadId,name,offset,contentBase64:encode(bytes)});
   if(!current())throw Error('Model upload cancelled.');
   done+=bytes.length;progress(done,total);
  }
  if(!current())throw Error('Model upload cancelled.');
  await api('finish',{uploadId});uploadId='';
 }catch(e){if(uploadId)await api('abort',{uploadId}).catch(()=>{});throw e;}
}
