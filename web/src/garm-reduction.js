// Restricted managed mode is not a general settings-edit capability.
export function maximumReductionOnly(snapshot) { return snapshot?.maximum_reduction_only === true; }
export function maximumReduction(selected, value) {
  if(!selected || typeof value!=='string' || !/^[0-9]+$/.test(value))throw new Error('Enter a positive maximum below the current maximum and at least the current idle minimum.');
  const maximum=Number(value);
  if(!Number.isSafeInteger(maximum)||maximum<=0||maximum>1000000||maximum>=selected.max_runners||maximum<selected.min_idle_runners)throw new Error('Enter a positive maximum below the current maximum and at least the current idle minimum.');
  return {max_runners:maximum};
}
export function reviewedReductionAllowed(snapshot, review) {
  if(!maximumReductionOnly(snapshot))return true;
  if(review?.kind!=='pool'||!review.change?.pool||Object.keys(review.change.pool).length!==1||!Object.hasOwn(review.change.pool,'max_runners'))return false;
  const pool=snapshot.settings.pools.find(p=>p.id===review.change.target);
  try{return maximumReduction(pool,String(review.change.pool.max_runners)).max_runners===review.change.pool.max_runners;}catch{return false;}
}
