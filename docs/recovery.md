# After a folder is renamed or moved

Audiobookshelf follows a renamed folder by its inode. Some storage gives a moved folder a new one, such as some network and pooled mounts. There, the next scan makes a new record for the folder and flags the old one missing, with everyone's listening progress still on it.

`library_issues_merge` puts the two back together. It is in the `admin` toolset, and loads only with `--enable-delete`.

What it does:

1. Pairs each missing record with the record made since for the same audio files.
2. Carries the old record's details, tags, chapters and cover onto the new one. Every account's progress and bookmarks go too, and its place in collections and playlists.
3. Reads the new record back.
4. Deletes the old record.

Without `confirm` it only reports.

## Its limits

- The day a book was added, and the day each account started it, cannot be carried. The server sets both itself.
- Another account's progress, bookmarks and playlists can only be written as that account. So a confirmed run makes a key for each account that expires in fifteen minutes, and deletes it when the run ends.
- A record holding the same audio that was already in the library before the folder went missing is taken for a second copy, not the folder moved. It is merged into only when `into` names it.
- A missing record with an open RSS feed or share link is left as it is, since deleting it would close them.
- A pair where anything fails to carry is left as it is, old record and all, with the reason.
- Books only. A podcast's progress hangs on its episodes.
