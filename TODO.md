# To do

Open items, roughly in priority order. Tick them off in commits.

## Verify against the real account

- [x] **Linked-device name.** Verified on iOS: `-device-platform desktop`
      (the default) shows the bare name, "Local MCP Bridge". `unknown` shows
      "Other device" and hides the name. Keep `desktop` as the default.
- [x] Confirm history sync populates contact names from the address book
      (app-state sync) and that group senders resolve to names, not numbers.
      Verified: group participant names resolve.
- [ ] Send a text, a file from `store/outbox`, and a voice note; download an
      incoming image. All exercised only against mocks so far.

## Features

- [ ] Full-text search (SQLite FTS5) for `list_messages`; current `LIKE` scan
      is fine to ~50k messages.
- [ ] Reactions and read receipts (currently dropped).
- [x] OAuth 2.1 for the HTTP transport so claude.ai connectors and cloud
      Claude Code can use a public deployment (`GRAAB_MCP_PUBLIC_URL`).
- [ ] Confirm the claude.ai custom-connector flow end to end against the Fly
      deployment (dynamic registration + login page).

## Housekeeping

- [ ] Switch the GitHub default branch to `main` and delete
      `claude/hn-item-43532967-sc82js` (GitHub refuses to delete the default).
- [ ] Bump whatsmeow periodically; CI catches API breakage.
