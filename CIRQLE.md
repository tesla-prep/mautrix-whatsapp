# CIRQLE fork

CIRQLE's patches on top of mautrix-whatsapp, kept on the `cirqle` branch
(based on the release tag we run). The image is built by
`.github/workflows/cirqle-image.yml` as `ghcr.io/tesla-prep/mautrix-whatsapp:cirqle-<sha>`
and deployed from `cirqle-agents/deploy/team-messenger/prod/compose.yaml`.

## Patches

- WhatsApp channels can be created: group type `channel` in the provisioning
  API (`POST /_matrix/provision/v3/create_group/channel` with `name`, optional
  `topic`) and the `create-channel <name>` command. Accepts the channel terms
  (notice 20601217) and uses the create queries WhatsApp Web sends today
  (whatsmeow's older one is refused), falling back to whatsmeow's.
- `delete-channel` in a channel room deletes the channel (owner only).
- Photos, videos, audio and files sent to a channel room are uploaded as
  channel media (unencrypted, with the upload's media handle).
- Reactions to channel messages are sent with the message's server ID.
- In channels the login administers, every Matrix user in the room may post
  (power level 0), so relayed staff can write there.
- In groups, WhatsApp admins (power level 50 and up, so the account when it
  administers the group) may change Matrix power levels. The relay bot uses it to
  give staff the kick level, so Element's «Remove from room» works for relayed
  staff; the bridge carries a relayed kick out through the relay login.
- Matrix power level changes for people who are not on WhatsApp (staff) are
  accepted quietly instead of answered with "this bridge does not support
  changing group power levels"; changes for WhatsApp members still are.
- A login that knows no name for a contact (its own number, for one) no longer
  renames the contact's ghost to the bare number. Several of our numbers share
  each ghost, so every contact search used to flip a chat's name between the
  name and the number.
- Group admins are made and dismissed through the provisioning API (CIRQLE
  Messenger's «Make group admin» / «Dismiss as admin», sent by the relay bot):
  `POST /_matrix/provision/v3/cirqle/group_admin?user_id=<owner>&login_id=<login>`
  with `{"room_id": "!…", "participant": "@whatsapp_…", "admin": true}`. The
  bridge asks WhatsApp who is in the group, refuses what WhatsApp would (400: not
  a group, not a member, the number itself, dismissing the creator; 403: the
  number is not in the group or not an admin), promotes or demotes, and updates
  the room's power levels at once; 502 is WhatsApp's refusal, 504 no answer.
  Matrix power levels can't do it: an admin (50) can't lower another admin.
- Channels the login owns, administers or follows get rooms: two minutes after
  connecting and then hourly, the bridge lists the subscribed channels
  (`GetSubscribedNewsletters`, at most 100), resyncs each one (name, avatar, the
  login's role) and fetches the posts newer than the room's last one (at most 50,
  so a new room starts with the latest 50). Posts that arrive as messages are
  bridged as before; older posts are not backfilled.
- `com.beeper.delete_chat` with `"com.cirqle.keep_on_phone": true` (CIRQLE
  Messenger's «Leave group», sent by the relay bot as the account) leaves the
  group like WhatsApp's «Exit group» and keeps the chat on the phone; the bridge
  still deletes the portal, so the room closes for everyone in Matrix.

## Updating to a new upstream release

```sh
git fetch upstream --tags
git rebase --onto vX.Y.Z <old tag> cirqle
```
