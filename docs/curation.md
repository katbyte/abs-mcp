# A typical curation session

Load the curation tools first: `ABS_TOOLSETS=curation`.

1. `audit_all` says where the library needs work. `audit_unmatched` lists the books never matched to a provider.
2. For each, `item_match` returns candidates with duration, narrator and series. Compare them with the item, then `item_match_apply candidate=N`.
3. `audit_missing field=cover` finds books with no cover. `item_cover_search` and `item_cover_edit` fill the gaps.
4. `audit_missing field=chapters` finds long books with no chapters. `item_chapters_set` pulls them from Audible by asin.
5. `audit_duplicates` and `audit_series` show what to prune and what is missing.

Each pass is the same three steps: run an audit, review the worklist, make a batch of edits.

The full rules of each audit are in [audits.md](audits.md), and every tool is in [tools.md](tools.md).
