const id = v => typeof v === 'string' && v.length > 0 && v.length <= 128;
const integer = v => Number.isSafeInteger(v) && v >= 0;
export const validMappingSize = r => r && ['cpu_millis','ram_mib','disk_mib'].every(k=>integer(r[k])&&r[k]>0) && r.cpu_millis%1000===0 && r.runners===1;
export function validatePoolMappings(v) {
 if (!v || v.configuration_only!==true || typeof v.writes_enabled!=='boolean' || !Array.isArray(v.recipes) || v.recipes.length>1000 || !Array.isArray(v.profiles) || v.profiles.length>1000 || !v.recipes.every(r=>id(r.id)&&id(r.pool)&&/^[a-f0-9]{64}$/.test(r.revision)) || new Set(v.recipes.map(r=>r.id)).size!==v.recipes.length || !v.profiles.every(p=>id(p.pool)&&id(p.repository)&&id(p.size_profile)&&integer(p.sequence)&&p.sequence>0&&validMappingSize(p.resources)&&Array.isArray(p.hosts)&&p.hosts.every(id)) || new Set(v.profiles.map(p=>p.pool)).size!==v.profiles.length) throw new Error('Unsupported pool mapping response.');
 return v;
}
export async function registerPoolMapping(token, input, {signal,fetcher=globalThis.fetch}={}) {
 if (!/^[a-f0-9]{64}$/.test(token) || !id(input.recipe)||!id(input.pool)||!/^[a-f0-9]{64}$/.test(input.recipe_revision)||!id(input.size_profile)||!integer(input.expected_sequence)||!validMappingSize(input.resources)) throw new Error('Review the mapping and enter the separate management token.');
 let response;
 try { response=await fetcher('/admin/api/v1/broker-profiles',{method:'POST',headers:{Authorization:`Bearer ${token}`,'Content-Type':'application/json'},body:JSON.stringify({recipe:input.recipe,recipe_revision:input.recipe_revision,size_profile:input.size_profile,expected_sequence:input.expected_sequence,resources:input.resources}),credentials:'omit',cache:'no-store',redirect:'error',signal}); } catch { throw new Error('Registration outcome unknown. Reload mappings before another action. No retry was made.'); }
 if (!response.ok) throw new Error('Registration not confirmed. Reload and review ownership, size and configured native policy.');
 let v;try {v=await response.json();}catch {throw new Error('Registration confirmation unavailable. Reload mappings.');}
 if(v.saved!==true||v.configuration_only!==true||!v.profile||!integer(v.profile.sequence)||v.profile.sequence<=input.expected_sequence||v.profile.pool!==input.pool||v.profile.size_profile!==input.size_profile||!validMappingSize(v.profile.resources)||['cpu_millis','ram_mib','disk_mib','runners'].some(k=>v.profile.resources[k]!==input.resources[k]))throw new Error('Registration confirmation unavailable. Reload mappings.');
 return v.profile;
}
