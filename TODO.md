# graab — TODO

## Inbox

- [ ] **#15** **URGENT** Remove and re-add the WhatsApp connector in claude.ai
      - Fly v7 (2026-09-16) runs with sending on and GRAAB_ALLOWED_RECIPIENTS
        limited to Richard's own number, but the connector caches its tool
        list, so `send_poll` / `vote_in_poll` do not appear until it is re-added.
      - Settings → Connectors → remove WhatsApp → add it again with the same URL.

- [ ] **#2** Send a text, a file from `store/outbox`, and a voice note against the real account
      - Exercised only against mocks so far. Needs #1 (Fly ran read-only until
        GRAAB_READ_ONLY was flipped on 2026-09-15) and the recipient must be
        Richard's own number while GRAAB_ALLOWED_RECIPIENTS is set.

- [ ] **#3** Exercise polls and events against the real account
      - Added 2026-09-14, exercised only against mocks and an offline whatsmeow
        client.
      - Restart the bridge so the new tables exist, check `list_polls` /
        `list_events` pick up polls and events from group history with votes and
        RSVPs, then vote in a poll and RSVP to an event and confirm the phone
        shows them.
      - Event creation and RSVP sending use hand-built protobufs (whatsmeow has
        helpers only for polls), so those two are the least certain.

- [ ] **#4** Full-text search (SQLite FTS5) for `list_messages`
      - The current `LIKE` scan is fine to ~50k messages.

- [ ] **#5** Reactions and read receipts
      - Currently dropped by the bridge.

- [ ] **#6** Bump whatsmeow periodically
      - CI catches API breakage.

## In Progress

## Done

- [x] **#1** **Done 2026-09-16.** Finish the GitHub → Fly deploy setup
      - FLY_API_TOKEN secret set, `.github/workflows/deploy.yml` deployed v7 on
        the first push to main. Bridge log confirms allowed recipients
        [18134953896] and read-only off; bridge_status agrees.

- [x] **#7** **Done 2026-09-14.** Switch the GitHub default branch to `main` and delete
      `claude/hn-item-43532967-sc82js`
      - Default is `main`; the branch was deleted with `git push origin --delete`
        on 2026-09-14.

- [x] **#8** **Done 2026-09-14.** Upload the new `new-app` skill to the Claude account
      - It exists locally at `~/.claude/skills/new-app` and in dotfiles (pushed
        2026-09-14), but cloud routines, Cowork and the desktop app load skills
        from the claude.ai account, and no browser was connected to do the upload.
      - Zip ready at `/private/tmp/claude-501/-Users-richard-code-spaan/3397c42f-8448-48ba-968e-b0aa16f6a878/scratchpad/skill-uploads/new-app.zip`
        (or re-zip: `cd ~/.claude/skills && zip -r new-app.zip new-app -x '*.DS_Store'`).
      - Steps: claude.ai → Customize → Skills → **Add** (it has never been
        uploaded) → pick the zip → confirm the page shows `new-app` under Yours.
      - Or run `/sync-skills` from a session with Claude in Chrome connected.

- [x] **#9** **Done 2026-09-14.** Polls and events: options, votes, dates, locations and RSVPs,
      plus creating and answering them

- [x] **#10** **Done 2026-09-12.** Download an incoming image
      - Verified on Fly v5: `download_media` returned a group flyer inline as an
        image block via the bridge's `/api/media` endpoint.

- [x] **#11** **Done 2026-09-07.** Confirm the claude.ai custom-connector flow end to end against
      the Fly deployment (dynamic registration + login page)
      - Verified including secret rotation invalidating the old session.

- [x] **#12** **Done 2026-09-07.** OAuth 2.1 for the HTTP transport so claude.ai connectors and
      cloud Claude Code can use a public deployment (`GRAAB_MCP_PUBLIC_URL`)

- [x] **#13** **Done 2026-09-07.** Linked-device name
      - Verified on iOS: `-device-platform desktop` (the default) shows the bare
        name, "Local MCP Bridge". `unknown` shows "Other device" and hides the
        name. Keep `desktop` as the default.

- [x] **#14** **Done 2026-09-07.** Confirm history sync populates contact names from the address
      book (app-state sync) and that group senders resolve to names, not numbers
      - Verified: group participant names resolve.

## Someday/Maybe
