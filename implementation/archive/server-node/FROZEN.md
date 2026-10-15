# FROZEN — legacy Node implementation (do not deploy)

This directory is the **original Node.js reference implementation** of the EduDesk
bundle server. It is kept only as a historical cross-check against the Go rewrite in
`../../server-go/`.

**Do not deploy, run, or patch this code.**

Why it is quarantined rather than deleted (patch plan P0-6):

- two live implementations of the same protocol drift apart silently — the terminal
  would be verifying bundles produced by whichever server happened to be started;
- the Go server has since gained behaviour this one does not have (bundle_id / ttl_sec /
  content_hash, revocation-aware auth, `--dev` secret gate), so a bundle from here is
  **not** protocol-equivalent and will be rejected or mis-rendered by the terminal;
- deleting outright loses the ability to diff the two canonicalisation schemes, which
  was the hardest bug in the port.

If you need the behaviour here, port it into `../../server-go/` and delete this tree.
