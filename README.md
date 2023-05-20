# GNATS to gitea, hard-coded for NetBSD GNATS instance

Will feed NetBSD's GNATS issues to a Gitea instance using the SDK

## Caveats

Conversion is likely to lose data:
- Ignores some fields
- Ignores data which is attachments (not yet handled)
- Shows only the plaintext data, not HTML
- Ignores confidential bug reports
