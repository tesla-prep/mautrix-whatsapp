// mautrix-whatsapp - A Matrix-WhatsApp puppeting bridge.
// Copyright (C) 2026 CIRQLE
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.

package connector

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/simplevent"

	"go.mau.fi/mautrix-whatsapp/pkg/waid"
)

const (
	// The first channel sync waits for the connection and the history sync to settle.
	newsletterSyncDelay = 2 * time.Minute
	// Channel posts normally arrive as messages; the hourly sync catches up on any that didn't.
	newsletterSyncInterval = time.Hour
	// At most this many channels of a login get rooms.
	maxSyncedNewsletters = 100
	// At most this many posts are fetched for a channel at a time, newest first.
	newsletterFetchLimit = 50
)

// newsletterSyncLoop gives every WhatsApp channel the login owns, administers
// or follows a room (CIRQLE): history syncs never list channels, so the bridge
// only opened one when a post or a join arrived while it was running.
func (wa *WhatsAppClient) newsletterSyncLoop(ctx context.Context) {
	if wa.disableNewsletter {
		return
	}
	timer := time.NewTimer(newsletterSyncDelay)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		if cli := wa.Client; cli != nil && cli.IsLoggedIn() {
			wa.syncNewsletters(ctx)
		}
		timer.Reset(newsletterSyncInterval)
	}
}

// syncNewsletters resyncs each subscribed channel, creating its room in the
// login's space if needed, and fetches the posts newer than the room's last one.
func (wa *WhatsAppClient) syncNewsletters(ctx context.Context) {
	log := zerolog.Ctx(ctx).With().Str("action", "sync channels").Logger()
	ctx = log.WithContext(ctx)
	newsletters, err := wa.Client.GetSubscribedNewsletters(ctx)
	if err != nil {
		log.Err(err).Msg("Failed to get subscribed channels")
		return
	}
	known, err := wa.knownNewsletters(ctx)
	if err != nil {
		log.Err(err).Msg("Failed to get the channels the login knows")
	}
	for _, jid := range known {
		if !slices.ContainsFunc(newsletters, func(info *types.NewsletterMetadata) bool { return info != nil && info.ID == jid }) {
			newsletters = append(newsletters, &types.NewsletterMetadata{ID: jid})
		}
	}
	if len(newsletters) > maxSyncedNewsletters {
		log.Warn().Int("channel_count", len(newsletters)).Msg("Too many channels, syncing only the first ones")
		newsletters = newsletters[:maxSyncedNewsletters]
	}
	synced := 0
	for _, info := range newsletters {
		if info == nil || info.ID.IsEmpty() {
			continue
		}
		if info.ViewerMeta == nil {
			// Without the role, a channel the login posts to would turn read-only.
			full, err := wa.Client.GetNewsletterInfo(ctx, info.ID)
			if err != nil || full == nil || full.ViewerMeta == nil {
				log.Warn().Err(err).Stringer("channel_jid", info.ID).Msg("Skipping channel without the login's role")
				continue
			}
			info = full
		}
		if info.ViewerMeta.Role == types.NewsletterRoleGuest {
			log.Debug().Stringer("channel_jid", info.ID).Msg("Skipping channel the login no longer follows")
			continue
		}
		res := wa.UserLogin.QueueRemoteEvent(&simplevent.ChatResync{
			EventMeta: simplevent.EventMeta{
				Type: bridgev2.RemoteEventChatResync,
				LogContext: func(c zerolog.Context) zerolog.Context {
					return c.Stringer("chat_jid", info.ID)
				},
				PortalKey:    wa.makeWAPortalKey(info.ID),
				CreatePortal: true,
			},
			ChatInfo: wa.wrapNewsletterInfo(ctx, info),
			// Posts carry no timestamp here: fetching what is newer than the last one decides.
			CheckNeedsBackfillFunc: func(context.Context, *database.Message) (bool, error) {
				return true, nil
			},
		})
		if !res.Success {
			return
		}
		synced++
	}
	log.Info().Int("channel_count", synced).Msg("Synced WhatsApp channels")
}

const getKnownNewslettersQuery = `
	SELECT chat_jid FROM whatsmeow_chat_settings WHERE our_jid=$1 AND chat_jid LIKE '%@newsletter'
	UNION
	SELECT portal_id FROM user_portal WHERE bridge_id=$2 AND login_id=$3 AND portal_id LIKE '%@newsletter'
`

// knownNewsletters lists the channels the login has seen: those in its chat
// settings (app state, which has every channel on the phone, owned ones
// included) and those it already has portals for. WhatsApp's subscribed list
// can come back empty for a login that owns and follows channels (2026-10-26:
// «Andrey - Personal», 4 channels in its app state, `xwa2_newsletter_subscribed: []`
// every hour), so the list alone gave none of them a room.
func (wa *WhatsAppClient) knownNewsletters(ctx context.Context) ([]types.JID, error) {
	rows, err := wa.Main.DB.Query(ctx, getKnownNewslettersQuery, wa.JID, wa.Main.Bridge.ID, wa.UserLogin.ID)
	if err != nil {
		return nil, err
	}
	var jids []types.JID
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if jid, err := types.ParseJID(raw); err == nil && jid.Server == types.NewsletterServer {
			jids = append(jids, jid)
		}
	}
	return jids, rows.Err()
}

// fetchNewsletterMessages backfills a channel room with its latest posts.
// Channels are missing from history syncs, so the posts come from WhatsApp,
// and only forwards: older posts stay on WhatsApp.
func (wa *WhatsAppClient) fetchNewsletterMessages(ctx context.Context, jid types.JID, params bridgev2.FetchMessagesParams) (*bridgev2.FetchMessagesResponse, error) {
	if !params.Forward {
		return &bridgev2.FetchMessagesResponse{HasMore: false}, nil
	} else if wa.Client == nil {
		return nil, bridgev2.ErrNotLoggedIn
	}
	posts, err := wa.Client.GetNewsletterMessages(ctx, jid, &whatsmeow.GetNewsletterMessagesParams{
		Count: min(params.Count, newsletterFetchLimit),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get channel messages: %w", err)
	}
	var after types.MessageServerID
	if params.AnchorMessage != nil {
		if meta, ok := params.AnchorMessage.Metadata.(*waid.MessageMetadata); ok {
			after = meta.NewsletterServerID
		}
	}
	slices.SortFunc(posts, func(a, b *types.NewsletterMessage) int {
		return cmp.Compare(a.MessageServerID, b.MessageServerID)
	})
	messages := make([]*bridgev2.BackfillMessage, 0, len(posts))
	for _, post := range posts {
		if post.Message == nil || post.MessageServerID <= after {
			continue
		}
		evt := (&events.Message{
			Info: types.MessageInfo{
				MessageSource: types.MessageSource{Chat: jid, Sender: jid},
				ID:            post.MessageID,
				ServerID:      post.MessageServerID,
				Type:          post.Type,
				Timestamp:     post.Timestamp,
			},
			RawMessage: post.Message,
		}).UnwrapRaw()
		if msgType := getMessageType(evt.Message); msgType == "ignore" || strings.HasPrefix(msgType, "unknown_protocol_") {
			continue
		}
		converted, _ := wa.convertHistorySyncMessage(ctx, params.Portal, &evt.Info, evt.Message, evt.RawMessage, false, nil)
		for _, part := range converted.Parts {
			if meta, ok := part.DBMetadata.(*waid.MessageMetadata); ok {
				meta.NewsletterServerID = post.MessageServerID
			}
		}
		messages = append(messages, converted)
	}
	return &bridgev2.FetchMessagesResponse{
		Messages:                messages,
		Forward:                 true,
		AggressiveDeduplication: true,
	}, nil
}
