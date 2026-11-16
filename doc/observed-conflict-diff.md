# Observed Garm conflict details

Read-only browser presentation of earlier configured settings versus latest observed settings when an existing dirty-draft/review conflict blocks apply. Keeps the draft and all existing write gates unchanged. No automatic merge, rebase, retry, deletion/recreation or attribution. Displays only public settings fields; hidden fingerprint differences get a generic description, never the hash/private configuration. Limits output to 50 differences and 512 characters per value. Absent observations are not deletion proof; unavailable snapshots do not produce false removal differences.

Qualification: all 39 web node tests pass; production Vite build passes. Extended existing Playwright conflict test checks visible before/latest priority and disappearance after explicit discard, but execution is not qualified here: bundled Chromium unavailable; installed Chromium aborted before browser startup. No live infrastructure or production write used.
