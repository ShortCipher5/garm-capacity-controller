// Presentation only. Never compare private specs or turn observations into a
// write precondition, deletion proof, automatic merge or retry authorization.
export function garmConflictDetails(previous, latest) {
  if (!previous?.settings_available || !latest?.settings_available)
    return { unavailable: true, changes: [], truncated: false };
  const changes = [];
  let truncated = false;
  const add = (change) => { if (changes.length < 50) changes.push(change); else truncated = true; };
  const show = value => {
    const text = value === undefined ? 'Not reported' : Array.isArray(value) ? value.join(', ') : String(value);
    return text.length > 512 ? text.slice(0, 512) + '… (truncated)' : text;
  };
  for (const [kind, collection, fields] of [
    ['Pool', 'pools', ['provider_name', 'enabled', 'max_runners', 'min_idle_runners', 'priority', 'generation', 'tags']],
    ['Repository', 'repositories', ['owner', 'name', 'pool_balancing_type']],
  ]) {
    const before = new Map(previous.settings[collection].map(value => [value.id, value]));
    const after = new Map(latest.settings[collection].map(value => [value.id, value]));
    for (const id of [...new Set([...before.keys(), ...after.keys()])].sort()) {
      const a = before.get(id), b = after.get(id);
      if (!a || !b) { add({kind, id, field: 'Observation', before: a ? 'Observed' : 'Not observed', after: b ? 'Observed' : 'Not observed'}); continue; }
      let visible = false;
      for (const field of fields) {
        const normalize = value => field === 'tags' && Array.isArray(value) ? [...value].sort() : value;
        if (JSON.stringify(normalize(a[field])) !== JSON.stringify(normalize(b[field]))) {
          visible = true; add({kind, id, field, before: show(a[field]), after: show(b[field])});
        }
      }
      if (!visible && a.fingerprint !== b.fingerprint)
        add({kind, id, field: 'Protected settings', before: 'Earlier observation', after: 'Changed; details not displayed'});
    }
  }
  for (const field of ['writes_enabled', 'write_blocked']) {
    if (previous[field] !== latest[field]) add({kind:'Management', id:'', field, before:show(previous[field]), after:show(latest[field])});
  }
  if (previous.settings.writes_enabled !== latest.settings.writes_enabled)
    add({kind:'Management', id:'', field:'remote writes_enabled', before:show(previous.settings.writes_enabled), after:show(latest.settings.writes_enabled)});
  return { unavailable: false, changes, truncated };
}
