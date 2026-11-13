// Build-time PUBLIC source/assets only. Never handles generated credentials/state.
import fs from 'node:fs';import path from 'node:path';import crypto from 'node:crypto';
const root='/build/webapp', build=path.join(root,'build'), output='/out';
const inventory={};
function walk(base,prefix=''){for(const name of fs.readdirSync(base).sort()){const full=path.join(base,name),relative=prefix+name,info=fs.lstatSync(full);if(info.isSymbolicLink())throw Error('linked public asset');if(info.isDirectory())walk(full,relative+'/');else if(info.isFile())inventory[relative]=crypto.createHash('sha256').update(fs.readFileSync(full)).digest('hex');else throw Error('unsupported public asset');}}
walk(build);for(const name of ['assets.go','openapitools.json'])inventory[name]=crypto.createHash('sha256').update(fs.readFileSync(path.join(root,'assets',name))).digest('hex');
if(!inventory['index.html']||Object.keys(inventory).length<20)throw Error('incomplete upstream UI');
fs.mkdirSync(output,{recursive:true});fs.writeFileSync(path.join(output,'public-assets.json'),JSON.stringify(inventory));
let omitted=null;const variant=process.env.PROFILE_VARIANT;
if(variant==='missing-js'){omitted=Object.keys(inventory).sort().find(n=>n.endsWith('.js'));if(!omitted)throw Error('no JS control');fs.unlinkSync(path.join(build,omitted));}else if(variant!=='baseline')throw Error('unrecognized variant');
fs.writeFileSync(path.join(output,'omitted.json'),JSON.stringify({variant,omitted}));
