# To do

Open items, roughly in priority order. Tick them off in commits.

## Verify against the real account

- [ ] **Linked-device name.** Pairing with the default (`unknown`) platform shows
      "Other device". `-device-platform desktop` (now the default) should show the
      name on its own; `chrome` shows "Chrome (name)". Needs a re-pair to confirm
      which rendering WhatsApp actually uses. Pick the default accordingly.
- [ ] Confirm history sync populates contact names from the address book
      (app-state sync) and that group senders resolve to names, not numbers.
- [ ] Send a text, a file from `store/outbox`, and a voice note; download an
      incoming image. All exercised only against mocks so far.

## Features

- [ ] Full-text search (SQLite FTS5) for `list_messages`; current `LIKE` scan
      is fine to ~50k messages.
- [ ] Group participant lists from group metadata, for names of people not in
      the address book.
- [ ] Reactions and read receipts (currently dropped).
- [ ] OAuth for the HTTP transport so Claude Desktop custom connectors can use
      a public deployment (Claude Code works with the bearer token today).

## Housekeeping

- [ ] Switch the GitHub default branch to `main` and delete
      `claude/hn-item-43532967-sc82js` (GitHub refuses to delete the default).
- [ ] Bump whatsmeow periodically; CI catches API breakage.
