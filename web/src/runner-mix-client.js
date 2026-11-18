const integer = n => Number.isSafeInteger(n) && n >= 0;
export function mixHost(host) {
  if (typeof host !== 'string' || !host || new TextEncoder().encode(host).length > 256 || host.trim() !== host || /[\p{Cc}/]/u.test(host)) throw new Error('Unsupported host identity.');
  return host;
}
export function validateMix(m, allowEmpty = false) {
  mixHost(m?.host);
  if (!integer(m.revision) || m.revision >= 9007199254740990 || !Array.isArray(m.entries) || m.entries.length > 32 || (!allowEmpty && !m.entries.length)) throw new Error('Review 1–32 sizes and a valid saved revision.');
  const names = new Set(), totals = [0,0,0,0];
  const entries = m.entries.map(e => {
    if (!e || !/^[a-z0-9-]{1,64}$/.test(e.name) || names.has(e.name) || !integer(e.cpu_millis) || !e.cpu_millis || e.cpu_millis % 1000 || !integer(e.ram_mib) || !e.ram_mib || !integer(e.disk_mib) || !e.disk_mib || !integer(e.count) || e.count > 1000000) throw new Error('Each unique size needs whole vCPUs, positive RAM/disk MiB and a count from 0 to 1,000,000.');
    names.add(e.name);
    [e.cpu_millis,e.ram_mib,e.disk_mib,1].forEach((v,i) => { totals[i] += v * e.count; if (!Number.isSafeInteger(totals[i])) throw new Error('Desired totals exceed safe display limits.'); });
    return {name:e.name,cpu_millis:e.cpu_millis,ram_mib:e.ram_mib,disk_mib:e.disk_mib,count:e.count};
  });
  return {host:m.host,revision:m.revision,entries};
}
export function validateMixView(v, host) {
  if (!integer(v?.sequence) || typeof v.profiles_match !== 'boolean' || v.mix?.host !== host) throw new Error('Saved mix response does not match this host.');
  const mix = validateMix(v.mix, v.mix.revision === 0);
  if (mix.revision === 0 && (mix.entries.length || v.profiles_match)) throw new Error('Invalid unsaved mix.');
  return {sequence:v.sequence,mix,profiles_match:v.profiles_match};
}
export async function saveRunnerMix(token, view, {fetcher=globalThis.fetch,signal}={}) {
  if (!/^[a-f0-9]{64}$/.test(token)) throw new Error('Enter the management token.');
  const mix = validateMix(view.mix);
  if (!integer(view.sequence)) throw new Error('Reload the saved mix first.');
  const uncertain = 'Save outcome uncertain. Reload the saved mix before another explicit review; do not automatically retry.';
  let response, result;
  try {
    response = await fetcher('/admin/api/v1/runner-mix',{method:'POST',headers:{Authorization:`Bearer ${token}`,'Content-Type':'application/json',Accept:'application/json'},body:JSON.stringify({expected_sequence:view.sequence,mix}),credentials:'omit',redirect:'error',cache:'no-store',signal});
    if (response.status === 409) throw new Error('Saved configuration changed. Reload and review again.');
    if (response.status !== 200) throw new Error(uncertain);
    result = await response.json();
  } catch(e) { if(e.message?.startsWith('Saved configuration changed')) throw e; throw new Error(uncertain); }
  if (signal?.aborted || !result || typeof result !== 'object' || Array.isArray(result) || result.host !== mix.host || result.revision !== mix.revision+1 || result.state !== 'saved_profiles_and_desired_mix_not_mapped') throw new Error(uncertain);
  return result;
}
