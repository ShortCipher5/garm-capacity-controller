// Compare remote settings, not history timestamps. Sorting avoids false drift
// when Garm changes collection order. This never authorizes a remote write.
export function garmSettingsChanged(previous, next) {
  const identity = (value) => JSON.stringify([
    value.settings_available, value.writes_enabled, value.write_blocked,
    value.settings.writes_enabled,
    [...value.settings.pools].map(p => [p.id, p.fingerprint]).sort(),
    [...value.settings.repositories].map(r => [r.id, r.fingerprint]).sort(),
  ]);
  return !previous || identity(previous) !== identity(next);
}
