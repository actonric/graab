# SF Events working docs — mirror

A read-only copy of the Google Drive folder **`My Drive / SF Events`**, the
working set behind the `sf-events-calendar` skill. **Google Drive remains the
source of truth.** Nothing here is edited by hand; a run that changes state
still writes a new delta doc in Drive, and this folder is re-pulled afterwards.

## Layout

```
data/index.json          one entry per Drive doc: id, title, timestamps, size,
                         sha256 + char count + tail of the mirrored text
data/docs/<driveId>.txt  live set  (the SF Events folder itself)
data/archive/<driveId>.txt   superseded versions (the archive subfolder)
```

Files are named by **Drive ID, never by title** — version numbers are not
unique in this corpus (five docs were once titled "v4") and two titles share
their first 80 characters, so a title-derived filename silently overwrote one
doc with another on 2026-09-12. Look titles up in `index.json`.

## How the copy was made

Each doc was fetched with `download_file_content(exportMimeType="text/plain")`
and base64-decoded — the byte-accurate path. `read_file_content` silently
truncates and mangles these docs; it was not used. Post-processing was limited
to stripping the UTF-8 BOM and converting CRLF to LF.

Every file's last 80 characters are recorded in `index.json` as `tail`. Note
that `Part 1 of N` masters legitimately end mid-sentence ("— but see",
"Goldstar, SF"): the split is at source-document boundaries and the earlier
manifests record those exact tails as correct.

## Checking for drift

`index.json` carries Drive's `modifiedTime` and the text's `sha256`. To see
whether Drive has moved on since this snapshot, list the folder by `parentId`
and compare `modifiedTime` per id; any id that is new or newer needs a
re-pull. Docs are never edited in place in Drive (updates are new delta docs),
so in practice drift means new ids, not changed ones.

## Three families, chained

- **Claude Manifest** — tracked event IDs, REJECTED list, reconciliation log
- **Source List** — every source ever found, with flags (append-only, Rule 0)
- **Source Retention Rules and Flags** — flag vocabulary, deviations, backlog

Each family is a `CONSOLIDATED MASTER` (split into parts) plus dated delta
docs layered on top. A delta names its parents in its header. Read every doc
in a family; the newest delta alone has no TRACKED list, and the master alone
misses the last few days.
